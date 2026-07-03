// waybar-gpu-temp emits JSON for a waybar custom module showing GPU
// temperature, plus a tooltip listing every GPU hwmon sensor. When
// multiple GPUs are present (e.g. discrete + iGPU), the one with the
// most sensors wins — that's the discrete card.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/hwmon"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

// llamaSwapRunning is llama-swap's GET /running response.
type llamaSwapRunning struct {
	Running []struct {
		Model string `json:"model"`
		State string `json:"state"`
	} `json:"running"`
}

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
	llamaSwap := flag.String("llama-swap", "http://localhost:9292",
		"llama-swap base URL; loaded models are listed in the tooltip. Empty string disables.")
	flag.Parse()

	dir := pickGPU()
	if dir == "" {
		log.Fatalf("no GPU temperature sensor found (looked for: %v)", gpuNames)
	}
	displayModel := *model
	if displayModel == "" {
		displayModel = gpuModel(dir)
	}

	var llamaClient *http.Client
	if *llamaSwap != "" {
		llamaClient = &http.Client{Timeout: time.Second}
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

		tempClass := "normal"
		switch {
		case primary.Celsius >= *critAt:
			tempClass = "critical"
		case primary.Celsius >= *warnAt:
			tempClass = "warm"
		}
		classes := []string{tempClass}

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

		// Utilization + VRAM go in the tooltip; VRAM additionally drives the
		// background "gauge" via a vram-NN class (5% buckets). Both are skipped
		// silently on GPUs that don't expose the amdgpu sysfs files.
		var stats []string
		if busy, err := hwmon.GPUBusy(dir); err == nil {
			stats = append(stats, fmt.Sprintf("GPU load: %d%%", busy))
		}
		if busy, err := hwmon.MemBusy(dir); err == nil {
			stats = append(stats, fmt.Sprintf("Mem load: %d%%", busy))
		}
		if used, total, err := hwmon.VRAM(dir); err == nil && total > 0 {
			pct := float64(used) / float64(total) * 100
			bucket := int(math.Round(pct/5)) * 5
			if bucket < 0 {
				bucket = 0
			} else if bucket > 100 {
				bucket = 100
			}
			classes = append(classes, fmt.Sprintf("vram-%d", bucket))
			const gib = 1024 * 1024 * 1024
			stats = append(stats, fmt.Sprintf("VRAM: %.1f / %.1f GiB (%.0f%%)",
				float64(used)/gib, float64(total)/gib, pct))
		}
		if len(stats) > 0 {
			fmt.Fprintf(&b, "\n\n%s", strings.Join(stats, "\n"))
		}

		// Loaded LLMs via llama-swap's /running endpoint. Skipped silently
		// (no logging — this runs every tick) when llama-swap isn't running
		// or nothing is loaded, mirroring the sysfs skips above.
		if llamaClient != nil {
			url := strings.TrimRight(*llamaSwap, "/") + "/running"
			if resp, err := fetchJSON[llamaSwapRunning](llamaClient, url); err == nil && len(resp.Running) > 0 {
				b.WriteString("\n\n<b>LLMs</b>")
				for _, m := range resp.Running {
					b.WriteString("\n" + escapePango(m.Model))
					if m.State != "" && m.State != "ready" {
						fmt.Fprintf(&b, " <small>(%s)</small>", escapePango(m.State))
					}
				}
			}
		}

		w.Class = classes
		w.ToolTip = b.String()

		if err := w.Print(); err != nil {
			log.Printf("print: %v", err)
		}
	}
}

func fetchJSON[T any](client *http.Client, url string) (*T, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &result, nil
}

// escapePango escapes the five XML entities so model names from llama-swap's
// config can't break (or inject) waybar's Pango-markup tooltip.
func escapePango(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&#39;",
		"\"", "&#34;",
	).Replace(s)
}
