// volvo-ctl talks to Volvo's cloud API (developer.volvocars.com app
// credentials required) and reports EV state. The waybar subcommand
// feeds the custom/volvo pill.
//
// Subcommands:
//   volvo-ctl auth       # one-time (and ~weekly) browser login, stores tokens
//   volvo-ctl vehicles   # emit JSONL of VINs on the account
//   volvo-ctl status     # human-readable battery/charging state
//   volvo-ctl location   # last GPS fix + map links (needs location:read)
//   volvo-ctl climate    # start/stop pre-climatization: climate <start|stop>
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
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// connectedLeaves are the Connected Vehicle v2 resources consulted for
// the tooltip's anomaly lines; `status` additionally pulls odometer
// and trip statistics.
var connectedLeaves = []string{"doors", "windows", "engine-status", "tyres", "warnings", "diagnostics"}

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
	case "location":
		cmdLocation(os.Args[2:])
	case "climate":
		cmdClimate(os.Args[2:])
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
	cv := fetchConnected(c, vin, append([]string{"odometer", "statistics"}, connectedLeaves...))
	for _, line := range statusCarLines(cv) {
		fmt.Println(line)
	}
}

func cmdLocation(args []string) {
	var g globalFlags
	var open bool
	fs := flag.NewFlagSet("location", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.BoolVar(&open, "open", false, "Open the position on OpenStreetMap (xdg-open)")
	fs.Parse(args)

	cfg, c, err := loadAll(g)
	if err != nil {
		fatal("%v", err)
	}
	vin, err := resolveVIN(g, cfg, c)
	if err != nil {
		fatal("%v", err)
	}
	loc, err := c.Location(vin)
	if err != nil {
		if errors.Is(err, volvo.ErrForbidden) {
			fatal("location: %v\nhint: the app lacks the location:read scope — add it on developer.volvocars.com, then re-run: volvo-ctl auth", err)
		}
		fatal("location: %v", err)
	}
	osm := fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=16/%.6f/%.6f",
		loc.Lat, loc.Lon, loc.Lat, loc.Lon)
	fmt.Printf("VIN: %s\n", vin)
	fmt.Printf("Position: %.6f, %.6f\n", loc.Lat, loc.Lon)
	if h := compass(loc.Heading); h != "" {
		fmt.Printf("Heading: %s\n", h)
	}
	if !loc.Timestamp.IsZero() {
		fmt.Printf("Fix age: %s (car reports opportunistically)\n", formatAge(loc.Age(time.Now())))
	}
	fmt.Printf("OSM: %s\n", osm)
	fmt.Printf("Google: https://www.google.com/maps?q=%.6f,%.6f\n", loc.Lat, loc.Lon)
	if open {
		_ = exec.Command("xdg-open", osm).Start()
	}
}

// cmdClimate is the tool's only car-actuating command; everything else
// is read-only by design (see the scopes const in pkg/volvo).
func cmdClimate(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("climate", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)
	action := fs.Arg(0)
	if action != "start" && action != "stop" {
		fatal("usage: volvo-ctl climate <start|stop>")
	}

	cfg, c, err := loadAll(g)
	if err != nil {
		fatal("%v", err)
	}
	vin, err := resolveVIN(g, cfg, c)
	if err != nil {
		fatal("%v", err)
	}
	res, err := c.Command(vin, "climatization-"+action)
	if err != nil {
		if errors.Is(err, volvo.ErrForbidden) {
			fatal("climate: %v\nhint: the app/consent lacks conve:climatization_start_stop — re-run: volvo-ctl auth", err)
		}
		fatal("climate: %v", err)
	}
	msg := res.InvokeStatus
	if res.Message != "" {
		msg += " (" + res.Message + ")"
	}
	fmt.Printf("Climatization %s: %s\n", action, msg)
	switch res.InvokeStatus {
	case "COMPLETED", "RUNNING", "DELIVERED", "WAITING":
	default:
		os.Exit(1)
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
	tooltip := "<b>Volvo XC40 · " + escapePango(vin) + "</b>\n" + escapePango(strings.Join(summaryLines(st), "\n"))
	// Location is garnish: any failure (typically ErrForbidden before
	// the scope/consent exists) leaves the pill battery-only.
	if loc, lerr := c.Location(vin); lerr == nil {
		tooltip += "\n" + escapePango(strings.Join(locationLines(loc), "\n"))
	} else if !errors.Is(lerr, volvo.ErrForbidden) {
		fmt.Fprintln(os.Stderr, lerr)
	}
	if lines := carLines(fetchConnected(c, vin, connectedLeaves)); len(lines) > 0 {
		tooltip += "\n" + escapePango(strings.Join(lines, "\n"))
	}
	return waybar.Waybar{
		Text:    fmt.Sprintf("%s %d%%", icon, pct),
		Class:   classify(pct, charging),
		ToolTip: tooltip,
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

// fetchConnected pulls the given Connected Vehicle leaves concurrently.
// Each leaf is garnish: a missing scope (ErrForbidden — e.g. an older
// portal app) drops the leaf silently, other errors are logged to
// stderr, and callers just see an absent map entry either way.
func fetchConnected(c *volvo.Client, vin string, leaves []string) map[string]volvo.CVMap {
	cv := make(map[string]volvo.CVMap, len(leaves))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, leaf := range leaves {
		wg.Add(1)
		go func(leaf string) {
			defer wg.Done()
			m, err := c.Connected(vin, leaf)
			if err != nil {
				if !errors.Is(err, volvo.ErrForbidden) {
					fmt.Fprintln(os.Stderr, err)
				}
				return
			}
			mu.Lock()
			cv[leaf] = m
			mu.Unlock()
		}(leaf)
	}
	wg.Wait()
	return cv
}

// openParts collects the human-readable names of open doors/windows.
// skip names a field to ignore (doors carry centralLock alongside the
// actual doors).
func openParts(m volvo.CVMap, skip string) []string {
	var open []string
	for k, f := range m {
		if k != skip && openish(string(f.Value)) {
			open = append(open, humanKey(k))
		}
	}
	sort.Strings(open)
	return open
}

// warnLines renders tyre/bulb/service warnings; empty when all clear.
func warnLines(cv map[string]volvo.CVMap) []string {
	var warn []string
	for k, f := range cv["tyres"] {
		if !nominal(string(f.Value)) {
			warn = append(warn, fmt.Sprintf("Tyre %s: %s", humanKey(k), humanVal(string(f.Value))))
		}
	}
	bulbs := 0
	for _, f := range cv["warnings"] {
		if !nominal(string(f.Value)) {
			bulbs++
		}
	}
	if bulbs > 0 {
		warn = append(warn, fmt.Sprintf("%d bulb warning(s)", bulbs))
	}
	for k, f := range cv["diagnostics"] {
		if strings.HasSuffix(k, "Warning") && !nominal(string(f.Value)) {
			warn = append(warn, fmt.Sprintf("%s: %s", humanKey(k), humanVal(string(f.Value))))
		}
	}
	sort.Strings(warn)
	return warn
}

// carLines renders Connected Vehicle state for the tooltip — anomalies
// only, so a locked, closed, warning-free car contributes exactly one
// reassuring line.
func carLines(cv map[string]volvo.CVMap) []string {
	var lines []string
	open := append(openParts(cv["doors"], "centralLock"), openParts(cv["windows"], "")...)
	sort.Strings(open)
	lock := strings.TrimSpace(string(cv["doors"]["centralLock"].Value))
	switch {
	case len(open) > 0:
		lines = append(lines, "Open: "+strings.Join(open, ", "))
		if lock == "UNLOCKED" {
			lines = append(lines, "Unlocked")
		}
	case lock == "UNLOCKED":
		lines = append(lines, "Unlocked")
	case lock == "LOCKED":
		lines = append(lines, "Locked, all closed")
	}
	if strings.TrimSpace(string(cv["engine-status"]["engineStatus"].Value)) == "RUNNING" {
		lines = append(lines, "Ignition on")
	}
	return append(lines, warnLines(cv)...)
}

// statusCarLines is carLines' verbose sibling for `status` — it states
// the nominal cases too and adds odometer/service/trip numbers.
func statusCarLines(cv map[string]volvo.CVMap) []string {
	var lines []string
	if f, ok := cv["odometer"]["odometer"]; ok {
		lines = append(lines, fmt.Sprintf("Odometer: %s %s", f.Value, f.Unit))
	}
	if f, ok := cv["engine-status"]["engineStatus"]; ok {
		lines = append(lines, "Ignition: "+humanVal(string(f.Value)))
	}
	if f, ok := cv["doors"]["centralLock"]; ok {
		lines = append(lines, "Lock: "+humanVal(string(f.Value)))
	}
	for _, part := range []struct{ leaf, label, skip string }{
		{"doors", "Doors", "centralLock"},
		{"windows", "Windows", ""},
	} {
		if len(cv[part.leaf]) == 0 {
			continue
		}
		if open := openParts(cv[part.leaf], part.skip); len(open) > 0 {
			lines = append(lines, part.label+" open: "+strings.Join(open, ", "))
		} else {
			lines = append(lines, part.label+": all closed")
		}
	}
	if warn := warnLines(cv); len(warn) > 0 {
		lines = append(lines, warn...)
	} else if len(cv["tyres"]) > 0 || len(cv["warnings"]) > 0 || len(cv["diagnostics"]) > 0 {
		lines = append(lines, "Warnings: none")
	}
	if f, ok := cv["diagnostics"]["distanceToService"]; ok {
		lines = append(lines, fmt.Sprintf("Service in: %s %s", f.Value, f.Unit))
	}
	if f, ok := cv["statistics"]["distanceToEmptyBattery"]; ok {
		lines = append(lines, fmt.Sprintf("Distance to empty: %s %s", f.Value, f.Unit))
	}
	if f, ok := cv["statistics"]["averageEnergyConsumption"]; ok {
		lines = append(lines, fmt.Sprintf("Avg consumption: %s %s", f.Value, f.Unit))
	}
	return lines
}

// nominal reports whether a Connected Vehicle enum value carries no
// news worth surfacing.
func nominal(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "NO_WARNING", "UNSPECIFIED":
		return true
	}
	return false
}

func openish(v string) bool {
	s := strings.TrimSpace(v)
	return s == "OPEN" || s == "AJAR"
}

// humanKey renders a camelCase API field name as spaced lowercase
// words ("frontLeftDoor" → "front left door").
func humanKey(k string) string {
	var b []byte
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b = append(b, ' ')
			}
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}

// humanVal renders an ENUM_VALUE as lowercase words.
func humanVal(v string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(v), "_", " "))
}

// locationLines renders the GPS fix for the tooltip — a sibling of
// summaryLines so `status` stays location-free (it has its own richer
// `location` subcommand).
func locationLines(loc *volvo.Location) []string {
	var lines []string
	pos := fmt.Sprintf("%.5f, %.5f", loc.Lat, loc.Lon)
	if !loc.Timestamp.IsZero() {
		lines = append(lines, fmt.Sprintf("Last seen: %s ago · %s", formatAge(loc.Age(time.Now())), pos))
	} else {
		lines = append(lines, "Position: "+pos)
	}
	if h := compass(loc.Heading); h != "" {
		lines = append(lines, "Heading: "+h)
	}
	return lines
}

// compass renders an API heading (degrees as a string, may be empty or
// junk) as a compass point; empty string when unparsable.
func compass(heading string) string {
	deg, err := strconv.ParseFloat(strings.TrimSpace(heading), 64)
	if err != nil {
		return ""
	}
	points := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	idx := int(deg/45+0.5) % 8
	if idx < 0 {
		idx += 8
	}
	return fmt.Sprintf("%s (%.0f°)", points[idx], deg)
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
  location   Last GPS fix + map links [--vin <vin>] [--open]
  climate    Start/stop pre-climatization: climate <start|stop>
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
