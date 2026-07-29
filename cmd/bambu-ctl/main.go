// bambu-ctl talks to Bambu Lab's cloud and reports 3D-printer state
// while the printer stays in Cloud mode (the P2S generation only serves
// local MQTT in LAN-only + Developer Mode, so cloud is the practical
// channel). The waybar subcommand feeds the custom/p2s pill.
//
// Subcommands:
//   bambu-ctl login    # email + password (or emailed code / 2FA); caches token
//   bambu-ctl status   # human-readable printer state [--raw]
//   bambu-ctl waybar   # one compact JSON line for waybar; always exits 0
//   bambu-ctl pause|resume|stop   # print control (stop confirms first)
//   bambu-ctl speed <level>       # silent|standard|sport|ludicrous
//   bambu-ctl light <on|off>      # chamber light
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
	case "pause", "resume":
		cmdSimplePrint(os.Args[1], os.Args[2:])
	case "toggle":
		cmdToggle(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "speed":
		cmdSpeed(os.Args[2:])
	case "light":
		cmdLight(os.Args[2:])
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
	path := sessionPath(g)
	sess, err := bambu.LoadSession(path)
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
	// Sessions saved before mqtt_user existed (or by the Python prototype)
	// lack the cached username; resolve and persist it once.
	if sess.MQTTUser == "" {
		user, err := sess.MQTTUsername()
		if err != nil {
			if user, err = bambu.UsernameFromAPI(sess.AccessToken); err != nil {
				return nil, "", fmt.Errorf("derive MQTT username: %w", err)
			}
		}
		sess.MQTTUser = user
		if err := sess.Save(path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist mqtt_user: %v\n", err)
		}
	}
	return sess, serial, nil
}

func cmdLogin(args []string) {
	var g globalFlags
	var useToken bool
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.BoolVar(&useToken, "token", false,
		"Paste a browser-session token instead of password login (for Google/Apple SSO accounts, which Bambu's API login doesn't support)")
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

	var token string
	if useToken {
		// SSO accounts have no API-usable password; the `token` cookie a
		// browser holds after a makerworld.com / bambulab.com login is the
		// same cloud access token the API login would have issued.
		fmt.Println("Log in at makerworld.com in a browser (Google/Apple is fine), then")
		fmt.Println("copy the value of the `token` cookie (DevTools → Application → Cookies).")
		token = strings.Trim(prompt("Paste token: "), `"' `)
		if strings.Count(token, ".") != 2 {
			fatal("that doesn't look like a JWT (expected three dot-separated parts) — wrong cookie?")
		}
	} else {
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
		token = res.AccessToken
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
	}
	if token == "" {
		fatal("empty token")
	}

	sess := &bambu.Session{AccessToken: token, Saved: time.Now().Unix(), Name: "P2S"}
	devices, err := bambu.Devices(token)
	if errors.Is(err, bambu.ErrAuth) {
		fatal("the cloud rejected this token — copy it fresh and retry (%v)", err)
	}
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
	if user, err := sess.MQTTUsername(); err == nil {
		sess.MQTTUser = user
	} else if user, err := bambu.UsernameFromAPI(token); err == nil {
		sess.MQTTUser = user
	} else {
		fmt.Fprintf(os.Stderr, "warning: could not derive MQTT username now (will retry on first status): %v\n", err)
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
		// Prefer real %RH (AMS 2 Pro+); older units only have the 1-5 index.
		hum := fmt.Sprintf("humidity idx %d", unit.Humidity.Int())
		if unit.HumidityRaw != nil {
			hum = fmt.Sprintf("humidity %d%%", unit.HumidityRaw.Int())
		}
		line := fmt.Sprintf("ams:       %s, temp %d °C", hum, unit.Temp.Int())
		if unit.HumidityRaw.Int() > 40 {
			line += " — high, consider drying"
		}
		lines = append(lines, line)
		if unit.DryTime.Int() > 0 {
			lines = append(lines, fmt.Sprintf("drying:    %d min left", unit.DryTime.Int()))
		}
		for i, tray := range unit.Tray {
			name := tray.TraySubBrands
			if name == "" {
				name = tray.TrayType
			}
			if name == "" {
				continue
			}
			line := fmt.Sprintf("slot %d:    %s", i+1, name)
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

// sendAndReport wraps SendCommand with uniform CLI output.
func sendAndReport(g globalFlags, section, command string, payload any, label string) {
	sess, serial, err := loadSession(g)
	if err != nil {
		fatal("%v", err)
	}
	if err := bambu.SendCommand(sess, serial, section, command, payload, 10*time.Second); err != nil {
		fatal("%s: %v", label, err)
	}
	fmt.Printf("%s: ok\n", label)
}

func printPayload(command, param string) any {
	return map[string]any{"print": map[string]any{
		"sequence_id": "0", "command": command, "param": param,
	}}
}

func cmdSimplePrint(cmd string, args []string) {
	var g globalFlags
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)
	sendAndReport(g, "print", cmd, printPayload(cmd, ""), cmd)
}

// cmdToggle pauses a running print or resumes a paused one — made for
// the pill's right-click, so "nothing to do" exits 0 quietly.
func cmdToggle(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("toggle", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	sess, serial, err := loadSession(g)
	if err != nil {
		fatal("%v", err)
	}
	rep, _, err := bambu.FetchReport(sess, serial, 15*time.Second)
	if err != nil {
		fatal("%v", err)
	}
	var cmd string
	switch state := strings.ToUpper(rep.Print.GcodeState); state {
	case "RUNNING", "PREPARE":
		cmd = "pause"
	case "PAUSE":
		cmd = "resume"
	default:
		fmt.Printf("nothing to toggle (state %s)\n", state)
		return
	}
	sendAndReport(g, "print", cmd, printPayload(cmd, ""), cmd)
}

func cmdStop(args []string) {
	var g globalFlags
	var yes bool
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	fs.Parse(args)
	if !yes {
		fmt.Print("Abort the current print? It cannot be resumed. [y/N] ")
		var answer string
		fmt.Fscanln(os.Stdin, &answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Println("not stopping.")
			return
		}
	}
	sendAndReport(g, "print", "stop", printPayload("stop", ""), "stop")
}

var speedLevels = map[string]string{
	"silent": "1", "standard": "2", "sport": "3", "ludicrous": "4",
	"1": "1", "2": "2", "3": "3", "4": "4",
}

func cmdSpeed(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("speed", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)
	level := strings.ToLower(fs.Arg(0))
	param, ok := speedLevels[level]
	if !ok {
		fatal("usage: bambu-ctl speed <silent|standard|sport|ludicrous>")
	}
	sendAndReport(g, "print", "print_speed", printPayload("print_speed", param), "speed "+level)
}

func cmdLight(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("light", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)
	mode := strings.ToLower(fs.Arg(0))
	if mode != "on" && mode != "off" {
		fatal("usage: bambu-ctl light <on|off>")
	}
	payload := map[string]any{"system": map[string]any{
		"sequence_id": "0", "command": "ledctrl", "led_node": "chamber_light",
		"led_mode": mode, "led_on_time": 500, "led_off_time": 500,
		"loop_times": 0, "interval_time": 0,
	}}
	sendAndReport(g, "system", "ledctrl", payload, "light "+mode)
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
           token and the printer serial. Google/Apple SSO accounts have
           no API password — use --token and paste the browser's "token"
           cookie from makerworld.com instead
  status   Human-readable printer state [--raw dumps the full report]
  waybar   One JSON line for the waybar pill; always exits 0
  pause    Pause the current print
  resume   Resume a paused print
  toggle   Pause if printing, resume if paused (for the pill's right-click)
  stop     Abort the current print (asks first; --yes to skip)
  speed    Set print speed: silent|standard|sport|ludicrous
  light    Chamber light: on|off

Global flags (all subcommands):
  --config <file>    Session file (default $HOME/.config/bambu-cloud.json)
  --serial <serial>  Printer serial (default: cached at login)`)
	os.Exit(2)
}
