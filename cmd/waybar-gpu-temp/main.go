// waybar-gpu-temp emits JSON for a waybar custom module showing GPU
// temperature, plus a tooltip listing every GPU hwmon sensor. When
// multiple GPUs are present (e.g. discrete + iGPU), the one with the
// most sensors wins — that's the discrete card.
package main

import (
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/hwmon"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

// Known GPU hwmon names. Picked in listed order on first match.
var gpuNames = []string{"amdgpu", "nouveau", "i915", "xe"}

// gpuModel resolves a human-readable GPU name via lspci using the PCI
// address owning the hwmon directory. lspci only knows the silicon family
// from pci.ids — for the exact card variant (e.g. RX 9070 vs 9070 XT)
// pass -model at startup. Falls back to the hwmon name if lspci isn't
// available or fails.
func gpuModel(hwmonDir string) string {
	fallback, _ := hwmon.Name(hwmonDir)
	if fallback == "" {
		fallback = "GPU"
	}
	addr := hwmon.PCIAddress(hwmonDir)
	if addr == "" {
		return fallback
	}
	out, err := exec.Command("lspci", "-s", addr).Output()
	if err != nil {
		return fallback
	}
	return shortenLspci(strings.TrimSpace(string(out)), fallback)
}

// shortenLspci trims verbose vendor fluff from an lspci line. Example input:
//
//	03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 48 [Radeon RX 9070/9070 XT/9070 GRE] (rev c0)
//
// Output: "Navi 48 (Radeon RX 9070/9070 XT/9070 GRE)"
func shortenLspci(line, fallback string) string {
	s := line
	if _, after, ok := strings.Cut(s, ": "); ok {
		s = after
	}
	if idx := strings.Index(s, " (rev "); idx >= 0 {
		s = s[:idx]
	}
	// Drop "Advanced Micro Devices, Inc. [AMD/ATI] " / "Intel Corporation " /
	// "NVIDIA Corporation " prefixes (legalese duplicated in the "[...]" group).
	for _, prefix := range []string{
		"Advanced Micro Devices, Inc. [AMD/ATI] ",
		"Advanced Micro Devices, Inc. ",
		"NVIDIA Corporation ",
		"Intel Corporation ",
	} {
		s = strings.TrimPrefix(s, prefix)
	}
	// "[AMD/ATI]" can also appear mid-string with no inner brackets;
	// similarly "[AMD]". Drop when present.
	for _, tag := range []string{"[AMD/ATI] ", "[AMD] "} {
		s = strings.ReplaceAll(s, tag, "")
	}
	// Normalise the model bracket to parens so it doesn't look like a
	// markup tag.
	s = strings.ReplaceAll(s, "[", "(")
	s = strings.ReplaceAll(s, "]", ")")
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// pickGPU returns the hwmon directory of the "main" GPU. If multiple
// amdgpu/etc. entries exist, prefer the one with the most temperature
// sensors — the discrete card has edge+junction+mem, integrated graphics
// typically have only edge.
func pickGPU() string {
	for _, name := range gpuNames {
		dirs, _ := hwmon.FindByName(name)
		if len(dirs) == 0 {
			continue
		}
		best := dirs[0]
		bestCount := hwmon.CountTempInputs(best)
		for _, d := range dirs[1:] {
			if n := hwmon.CountTempInputs(d); n > bestCount {
				best, bestCount = d, n
			}
		}
		return best
	}
	return ""
}

func main() {
	interval := flag.Int("interval", 2, "Polling interval in seconds")
	sensor := flag.String("sensor", "edge", "Primary sensor label to show (e.g. edge, junction, mem)")
	model := flag.String("model", "", "Override the GPU model name shown in the tooltip (auto-discovered via lspci otherwise)")
	warnAt := flag.Float64("warn", 75, "Class=warm above this °C")
	critAt := flag.Float64("crit", 90, "Class=critical above this °C")
	flag.Parse()

	dir := pickGPU()
	if dir == "" {
		log.Fatalf("no GPU temperature sensor found (looked for: %v)", gpuNames)
	}
	displayModel := *model
	if displayModel == "" {
		displayModel = gpuModel(dir)
	}

	ticker := time.NewTicker(time.Duration(*interval) * time.Second)
	defer ticker.Stop()

	for ; ; <-ticker.C {
		readings, err := hwmon.ReadAll(dir)
		if err != nil || len(readings) == 0 {
			log.Printf("GPU sensor read failed: %v", err)
			continue
		}

		// Find the primary reading by label; fall back to the first.
		primary := readings[0]
		for _, r := range readings {
			if strings.EqualFold(r.Label, *sensor) {
				primary = r
				break
			}
		}

		w := waybar.New()
		w.Text = fmt.Sprintf("%.0f", primary.Celsius)

		switch {
		case primary.Celsius >= *critAt:
			w.Class = "critical"
		case primary.Celsius >= *warnAt:
			w.Class = "warm"
		default:
			w.Class = "normal"
		}

		var b strings.Builder
		fmt.Fprintf(&b, "<b>%s</b>", displayModel)
		name, _ := hwmon.Name(dir)
		fmt.Fprintf(&b, "\n\n<b>%s</b>", name)
		for _, r := range readings {
			if r.Celsius <= 0 || r.Celsius > 120 {
				continue
			}
			fmt.Fprintf(&b, "\n%s: %.0f°C", r.Label, r.Celsius)
		}
		w.ToolTip = b.String()

		if err := w.Print(); err != nil {
			log.Printf("print: %v", err)
		}
	}
}
