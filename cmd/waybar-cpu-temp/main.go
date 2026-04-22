// waybar-cpu-temp emits JSON for a waybar custom module showing CPU
// temperature, plus a rich tooltip listing every CPU-related hwmon sensor.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/hwmon"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

// Known CPU-die hwmon names, tried in order. First match wins as the
// primary sensor (the one shown in the text field).
var cpuDieNames = []string{"k10temp", "zenpower", "coretemp"}

// Extra motherboard/EC sensors to include in the tooltip if present.
// Order matters — this is the display order. We prefer vendor EC chips
// (asusec) over generic super-I/O (nct6*, it87) because the EC uses
// human-friendly labels ("CPU", "VRM") where the super-I/O exposes
// many unlabeled AUXTIN pins, most of which are disconnected.
var extraSensorNames = []string{"asusec", "nct6799", "nct6798", "it87"}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "CPU"
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "model name") {
			if _, v, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return "CPU"
}

func main() {
	interval := flag.Int("interval", 2, "Polling interval in seconds")
	warnAt := flag.Float64("warn", 70, "Class=warm above this °C")
	critAt := flag.Float64("crit", 85, "Class=critical above this °C")
	flag.Parse()

	model := cpuModel()

	// Discover the primary CPU die sensor.
	var primary string
	for _, name := range cpuDieNames {
		if dirs, _ := hwmon.FindByName(name); len(dirs) > 0 {
			primary = dirs[0]
			break
		}
	}
	if primary == "" {
		log.Fatalf("no CPU temperature sensor found (looked for: %v)", cpuDieNames)
	}

	// Gather extras that exist on this host.
	var extras []string
	for _, name := range extraSensorNames {
		if dirs, _ := hwmon.FindByName(name); len(dirs) > 0 {
			extras = append(extras, dirs...)
		}
	}

	ticker := time.NewTicker(time.Duration(*interval) * time.Second)
	defer ticker.Stop()

	for ; ; <-ticker.C {
		w := waybar.New()

		// temp1_input is conventionally the primary reading
		// (Tctl on k10temp, Package id 0 on coretemp, Tdie on zenpower).
		t, err := hwmon.ReadTemp(primary, "temp1_input")
		if err != nil {
			log.Printf("primary sensor read failed: %v", err)
			continue
		}
		w.Text = fmt.Sprintf("%.0f", t)

		switch {
		case t >= *critAt:
			w.Class = "critical"
		case t >= *warnAt:
			w.Class = "warm"
		default:
			w.Class = "normal"
		}

		w.ToolTip = buildTooltip(model, primary, extras)

		if err := w.Print(); err != nil {
			log.Printf("print: %v", err)
		}
	}
}

func buildTooltip(model, primary string, extras []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>", model)
	for _, h := range append([]string{primary}, extras...) {
		readings, err := hwmon.ReadAll(h)
		if err != nil {
			continue
		}
		readings = filterPlausible(readings)
		if len(readings) == 0 {
			continue
		}
		name, _ := hwmon.Name(h)
		fmt.Fprintf(&b, "\n\n<b>%s</b>", name)
		for _, r := range readings {
			fmt.Fprintf(&b, "\n%s: %.0f°C", r.Label, r.Celsius)
		}
	}
	return b.String()
}

// filterPlausible drops readings that look like disconnected sensors:
// super-I/O chips expose many AUXTIN/PCH pins that return 0°C, -61°C
// (sign-extended 0xFF), or similar nonsense when no probe is attached.
func filterPlausible(in []hwmon.Reading) []hwmon.Reading {
	out := in[:0]
	for _, r := range in {
		if r.Celsius > 0 && r.Celsius < 120 {
			out = append(out, r)
		}
	}
	return out
}
