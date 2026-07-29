// bambu-ctl talks to Bambu Lab's cloud and reports 3D-printer state
// while the printer stays in Cloud mode (the P2S generation only serves
// local MQTT in LAN-only + Developer Mode, so cloud is the practical
// channel). The waybar subcommand feeds the custom/p2s pill.
//
// Subcommands:
//   bambu-ctl login    # email + password (or emailed code / 2FA); caches token
//   bambu-ctl status   # human-readable printer state [--raw]
//   bambu-ctl waybar   # one compact JSON line for waybar; always exits 0
//
// Token cache: ~/.config/bambu-cloud.json (~3-month expiry; the waybar
// pill flips to its reauth state when a new login is needed).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
	"github.com/polarn/waybar-modules/pkg/waybar"
	"golang.org/x/term"
)

// Written as an escape, not a literal glyph — editors/tools have silently
// dropped raw Nerd Font PUA glyphs from this repo before.
const iconPrinter = "\U000F042B" // nf-md-printer_3d

type globalFlags struct {
	config string
	serial string
}

func addGlobal(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.config, "config", "", "Session file (default $HOME/.config/bambu-cloud.json)")
	fs.StringVar(&g.serial, "serial", "", "Printer serial (default: the one cached at login)")
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
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

func sessionPath(g globalFlags) string {
	if g.config != "" {
		return g.config
	}
	path, err := bambu.DefaultPath()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return path
}

// loadSession + serial resolution shared by status/waybar.
func loadSession(g globalFlags) (*bambu.Session, string, error) {
	sess, err := bambu.LoadSession(sessionPath(g))
	if err != nil {
		return nil, "", err
	}
	serial := g.serial
	if serial == "" {
		serial = sess.Serial
	}
	if serial == "" {
		return nil, "", errors.New("no printer serial cached — rerun: bambu-ctl login")
	}
	return sess, serial, nil
}

func cmdLogin(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	stdin := bufio.NewReader(os.Stdin)
	prompt := func(label string) string {
		fmt.Print(label)
		line, err := stdin.ReadString('\n')
		if err != nil {
			fatal("read input: %v", err)
		}
		return strings.TrimSpace(line)
	}

	email := prompt("Bambu account email: ")
	fmt.Print("Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		fatal("read password: %v", err)
	}

	res, err := bambu.LoginPassword(email, string(pw))
	if err != nil {
		fatal("login: %v", err)
	}
	token := res.AccessToken
	switch {
	case token != "":
	case res.LoginType == "verifyCode":
		if err := bambu.RequestEmailCode(email); err != nil {
			fatal("request email code: %v", err)
		}
		code := prompt("Verification code (check your email): ")
		res, err := bambu.LoginCode(email, code)
		if err != nil {
			fatal("code login: %v", err)
		}
		token = res.AccessToken
	case res.LoginType == "tfa":
		code := prompt("Two-factor code: ")
		token, err = bambu.LoginTFA(res.TFAKey, code)
		if err != nil {
			fatal("tfa login: %v", err)
		}
	}
	if token == "" {
		fatal("login failed: no token issued (loginType %q)", res.LoginType)
	}

	sess := &bambu.Session{AccessToken: token, Saved: time.Now().Unix(), Name: "P2S"}
	devices, err := bambu.Devices(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list printers: %v\n", err)
	}
	for _, d := range devices {
		fmt.Printf("  found: %s %q serial %s online=%v\n",
			d.DevProductName, d.Name, d.DevID, d.Online)
	}
	if len(devices) > 0 {
		sess.Serial = devices[0].DevID
		if devices[0].Name != "" {
			sess.Name = devices[0].Name
		}
	}
	if g.serial != "" {
		sess.Serial = g.serial
	}

	path := sessionPath(g)
	if err := sess.Save(path); err != nil {
		fatal("save session: %v", err)
	}
	fmt.Printf("token saved to %s (valid ~3 months)\n", path)
}

func cmdStatus(args []string) {
	var g globalFlags
	var raw bool
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.BoolVar(&raw, "raw", false, "Dump the full JSON report instead of a summary")
	fs.Parse(args)

	sess, serial, err := loadSession(g)
	if err != nil {
		fatal("%v", err)
	}
	rep, rawBody, err := bambu.FetchReport(sess, serial, 25*time.Second)
	if err != nil {
		fatal("%v", err)
	}
	if raw {
		var pretty bytes.Buffer
		if json.Indent(&pretty, rawBody, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(rawBody))
		}
		return
	}
	for _, line := range summaryLines(rep) {
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
		fmt.Printf("{\"text\":\"%s ?\"}\n", iconPrinter)
	}
}

func buildWaybar(g globalFlags) waybar.Waybar {
	sess, serial, err := loadSession(g)
	if err != nil {
		if errors.Is(err, bambu.ErrNoSession) {
			return waybar.Waybar{
				Text:  iconPrinter + " login",
				Class: "setup",
				ToolTip: "<b>Bambu Cloud: login needed</b>\n" +
					"Run: bambu-ctl login",
			}
		}
		return errPill(err)
	}
	rep, _, err := bambu.FetchReport(sess, serial, 15*time.Second)
	switch {
	case errors.Is(err, bambu.ErrAuth):
		fmt.Fprintln(os.Stderr, err)
		return waybar.Waybar{
			Text:  iconPrinter + " login",
			Class: "reauth",
			ToolTip: "<b>Bambu Cloud token expired</b>\n" +
				"Run: bambu-ctl login",
		}
	case errors.Is(err, bambu.ErrNoReport):
		fmt.Fprintln(os.Stderr, err)
		return waybar.Waybar{
			Text:    iconPrinter,
			Class:   "offline",
			ToolTip: "<b>" + escapePango(sess.Name) + " offline</b>\n(or rate-limited poll)",
		}
	case err != nil:
		return errPill(err)
	}

	p := rep.Print
	state := strings.ToUpper(p.GcodeState)
	if state == "" {
		state = "IDLE"
	}
	tooltip := "<b>Bambu " + escapePango(sess.Name) + "</b>\n" +
		escapePango(strings.Join(summaryLines(rep), "\n"))

	var text, class string
	switch state {
	case "RUNNING", "PREPARE", "SLICING":
		text = fmt.Sprintf("%s %d%% %dm", iconPrinter, p.McPercent.Int(), p.McRemainingTime.Int())
		class = "printing"
	case "PAUSE":
		text = fmt.Sprintf("%s %d%%", iconPrinter, p.McPercent.Int())
		class = "paused"
	case "FAILED":
		text = fmt.Sprintf("%s %d°", iconPrinter, p.NozzleTemper.Int())
		class = "failed"
	default: // IDLE / FINISH
		text = fmt.Sprintf("%s %d°", iconPrinter, p.NozzleTemper.Int())
		class = "idle"
	}
	return waybar.Waybar{Text: text, Class: class, ToolTip: tooltip}
}

// summaryLines renders the human-readable state shared by `status` and
// the pill tooltip.
func summaryLines(rep *bambu.Report) []string {
	p := rep.Print
	state := strings.ToUpper(p.GcodeState)
	if state == "" {
		state = "IDLE"
	}
	job := p.SubtaskName
	if job == "" {
		job = "-"
	}
	lines := []string{
		"state:     " + state,
		"job:       " + job,
		fmt.Sprintf("progress:  %d%%  (layer %d/%d, %d min left)",
			p.McPercent.Int(), p.LayerNum.Int(), p.TotalLayerNum.Int(),
			p.McRemainingTime.Int()),
		fmt.Sprintf("nozzle:    %d °C", p.NozzleTemper.Int()),
		fmt.Sprintf("bed:       %d °C", p.BedTemper.Int()),
		fmt.Sprintf("chamber:   %d °C", p.ChamberTemper.Int()),
	}
	if len(p.AMS.AMS) > 0 {
		unit := p.AMS.AMS[0]
		lines = append(lines, fmt.Sprintf("ams:       humidity idx %d, temp %d °C",
			unit.Humidity.Int(), unit.Temp.Int()))
		for i, tray := range unit.Tray {
			if tray.TrayType == "" {
				continue
			}
			line := fmt.Sprintf("slot %d:    %s", i+1, tray.TrayType)
			if tray.TrayColor != "" {
				color := tray.TrayColor
				if len(color) > 6 {
					color = color[:6]
				}
				line += " #" + color
			}
			if tray.Remain != nil && tray.Remain.Int() >= 0 {
				line += fmt.Sprintf(" %d%%", tray.Remain.Int())
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func errPill(err error) waybar.Waybar {
	fmt.Fprintln(os.Stderr, err)
	return waybar.Waybar{
		Text:    iconPrinter + " !",
		Class:   "error",
		ToolTip: "<b>Bambu cloud error</b>\n" + escapePango(err.Error()),
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
	fmt.Fprintln(os.Stderr, `Usage: bambu-ctl <subcommand> [flags]

Subcommands:
  login    Email + password (or emailed code / 2FA); caches the cloud
           token and the printer serial
  status   Human-readable printer state [--raw dumps the full report]
  waybar   One JSON line for the waybar pill; always exits 0

Global flags (all subcommands):
  --config <file>    Session file (default $HOME/.config/bambu-cloud.json)
  --serial <serial>  Printer serial (default: cached at login)`)
	os.Exit(2)
}
