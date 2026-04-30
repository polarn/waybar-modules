// waybar-batteries: a generic battery pill for waybar that aggregates every
// reachable battery-equipped peripheral (Bluetooth devices via UPower,
// SteelSeries USB-dongle headsets via headsetcontrol). New sources can be
// added by implementing a poll function that returns []Device.
//
// Pill content rules:
//   - 0 devices reachable -> empty text (waybar collapses the module)
//   - 1..max-inline devices -> all shown inline, sorted by percent ascending
//   - more than max-inline -> just the lowest one + "+N" indicator
//
// Tooltip always lists every device with name, percent, and charging state.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	iconHeadset    = "󰋎" // U+F02CE nf-md-headset
	iconHeadphones = "󰋋" // U+F02CB nf-md-headphones
	iconMouse      = "󰍽" // U+F037D nf-md-mouse
	iconKeyboard   = "󰌌" // U+F030C nf-md-keyboard
	iconGamepad    = "󰊗" // U+F0297 nf-md-gamepad
	iconPhone      = "󰏰" // U+F03F0 nf-md-cellphone
	iconBattery    = "󰁹" // U+F0079 nf-md-battery
)

// Device is a single battery-equipped peripheral, normalised across sources.
type Device struct {
	Source  string // "upower", "headsetcontrol", ...
	Name    string // "Bose QC35 II"
	Kind    string // "headset", "mouse", ...
	Icon    string // glyph chosen from kind
	Percent int    // 0..100
	State   string // "charging" | "discharging" | "full" | "unknown"
}

func iconForKind(kind string) string {
	switch strings.ToLower(kind) {
	case "headset":
		return iconHeadset
	case "headphones", "headphone":
		return iconHeadphones
	case "mouse":
		return iconMouse
	case "keyboard":
		return iconKeyboard
	case "gaming-input", "gamepad":
		return iconGamepad
	case "phone":
		return iconPhone
	default:
		return iconBattery
	}
}

// pollUPower lists every UPower battery device and returns them as Devices.
// Skips DisplayDevice (an aggregate) and AC-power entries (no battery).
func pollUPower() ([]Device, error) {
	out, err := exec.Command("upower", "-e").Output()
	if err != nil {
		return nil, err
	}
	var devices []Device
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p == "" || strings.HasSuffix(p, "/DisplayDevice") || strings.Contains(p, "/line_power_") {
			continue
		}
		info, err := exec.Command("upower", "-i", p).Output()
		if err != nil {
			continue
		}
		d := parseUPowerInfo(string(info))
		if d.Percent == 0 && d.State == "" {
			continue
		}
		d.Source = "upower"
		if d.Icon == "" {
			d.Icon = iconForKind(d.Kind)
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// UPower's text output structure (for a peripheral):
//
//	  native-path:          /org/bluez/...     <-- 2-space indent metadata
//	  model:                Bose QC35 II
//	  ...
//	  headphones                                <-- 2-space indent, NO colon: device kind
//	    percentage:          60%                <-- 4-space indent: section attributes
//	    state:               discharging
//
// So the device "kind" is a section header word, not a `kind:` field.
func parseUPowerInfo(raw string) Device {
	var d Device
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Section header: indented exactly 2 spaces, single word, no colon.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") &&
			!strings.Contains(trimmed, ":") && d.Kind == "" {
			d.Kind = trimmed
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "model:"):
			d.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "model:"))
		case strings.HasPrefix(trimmed, "percentage:"):
			pct := strings.TrimSpace(strings.TrimPrefix(trimmed, "percentage:"))
			pct = strings.TrimSuffix(pct, "%")
			n, _ := strconv.Atoi(strings.TrimSpace(pct))
			d.Percent = n
		case strings.HasPrefix(trimmed, "state:"):
			d.State = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
		}
	}
	return d
}

// pollHeadsetControl returns devices reported by headsetcontrol -o JSON.
// Currently this is the only way to read the Arctis 7's battery (the USB
// dongle does not register with UPower).
func pollHeadsetControl() ([]Device, error) {
	out, err := exec.Command("headsetcontrol", "-o", "JSON").Output()
	if err != nil {
		return nil, err
	}
	var raw struct {
		Devices []struct {
			Device  string `json:"device"`
			Battery struct {
				Status string `json:"status"`
				Level  int    `json:"level"`
			} `json:"battery"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	var devices []Device
	for _, hd := range raw.Devices {
		if hd.Battery.Status == "BATTERY_UNAVAILABLE" {
			continue
		}
		// headsetcontrol's USB dongle keeps reporting BATTERY_AVAILABLE even
		// when the wireless headset itself is powered off — battery level is
		// just 0 in that case. Treat it as absent rather than flagging the
		// pill critical for a device that isn't actually being used.
		if hd.Battery.Level == 0 {
			continue
		}
		state := "discharging"
		if hd.Battery.Status == "BATTERY_CHARGING" {
			state = "charging"
		}
		devices = append(devices, Device{
			Source:  "headsetcontrol",
			Name:    hd.Device,
			Kind:    "headset",
			Icon:    iconHeadset,
			Percent: hd.Battery.Level,
			State:   state,
		})
	}
	return devices, nil
}

type WaybarOut struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip,omitempty"`
	Class   string `json:"class,omitempty"`
}

// classify picks the worst-state CSS class across all devices for colouring.
//   critical (<20%) > low (<40%) > charging > ok
func classify(devs []Device) string {
	hasCharging := false
	hasLow := false
	for _, d := range devs {
		if d.Percent < 20 {
			return "critical"
		}
		if d.Percent < 40 {
			hasLow = true
		}
		if d.State == "charging" {
			hasCharging = true
		}
	}
	switch {
	case hasLow:
		return "low"
	case hasCharging:
		return "charging"
	}
	return "ok"
}

func render(devs []Device, maxInline int) WaybarOut {
	if len(devs) == 0 {
		return WaybarOut{Text: ""}
	}
	sort.SliceStable(devs, func(i, j int) bool {
		return devs[i].Percent < devs[j].Percent
	})

	var pillParts []string
	if len(devs) <= maxInline {
		for _, d := range devs {
			pillParts = append(pillParts, fmt.Sprintf("%s %d%%", d.Icon, d.Percent))
		}
	} else {
		// Lowest device shown in detail; rest summarised as "+N".
		d := devs[0]
		pillParts = append(pillParts, fmt.Sprintf("%s %d%%", d.Icon, d.Percent))
		pillParts = append(pillParts, fmt.Sprintf("+%d", len(devs)-1))
	}

	var tooltipLines []string
	for _, d := range devs {
		line := fmt.Sprintf("%s %s: %d%%", d.Icon, d.Name, d.Percent)
		if d.State == "charging" {
			line += " (charging)"
		}
		tooltipLines = append(tooltipLines, line)
	}

	return WaybarOut{
		Text:    strings.Join(pillParts, " "),
		Tooltip: strings.Join(tooltipLines, "\n"),
		Class:   classify(devs),
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func emit(sources []string, maxInline int) {
	var devs []Device
	for _, src := range sources {
		var got []Device
		var err error
		switch src {
		case "upower":
			got, err = pollUPower()
		case "headsetcontrol":
			got, err = pollHeadsetControl()
		default:
			fmt.Fprintln(os.Stderr, "unknown --source:", src)
			continue
		}
		if err != nil {
			// Soft-fail: missing tool, no permission, etc. Log to stderr so it
			// shows in waybar's journal but keep emitting valid JSON.
			fmt.Fprintf(os.Stderr, "%s: %v\n", src, err)
			continue
		}
		devs = append(devs, got...)
	}
	out, _ := json.Marshal(render(devs, maxInline))
	fmt.Println(string(out))
}

func main() {
	var sources stringList
	flag.Var(&sources, "source", "Battery source (repeatable). Built-in: upower, headsetcontrol. Defaults to both.")
	interval := flag.Int("interval", 60, "Poll interval in seconds")
	maxInline := flag.Int("max-inline", 2, "Devices shown inline before collapsing to lowest+\"+N\"")
	flag.Parse()

	if len(sources) == 0 {
		sources = []string{"upower", "headsetcontrol"}
	}

	for {
		emit(sources, *maxInline)
		time.Sleep(time.Duration(*interval) * time.Second)
	}
}
