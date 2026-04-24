// tradfri-ctl is a control CLI for a DIRIGERA hub. Companion to
// waybar-tradfri (read-only daemon) — this one can toggle and set
// light state.
//
// Subcommands:
//   tradfri-ctl list                 # emit JSONL of all lights (id, name, on, color, label, pct)
//   tradfri-ctl toggle --id <id>     # flip isOn
//   tradfri-ctl set    --id <id> --on|--off
//   tradfri-ctl set    --id <id> --brightness 0-100
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/polarn/waybar-modules/pkg/dirigera"
)

type globalFlags struct {
	host      string
	tokenPath string
}

func addGlobal(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.host, "host", "", "DIRIGERA hostname or IP (required)")
	fs.StringVar(&g.tokenPath, "token", "", "Path to access token (default $HOME/.config/waybar-tradfri/token)")
}

func mustClient(g globalFlags) *dirigera.Client {
	if g.host == "" {
		fatal("--host is required")
	}
	path := g.tokenPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("resolve home dir: %v", err)
		}
		path = filepath.Join(home, ".config", "waybar-tradfri", "token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("read token (%s): %v\n\nRun waybar-tradfri-auth first.", path, err)
	}
	token := strings.TrimSpace(string(data))
	return dirigera.NewClient(g.host, token)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "list":
		cmdList(os.Args[2:])
	case "toggle":
		cmdToggle(os.Args[2:])
	case "set":
		cmdSet(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
	}
}

type listEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	On         bool   `json:"on"`
	Color      string `json:"color"`
	Label      string `json:"label"`
	Brightness int    `json:"brightness"`
}

func cmdList(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	c := mustClient(g)
	devices, err := c.Devices()
	if err != nil {
		fatal("list: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	for _, d := range devices {
		if d.Type != "light" {
			continue
		}
		a := d.Attributes
		entry := listEntry{
			ID:         d.ID,
			Name:       strings.TrimSpace(a.CustomName),
			On:         a.IsOn,
			Color:      a.Hex(),
			Label:      a.Label(),
			Brightness: a.LightLevel,
		}
		_ = enc.Encode(entry)
	}
}

func cmdToggle(args []string) {
	var g globalFlags
	var id string
	fs := flag.NewFlagSet("toggle", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.StringVar(&id, "id", "", "Device ID to toggle (required)")
	fs.Parse(args)

	if id == "" {
		fatal("--id is required")
	}
	c := mustClient(g)

	devices, err := c.Devices()
	if err != nil {
		fatal("read current state: %v", err)
	}
	var current *dirigera.Device
	for i := range devices {
		if devices[i].ID == id {
			current = &devices[i]
			break
		}
	}
	if current == nil {
		fatal("device %q not found", id)
	}
	next := !current.Attributes.IsOn
	if err := c.SetLightOn(id, next); err != nil {
		fatal("set: %v", err)
	}
	fmt.Printf("%s: %v\n", strings.TrimSpace(current.Attributes.CustomName), onOff(next))
}

func cmdSet(args []string) {
	var g globalFlags
	var id string
	var on, off bool
	var brightness int
	fs := flag.NewFlagSet("set", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.StringVar(&id, "id", "", "Device ID (required)")
	fs.BoolVar(&on, "on", false, "Turn light on")
	fs.BoolVar(&off, "off", false, "Turn light off")
	fs.IntVar(&brightness, "brightness", -1, "Set brightness 0-100 (omit to leave unchanged)")
	fs.Parse(args)

	if id == "" {
		fatal("--id is required")
	}
	if on && off {
		fatal("cannot use --on and --off together")
	}
	c := mustClient(g)

	if on || off {
		if err := c.SetLightOn(id, on); err != nil {
			fatal("set isOn: %v", err)
		}
	}
	if brightness >= 0 {
		if err := c.SetLightBrightness(id, brightness); err != nil {
			fatal("set brightness: %v", err)
		}
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: tradfri-ctl <subcommand> [flags]

Subcommands:
  list                 Emit one JSON per light to stdout
  toggle --id <id>     Flip a light's on/off state
  set    --id <id> --on|--off [--brightness 0-100]

Global flags (all subcommands):
  --host <host>        DIRIGERA hostname/IP (required)
  --token <path>       Token file (default $HOME/.config/waybar-tradfri/token)`)
	os.Exit(2)
}
