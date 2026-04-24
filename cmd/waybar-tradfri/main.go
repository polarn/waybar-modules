// waybar-tradfri is a long-running daemon that polls a DIRIGERA hub
// and emits waybar JSON summarising the state of a user-selected set
// of lights.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/dirigera"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

// offColor is the greyed-out swatch shown for a light that's off.
const offColor = "#4a4a58"

type lightList []string

func (l *lightList) String() string     { return strings.Join(*l, ", ") }
func (l *lightList) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	host := flag.String("host", "", "DIRIGERA hostname or IP (required)")
	tokenPath := flag.String("token", "", "Path to access token (default $HOME/.config/waybar-tradfri/token)")
	interval := flag.Int("interval", 5, "Polling interval in seconds")
	var lights lightList
	flag.Var(&lights, "light", "Friendly name of a light to include (repeatable). If omitted, all lights are shown.")
	flag.Parse()

	if *host == "" {
		log.Fatal("--host is required")
	}
	if *tokenPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("resolve home dir: %v", err)
		}
		*tokenPath = filepath.Join(home, ".config", "waybar-tradfri", "token")
	}
	token, err := readToken(*tokenPath)
	if err != nil {
		log.Fatalf("read token (%s): %v\n\nRun waybar-tradfri-auth first.", *tokenPath, err)
	}

	c := dirigera.NewClient(*host, token)
	want := make(map[string]bool, len(lights))
	for _, n := range lights {
		want[strings.TrimSpace(n)] = true
	}

	for ; ; time.Sleep(time.Duration(*interval) * time.Second) {
		emit(c, want)
	}
}

func emit(c *dirigera.Client, want map[string]bool) {
	w := waybar.New()

	devices, err := c.Devices()
	if err != nil {
		w.Text = "tradfri ?"
		w.ToolTip = fmt.Sprintf("Error polling DIRIGERA:\n%s", err.Error())
		w.Class = "error"
		_ = w.Print()
		return
	}

	var selected []dirigera.Device
	for _, d := range devices {
		if d.Type != "light" {
			continue
		}
		if len(want) > 0 && !want[strings.TrimSpace(d.Attributes.CustomName)] {
			continue
		}
		selected = append(selected, d)
	}

	if len(selected) == 0 {
		w.Text = "no lights"
		w.Class = "empty"
		_ = w.Print()
		return
	}

	onCount := 0
	var dots strings.Builder
	for _, d := range selected {
		color := d.Attributes.Hex()
		if color == "" {
			color = offColor
		} else {
			onCount++
		}
		fmt.Fprintf(&dots, `<span foreground="%s">●</span>`, color)
	}
	w.Text = fmt.Sprintf(`%s %d/%d`, dots.String(), onCount, len(selected))

	var tip strings.Builder
	for i, d := range selected {
		color := d.Attributes.Hex()
		if color == "" {
			color = offColor
		}
		if i > 0 {
			tip.WriteByte('\n')
		}
		a := d.Attributes
		name := escape(strings.TrimSpace(a.CustomName))
		if name == "" {
			name = "(unnamed)"
		}
		if !a.IsOn {
			fmt.Fprintf(&tip, `<span foreground="%s">●</span> %s: off`, color, name)
			continue
		}
		fmt.Fprintf(&tip, `<span foreground="%s">●</span> %s: %s (%d%%)`,
			color, name, a.Label(), a.LightLevel)
	}
	w.ToolTip = tip.String()

	switch {
	case onCount == 0:
		w.Class = "all-off"
	case onCount == len(selected):
		w.Class = "all-on"
	default:
		w.Class = "some-on"
	}
	if err := w.Print(); err != nil {
		log.Printf("print: %v", err)
	}
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("empty")
	}
	return tok, nil
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}
