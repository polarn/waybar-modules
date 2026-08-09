package main

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
)

// Prometheus text exposition, hand-rolled for the same reason as the MQTT
// framing in pkg/bambu: the format is small and stable, while
// client_golang would pull protobuf and a half-dozen transitive modules
// into a repo whose go.sum is eight lines long (AGENTS.md: "No external
// dependencies beyond what's in go.mod").

type family struct {
	help    string
	samples []string
}

type exposition struct {
	order []string
	fams  map[string]*family
}

func newExposition() *exposition {
	return &exposition{fams: make(map[string]*family)}
}

// gauge records one sample. labels are alternating key/value pairs.
// Samples of the same metric are grouped, so HELP/TYPE is emitted once
// and each family stays contiguous as the format requires.
func (e *exposition) gauge(name, help string, v float64, labels ...string) {
	f, ok := e.fams[name]
	if !ok {
		f = &family{help: help}
		e.fams[name] = f
		e.order = append(e.order, name)
	}
	f.samples = append(f.samples, name+formatLabels(labels)+" "+formatValue(v))
}

func (e *exposition) String() string {
	var b strings.Builder
	for _, name := range e.order {
		f := e.fams[name]
		fmt.Fprintf(&b, "# HELP %s %s\n", name, escapeHelp(f.help))
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		for _, s := range f.samples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func formatLabels(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(kv[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }

var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func escapeHelp(s string) string { return helpEscaper.Replace(s) }

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// printStates is the enum surfaced as a 0/1 set, so a dashboard can graph
// state transitions without string handling.
var printStates = []string{"IDLE", "PREPARE", "SLICING", "RUNNING", "PAUSE", "FINISH", "FAILED"}

// collect renders the current view as Prometheus text.
//
// Every metric sourced from the printer is emitted only when the printer
// actually reported it. Absent is not zero — publishing a default would
// reproduce the bug this whole exporter grew out of, where a missing
// chamber field graphed as a confident 0 °C.
func collect(printer string, st *bambu.State, sess *bambu.Session, authOK bool) string {
	e := newExposition()
	pn := []string{"printer_name", printer}

	e.gauge("bambulab_cloud_auth_ok",
		"1 while the cloud accepts the cached token, 0 once it has been rejected (needs an interactive bambu-ctl login).",
		boolValue(authOK), pn...)

	// Token renewal is interactive and lapses roughly quarterly, so make it
	// visible rather than letting it show up as a gap in the graphs.
	if sess.Saved > 0 {
		e.gauge("bambulab_cloud_token_age_seconds",
			"Age of the cached cloud token. Tokens last about three months and cannot be renewed unattended.",
			float64(time.Now().Unix()-sess.Saved), pn...)
	}
	// Only present for JWT tokens; the emailed-code login flow mints opaque
	// ones, and then expiry simply is not knowable in advance.
	if exp, err := sess.TokenExpiry(); err == nil {
		e.gauge("bambulab_cloud_token_expiry_timestamp_seconds",
			"When the cloud token expires, from its JWT exp claim. Absent for opaque tokens.",
			float64(exp.Unix()), pn...)
	}

	age, haveState := st.Age()
	if !haveState {
		e.gauge("bambulab_printer_up", "1 when the printer is reporting to the cloud.", 0, pn...)
		return e.String()
	}
	e.gauge("bambulab_report_age_seconds",
		"Seconds since the last MQTT report. The honest liveness signal: the process can be healthy while its subscription has gone deaf.",
		age.Seconds(), pn...)

	rep, ok := st.Report()
	if !ok {
		e.gauge("bambulab_printer_up", "1 when the printer is reporting to the cloud.", 0, pn...)
		return e.String()
	}

	// Reports land every 1-2 s while the printer is on, so this much
	// silence means it is powered down rather than merely idle.
	up := 0.0
	if age < 5*time.Minute {
		up = 1
	}
	e.gauge("bambulab_printer_up", "1 when the printer is reporting to the cloud.", up, pn...)

	num := func(name, help string, n *bambu.Num, labels ...string) {
		if n == nil {
			return
		}
		e.gauge(name, help, float64(*n), labels...)
	}

	num("bambulab_nozzle_temperature_celsius", "Current nozzle temperature.", rep.Print.NozzleTemper, pn...)
	num("bambulab_nozzle_target_temperature_celsius", "Target nozzle temperature.", rep.Print.NozzleTargetTemper, pn...)
	num("bambulab_bed_temperature_celsius", "Current heatbed temperature.", rep.Print.BedTemper, pn...)
	num("bambulab_bed_target_temperature_celsius", "Target heatbed temperature.", rep.Print.BedTargetTemper, pn...)
	if chamber, ok := rep.ChamberTemp(); ok {
		e.gauge("bambulab_chamber_temperature_celsius", "Current chamber temperature.", float64(chamber), pn...)
	}

	num("bambulab_print_progress_percent", "Print progress.", rep.Print.McPercent, pn...)
	num("bambulab_print_layer_current", "Layer being printed.", rep.Print.LayerNum, pn...)
	// Not _total: Prometheus reserves that suffix for counters, and this is
	// a gauge. (Grafana dashboard 25033 uses the _total spelling, so its
	// layer panel needs the name changed if you import it.)
	num("bambulab_print_layers", "Total layers in the job.", rep.Print.TotalLayerNum, pn...)
	if t := rep.Print.McRemainingTime; t != nil {
		e.gauge("bambulab_print_remaining_seconds", "Estimated time left in the print.",
			float64(*t)*60, pn...) // the printer reports minutes
	}
	num("bambulab_print_stage", "Current stage code; see StageName for the printer's own wording.", rep.Print.StgCur, pn...)
	num("bambulab_print_error_code", "Printer error code, 0 when healthy.", rep.Print.PrintError, pn...)
	num("bambulab_speed_level", "Speed profile: 1 silent, 2 standard, 3 sport, 4 ludicrous.", rep.Print.SpdLvl, pn...)
	num("bambulab_speed_magnitude_percent", "Speed as a percentage of standard.", rep.Print.SpdMag, pn...)
	// Nozzle diameter is configuration rather than telemetry, and naming it
	// in Prometheus base units would mean graphing 0.0004 m. It reads
	// better as a label on an info metric.
	if d := rep.Print.NozzleDiameter; d != nil {
		e.gauge("bambulab_nozzle_info", "Installed nozzle, as labels.", 1,
			"printer_name", printer, "diameter_mm", formatValue(float64(*d)))
	}

	state := strings.ToUpper(rep.Print.GcodeState)
	if state == "" {
		state = "IDLE"
	}
	for _, s := range printStates {
		e.gauge("bambulab_print_state", "1 for the printer's current gcode state, 0 for the others.",
			boolValue(s == state), "printer_name", printer, "state", s)
	}

	// The job name is an info metric so a new print doesn't fork every
	// numeric series. Entities are decoded because MakerWorld titles arrive
	// HTML-escaped ("Snake &apos;Long&apos;").
	if job := html.UnescapeString(rep.Print.SubtaskName); job != "" {
		e.gauge("bambulab_print_job_info", "Current job name, as a label.", 1,
			"printer_name", printer, "job", job)
	}

	fan := func(name string, n *bambu.Num) {
		if n == nil {
			return
		}
		e.gauge("bambulab_fan_speed_percent", "Fan speed, converted from the printer's 0-15 gear value.",
			bambu.FanPercent(n), "printer_name", printer, "fan", name)
	}
	fan("cooling", rep.Print.CoolingFanSpeed)
	fan("aux", rep.Print.BigFan1Speed)     // big_fan1
	fan("chamber", rep.Print.BigFan2Speed) // big_fan2
	fan("heatbreak", rep.Print.HeatbreakFanSpeed)

	if dbm, ok := rep.WifiSignalDBm(); ok {
		e.gauge("bambulab_wifi_signal_dbm", "Wi-Fi signal strength.", float64(dbm), pn...)
	}

	for i, unit := range rep.Print.AMS.AMS {
		ams := strconv.Itoa(i)
		al := []string{"printer_name", printer, "ams", ams}
		num("bambulab_ams_humidity_percent", "AMS relative humidity. Only AMS 2 Pro and newer report a real percentage.", unit.HumidityRaw, al...)
		num("bambulab_ams_humidity_index", "AMS humidity as the coarse 1-5 index every AMS model reports.", unit.Humidity, al...)
		num("bambulab_ams_temperature_celsius", "AMS internal temperature.", unit.Temp, al...)
		if d := unit.DryTime; d != nil {
			e.gauge("bambulab_ams_drying_remaining_seconds", "Time left in an active drying run, 0 when not drying.",
				float64(*d)*60, al...) // reported in minutes
		}
		for j, tray := range unit.Tray {
			tl := []string{"printer_name", printer, "ams", ams, "tray", strconv.Itoa(j)}
			// remain is -1 when the spool has no usable estimate; emitting
			// that as a percentage would graph as a real reading.
			if tray.Remain != nil && tray.Remain.Int() >= 0 {
				e.gauge("bambulab_ams_tray_remaining_percent", "Filament remaining on the spool.",
					float64(*tray.Remain), tl...)
			}
			name := tray.TraySubBrands
			if name == "" {
				name = tray.TrayType
			}
			if name != "" {
				e.gauge("bambulab_ams_tray_info", "Loaded filament per slot, as labels.", 1,
					"printer_name", printer, "ams", ams, "tray", strconv.Itoa(j),
					"type", tray.TrayType, "name", name, "color", tray.TrayColor)
			}
		}
	}

	return e.String()
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
