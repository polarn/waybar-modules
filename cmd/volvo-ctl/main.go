// volvo-ctl talks to Volvo's cloud API (developer.volvocars.com app
// credentials required) and reports EV state. The waybar subcommand
// feeds the custom/volvo pill.
//
// Subcommands:
//   volvo-ctl auth       # one-time (and ~weekly) browser login, stores tokens
//   volvo-ctl vehicles   # emit JSONL of VINs on the account
//   volvo-ctl status     # human-readable battery/charging state
//   volvo-ctl waybar     # one compact JSON line for waybar; always exits 0
//
// Config: ~/.config/volvo/config.json {client_id, client_secret,
// vcc_api_key, vin?}; tokens are managed in ~/.config/volvo/tokens.json.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/volvo"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

// Written as escapes, not literal glyphs — editors/tools have silently
// dropped raw Nerd Font PUA glyphs from this repo before.
const (
	iconCar  = "\U000F0B6C" // nf-md-car_electric
	iconBolt = "\U000F140B" // nf-md-lightning_bolt
)

type globalFlags struct {
	configDir string
	vin       string
}

func addGlobal(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.configDir, "config", "", "Config directory (default $HOME/.config/volvo)")
	fs.StringVar(&g.vin, "vin", "", "VIN to query (default: config.json vin, else sole vehicle on the account)")
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "auth":
		cmdAuth(os.Args[2:])
	case "vehicles":
		cmdVehicles(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "waybar":
		cmdWaybar(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
	}
}

// configDir resolves the effective config directory.
func configDir(g globalFlags) string {
	if g.configDir != "" {
		return g.configDir
	}
	dir, err := volvo.DefaultDir()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return dir
}

// loadAll loads config and builds a client. Errors propagate so the
// waybar path can map them to pill states instead of exiting.
func loadAll(g globalFlags) (*volvo.Config, *volvo.Client, error) {
	dir := configDir(g)
	cfg, err := volvo.LoadConfig(dir)
	if err != nil {
		return nil, nil, err
	}
	store := &volvo.TokenStore{Path: filepath.Join(dir, "tokens.json")}
	return cfg, volvo.NewClient(cfg, store), nil
}

// resolveVIN picks the VIN: flag > config > sole vehicle on the account.
func resolveVIN(g globalFlags, cfg *volvo.Config, c *volvo.Client) (string, error) {
	if g.vin != "" {
		return g.vin, nil
	}
	if cfg.VIN != "" {
		return cfg.VIN, nil
	}
	vins, err := c.Vehicles()
	if err != nil {
		return "", err
	}
	switch len(vins) {
	case 0:
		return "", errors.New("no vehicles on this Volvo ID")
	case 1:
		fmt.Fprintf(os.Stderr, "hint: pin \"vin\": %q in config.json to skip this lookup\n", vins[0])
		return vins[0], nil
	default:
		return "", fmt.Errorf("multiple vehicles (%s) — set vin in config.json or pass --vin", strings.Join(vins, ", "))
	}
}

func cmdAuth(args []string) {
	var g globalFlags
	var redirectPort int
	var noBrowser bool
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.IntVar(&redirectPort, "redirect-port", 0, "Localhost callback port (default 20999; must match the redirect URI registered on the app)")
	fs.BoolVar(&noBrowser, "no-browser", false, "Don't invoke xdg-open; just print the login URL")
	fs.Parse(args)

	dir := configDir(g)
	cfg, err := volvo.LoadConfig(dir)
	if err != nil {
		fatal("%v", err)
	}
	if redirectPort != 0 {
		cfg.RedirectPort = redirectPort
	}

	tok, err := volvo.Authenticate(cfg, !noBrowser)
	if err != nil {
		fatal("auth: %v", err)
	}
	store := &volvo.TokenStore{Path: filepath.Join(dir, "tokens.json")}
	if err := store.WithLock(func() error { return store.Save(tok) }); err != nil {
		fatal("save tokens: %v", err)
	}
	fmt.Println("Authenticated.")

	c := volvo.NewClient(cfg, store)
	vins, err := c.Vehicles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list vehicles: %v\n", err)
		return
	}
	fmt.Printf("Vehicles on this Volvo ID: %s\n", strings.Join(vins, ", "))
	if cfg.VIN == "" && len(vins) > 0 {
		fmt.Printf("Add \"vin\": %q to %s/config.json to pin one.\n", vins[0], dir)
	}
}

func cmdVehicles(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("vehicles", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	_, c, err := loadAll(g)
	if err != nil {
		fatal("%v", err)
	}
	vins, err := c.Vehicles()
	if err != nil {
		fatal("vehicles: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	for _, vin := range vins {
		_ = enc.Encode(struct {
			VIN string `json:"vin"`
		}{vin})
	}
}

func cmdStatus(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	cfg, c, err := loadAll(g)
	if err != nil {
		fatal("%v", err)
	}
	vin, err := resolveVIN(g, cfg, c)
	if err != nil {
		fatal("%v", err)
	}
	st, err := c.EnergyState(vin)
	if err != nil {
		fatal("energy state: %v", err)
	}
	fmt.Printf("VIN: %s\n", vin)
	for _, line := range summaryLines(st) {
		fmt.Println(line)
	}
}

// cmdWaybar prints exactly one compact JSON line and always exits 0 —
// a bad exit or garbage output would blank or break the pill. Errors
// are additionally logged to stderr, which waybar forwards to the
// journal.
func cmdWaybar(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("waybar", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	out := buildWaybar(g)
	if err := out.Print(); err != nil {
		fmt.Printf("{\"text\":\"%s ?\"}\n", iconCar)
	}
}

func buildWaybar(g globalFlags) waybar.Waybar {
	cfg, c, err := loadAll(g)
	if err != nil {
		if errors.Is(err, volvo.ErrNoConfig) {
			return waybar.Waybar{
				Text:  iconCar + " setup",
				Class: "setup",
				ToolTip: "<b>Volvo: setup needed</b>\n" +
					"Create ~/.config/volvo/config.json with\n" +
					"client_id / client_secret / vcc_api_key\n" +
					"(developer.volvocars.com), then run: volvo-ctl auth\n\n" + escapePango(err.Error()),
			}
		}
		return errPill(err)
	}
	vin, err := resolveVIN(g, cfg, c)
	if err != nil {
		return authOrErrPill(err)
	}
	st, err := c.EnergyState(vin)
	if err != nil {
		return authOrErrPill(err)
	}
	if !st.BatteryChargeLevel.OK() {
		return errPill(fmt.Errorf("batteryChargeLevel unavailable (status %s, code %s)",
			st.BatteryChargeLevel.Status, st.BatteryChargeLevel.Code))
	}
	pct, err := st.BatteryChargeLevel.Int()
	if err != nil {
		return errPill(fmt.Errorf("parse charge level: %v", err))
	}
	charging := st.ChargingStatus.OK() &&
		strings.EqualFold(strings.TrimSpace(string(st.ChargingStatus.Value)), "CHARGING")

	icon := iconCar
	if charging {
		icon += iconBolt
	}
	return waybar.Waybar{
		Text:    fmt.Sprintf("%s %d%%", icon, pct),
		Class:   classify(pct, charging),
		ToolTip: "<b>Volvo XC40 · " + escapePango(vin) + "</b>\n" + escapePango(strings.Join(summaryLines(st), "\n")),
	}
}

// classify uses EV thresholds, not the laptop pill's 40/20 — a parked
// EV at 35% is healthy; 20% means plan charging, 10% means charge now.
func classify(pct int, charging bool) string {
	switch {
	case pct < 10:
		return "critical"
	case pct < 20:
		return "low"
	case charging:
		return "charging"
	}
	return "ok"
}

// summaryLines renders the human-readable state shared by `status` and
// the pill tooltip. Fields whose status isn't OK are omitted.
func summaryLines(st *volvo.EnergyState) []string {
	var lines []string

	if st.BatteryChargeLevel.OK() {
		if pct, err := st.BatteryChargeLevel.Int(); err == nil {
			line := fmt.Sprintf("Battery: %d%%", pct)
			if st.TargetBatteryChargeLevel.OK() {
				if target, err := st.TargetBatteryChargeLevel.Int(); err == nil {
					line += fmt.Sprintf(" (target %d%%)", target)
				}
			}
			lines = append(lines, line)
		}
	}
	if st.ElectricRange.OK() {
		// Unit is region-dependent (km or mi) — print what the API says.
		lines = append(lines, fmt.Sprintf("Range: %s %s", st.ElectricRange.Value, st.ElectricRange.Unit))
	}
	charging := st.ChargingStatus.OK() &&
		strings.EqualFold(strings.TrimSpace(string(st.ChargingStatus.Value)), "CHARGING")
	if charging {
		line := "Charging"
		if st.ChargingPower.OK() {
			if w, err := st.ChargingPower.Int(); err == nil {
				line += fmt.Sprintf(": %.1f kW", float64(w)/1000)
			}
		}
		if st.ChargingTimeToTarget.OK() {
			if min, err := st.ChargingTimeToTarget.Int(); err == nil {
				line += fmt.Sprintf(", ~%s to target", formatMinutes(min))
			}
		}
		lines = append(lines, line)
	} else if st.ChargerConnectionStatus.OK() {
		lines = append(lines, "Charger: "+strings.ToLower(string(st.ChargerConnectionStatus.Value)))
	}
	if st.BatteryChargeLevel.OK() && !st.BatteryChargeLevel.UpdatedAt.IsZero() {
		age := st.BatteryChargeLevel.Age(time.Now())
		lines = append(lines, fmt.Sprintf("Data age: %s (car reports opportunistically)", formatAge(age)))
	}
	return lines
}

func formatMinutes(min int) string {
	if min < 60 {
		return fmt.Sprintf("%dmin", min)
	}
	return fmt.Sprintf("%dh%02dmin", min/60, min%60)
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func authOrErrPill(err error) waybar.Waybar {
	if errors.Is(err, volvo.ErrNoTokens) || errors.Is(err, volvo.ErrReauthNeeded) {
		fmt.Fprintln(os.Stderr, err)
		return waybar.Waybar{
			Text:    iconCar + " auth",
			Class:   "reauth",
			ToolTip: "<b>Volvo login needed</b>\nRun: volvo-ctl auth\n\n" + escapePango(err.Error()),
		}
	}
	return errPill(err)
}

func errPill(err error) waybar.Waybar {
	fmt.Fprintln(os.Stderr, err)
	return waybar.Waybar{
		Text:    iconCar + " —",
		Class:   "error",
		ToolTip: "<b>Volvo API error</b>\n" + escapePango(err.Error()),
	}
}

// escapePango escapes the five XML entities so API-supplied strings
// can't break (or inject) waybar's Pango-markup tooltip.
func escapePango(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&#39;",
		"\"", "&#34;",
	).Replace(s)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: volvo-ctl <subcommand> [flags]

Subcommands:
  auth       Browser login (one-time, and again when the grant lapses)
             [--redirect-port N] [--no-browser]
  vehicles   Emit one JSON per VIN to stdout
  status     Human-readable battery/charging state [--vin <vin>]
  waybar     One JSON line for the waybar pill; always exits 0

Global flags (all subcommands):
  --config <dir>   Config directory (default $HOME/.config/volvo)
  --vin <vin>      VIN override (default: config.json, else sole vehicle)

Config file ($HOME/.config/volvo/config.json, chmod 600):
  {"client_id": "...", "client_secret": "...", "vcc_api_key": "...",
   "redirect_uri": "https://<your-pages>/volvo-callback/", "vin": "..."}
  redirect_uri is the public forwarding page registered on the portal app
  (the portal bans localhost); omit it only for apps registered with the
  legacy http://localhost:20999/callback.`)
	os.Exit(2)
}
