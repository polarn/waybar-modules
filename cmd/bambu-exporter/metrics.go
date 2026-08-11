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
	typ     string // "gauge" or "counter"
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
	e.sample("gauge", name, help, v, labels...)
}

// counter records one monotonic sample. Only the _total families use it;
// everything sourced from a report snapshot is a gauge.
func (e *exposition) counter(name, help string, v float64, labels ...string) {
	e.sample("counter", name, help, v, labels...)
}

func (e *exposition) sample(typ, name, help string, v float64, labels ...string) {
	f, ok := e.fams[name]
	if !ok {
		f = &family{typ: typ, help: help}
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
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, f.typ)
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
func collect(printer string, st *bambu.State, sess *bambu.Session, authOK bool, tasks *taskCache, jobs *jobWatch) string {
	e := newExposition()
	pn := []string{"printer_name", printer}

	// Counters first, and outside the report guard below: they are the one
	// thing that stays meaningful when the printer is off, and losing the
	// history to a powered-down printer would defeat the point.
	if jobs != nil {
		jobs.collect(e, printer)
	}

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
	num("bambulab_mc_print_error_code", "Motion-controller error code, 0 when healthy.", rep.Print.McPrintErrorCode, pn...)
	num("bambulab_print_fail_reason", "Failure reason code for the last print, 0 when none.", rep.Print.FailReason, pn...)
	num("bambulab_speed_level", "Speed profile: 1 silent, 2 standard, 3 sport, 4 ludicrous.", rep.Print.SpdLvl, pn...)
	num("bambulab_speed_magnitude_percent", "Speed as a percentage of standard.", rep.Print.SpdMag, pn...)
	// The number is already in bambulab_print_stage; this carries the
	// printer's own wording so a state timeline needs no lookup table in
	// the dashboard. Idle stages are named too ("idle", "printing") rather
	// than omitted, so the series stays continuous.
	if s := rep.Print.StgCur; s != nil {
		e.gauge("bambulab_print_stage_info", "The printer's own wording for the current stage, as a label.", 1,
			"printer_name", printer, "stage", bambu.StageName(s.Int()))
	}
	if p := bambu.SpeedProfile(rep.Print.SpdLvl); p != "" {
		e.gauge("bambulab_speed_profile_info", "Name of the speed profile in bambulab_speed_level, as a label.", 1,
			"printer_name", printer, "profile", p)
	}
	// Nozzle diameter is configuration rather than telemetry, and naming it
	// in Prometheus base units would mean graphing 0.0004 m. It reads
	// better as a label on an info metric.
	if d := rep.Print.NozzleDiameter; d != nil {
		e.gauge("bambulab_nozzle_info", "Installed nozzle, as labels.", 1,
			"printer_name", printer, "diameter_mm", formatValue(float64(*d)),
			"type", rep.Print.NozzleType)
	}
	// Scale is undocumented — 0 on a new nozzle is all this printer has
	// shown so far — hence no unit in the name.
	if n := rep.Print.Device.Nozzle.Info; len(n) > 0 {
		num("bambulab_nozzle_wear", "Nozzle wear as the printer reports it; the scale is undocumented.", n[0].Wear, pn...)
	}

	// Firmware, so an upgrade shows up as an annotation-worthy change in
	// the graphs. `visible` is not filtered on: the submodules it hides
	// (motion controller, toolhead) are the ones worth knowing the version
	// of when something misbehaves.
	if m, ok := rep.OTAModule(); ok {
		e.gauge("bambulab_printer_info", "Printer model and firmware version, as labels.", 1,
			"printer_name", printer, "model", m.ProductName, "firmware", m.SWVer)
	}
	seenModule := make(map[string]bool)
	for _, m := range rep.Info.Module {
		if m.Name == "" || seenModule[m.Name] {
			continue
		}
		seenModule[m.Name] = true
		e.gauge("bambulab_module_info", "Hardware and firmware revision per reported module, as labels.", 1,
			"printer_name", printer, "module", m.Name, "product_name", m.ProductName,
			"hw_ver", m.HWVer, "sw_ver", m.SWVer)
	}
	num("bambulab_firmware_new_version_state", "upgrade_state.new_version_state as reported. Each value's meaning is undocumented, so this is the raw number and not a boolean (this printer reports 2 while up to date).",
		rep.Print.UpgradeState.NewVersionState, pn...)

	// One series per latched fault. Deduplicated because two identical
	// entries would be two samples with the same label set, which is a
	// scrape error rather than a merely odd graph.
	seenHMS := make(map[string]bool)
	for _, h := range rep.Print.HMS {
		code := bambu.HMSCode(h.Attr, h.Code)
		if code == "" || seenHMS[code] {
			continue
		}
		seenHMS[code] = true
		e.gauge("bambulab_hms_active", "One per entry in the printer's HMS list. Each carries an event timestamp, and whether the list means \"active now\" or \"seen since boot\" is not established — see pkg/bambu/hms.go. severity is fatal, serious, common or unknown; note serious is the modal bucket, not an escalation.", 1,
			"printer_name", printer, "code", code, "severity", bambu.HMSSeverity(h.Code))
	}

	seenLight := make(map[string]bool)
	for _, l := range rep.Print.LightsReport {
		if l.Node == "" || seenLight[l.Node] {
			continue
		}
		seenLight[l.Node] = true
		// mode is not a boolean — work_light reports "flashing" — so this is
		// an info metric rather than a 0/1 gauge.
		e.gauge("bambulab_light_mode_info", "Light state as a label; mode is on, off or flashing.", 1,
			"printer_name", printer, "light", l.Node, "mode", l.Mode)
	}

	// Timelapse recording stops silently when the internal storage fills.
	if f := rep.Print.IPCam.TLInternalFreeKB; f != nil {
		e.gauge("bambulab_timelapse_storage_free_bytes", "Free space on the printer's internal timelapse storage.",
			float64(*f)*1024, pn...) // reported in kibibytes
	}
	if t := rep.Print.IPCam.TLInternalTotalKB; t != nil {
		e.gauge("bambulab_timelapse_storage_total_bytes", "Size of the printer's internal timelapse storage.",
			float64(*t)*1024, pn...)
	}

	state := strings.ToUpper(rep.Print.GcodeState)
	if state == "" {
		state = "IDLE"
	}
	for _, s := range printStates {
		e.gauge("bambulab_print_state", "1 for the printer's current gcode state, 0 for the others.",
			boolValue(s == state), "printer_name", printer, "state", s)
	}
	// printStates is a fixed list, so a firmware reporting anything outside
	// it would read as all-zero and leave the real state invisible. This
	// carries whatever the printer actually said.
	e.gauge("bambulab_print_state_info", "The raw gcode_state as a label, including values outside the set bambulab_print_state enumerates.", 1,
		"printer_name", printer, "state", state)

	// The job name is an info metric so a new print doesn't fork every
	// numeric series. Entities are decoded because MakerWorld titles arrive
	// HTML-escaped ("Snake &apos;Long&apos;").
	//
	// The label is `title`, not `job`: kube-prometheus-stack's scrape
	// attaches its own job="bambu-exporter", so Prometheus renamed ours to
	// exported_job and every dashboard asking for {{job}} rendered the
	// scrape job instead of the print. Do not name a label `job` here.
	if job := html.UnescapeString(rep.Print.SubtaskName); job != "" {
		e.gauge("bambulab_print_job_info", "Current job name and provenance, as labels.", 1,
			"printer_name", printer, "title", job,
			"print_type", rep.Print.PrintType,
			"model_id", rep.Print.ModelID, "design_id", rep.Print.DesignID)
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

	// Which slot is feeding, resolved once — tray_now is AMS-global, so it
	// has to be decomposed rather than compared per unit.
	_, activeAMS, activeSlot, haveActive := rep.ActiveFilament()
	if n := rep.Print.AMS.TrayNow; n != nil && haveActive {
		e.gauge("bambulab_ams_active_tray", "Global index of the slot feeding the hotend, four per AMS unit. Absent when nothing is loaded; see bambulab_ams_tray_active for the per-slot form.",
			float64(n.Int()), pn...)
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
		for j := range unit.Tray {
			tray := &unit.Tray[j]
			tl := []string{"printer_name", printer, "ams", ams, "tray", strconv.Itoa(j)}

			// An empty slot used to emit nothing at all, which made it
			// indistinguishable from an AMS that had been unplugged. ok is
			// false when the printer reported no bitfield, and then this
			// stays absent rather than claiming the slot is empty.
			if present, ok := rep.TrayPresent(i, j); ok {
				e.gauge("bambulab_ams_tray_present", "1 when the slot holds a spool, 0 when it is empty.",
					boolValue(present), tl...)
			}
			// All zero means nothing is loaded, which is real information;
			// the family is absent entirely when the printer did not say.
			if rep.Print.AMS.TrayNow != nil {
				e.gauge("bambulab_ams_tray_active", "1 for the slot feeding the hotend, 0 for the rest.",
					boolValue(haveActive && i == activeAMS && j == activeSlot), tl...)
			}

			// remain is -1 when the spool has no usable estimate; emitting
			// that as a percentage would graph as a real reading.
			if tray.Remain != nil && tray.Remain.Int() >= 0 {
				e.gauge("bambulab_ams_tray_remaining_percent", "Filament remaining on the spool.",
					float64(*tray.Remain), tl...)
			}
			// Nominal, not current: grams left is this times the remaining
			// percentage, which is left to PromQL rather than exported as a
			// third metric derived from two others.
			num("bambulab_ams_tray_weight_grams", "Weight of a full spool of this filament.", tray.TrayWeight, tl...)
			// Base units, per promtool: the printer reports millimetres of
			// filament and whole hours of drying, both converted here.
			if l := tray.TotalLen; l != nil {
				e.gauge("bambulab_ams_tray_length_meters", "Filament length on a full spool.",
					float64(*l)/1000, tl...)
			}
			num("bambulab_ams_tray_nozzle_temp_min_celsius", "Lowest nozzle temperature this filament is rated for.", tray.NozzleTempMin, tl...)
			num("bambulab_ams_tray_nozzle_temp_max_celsius", "Highest nozzle temperature this filament is rated for.", tray.NozzleTempMax, tl...)
			num("bambulab_ams_tray_drying_temperature_celsius", "Drying temperature this filament wants.", tray.DryingTemp, tl...)
			if d := tray.DryingTime; d != nil {
				e.gauge("bambulab_ams_tray_drying_seconds", "Drying time this filament wants.",
					float64(*d)*3600, tl...)
			}
			num("bambulab_ams_tray_state", "Raw slot state from the firmware. The values have no published meaning (11 idle and 27 feeding are observations, not a contract) — use bambulab_ams_tray_active instead.", tray.State, tl...)

			if !tray.Loaded() {
				continue
			}
			diameter := ""
			if d := tray.TrayDiameter; d != nil {
				diameter = formatValue(float64(*d))
			}
			// Left empty when the printer reported no bitfield, so an
			// unknown spool is not labelled as a third-party one.
			bbl := ""
			if v, ok := rep.TrayIsBBL(i, j); ok {
				bbl = strconv.FormatBool(v)
			}
			e.gauge("bambulab_ams_tray_info", "Loaded filament per slot, as labels.", 1,
				"printer_name", printer, "ams", ams, "tray", strconv.Itoa(j),
				"type", tray.TrayType, "name", tray.Name(), "color", tray.TrayColor,
				"code", tray.TrayInfoIdx, "id_name", tray.TrayIDName,
				"spool_uid", tray.TagUID, "diameter_mm", diameter, "bbl", bbl)
		}
	}

	// The filament actually being printed with, as one series. This is what
	// makes "which spool did that print use" answerable after the fact —
	// per-slot info alone cannot say which slot was feeding.
	if tray, ams, slot, ok := rep.ActiveFilament(); ok && tray.Loaded() {
		e.gauge("bambulab_print_filament_info", "The filament feeding the hotend, as labels.", 1,
			"printer_name", printer, "ams", strconv.Itoa(ams), "tray", strconv.Itoa(slot),
			"type", tray.TrayType, "name", tray.Name(), "color", tray.TrayColor,
			"spool_uid", tray.TagUID)
	}

	// The external spool holder. This firmware reports it as vir_slot and
	// has no vt_tray key at all, so it is always present and usually empty
	// — hence the Loaded gate, or every scrape would carry a blank spool.
	for i := range rep.Print.VirSlot {
		slot := &rep.Print.VirSlot[i]
		if !slot.Loaded() {
			continue
		}
		idx := strconv.Itoa(i)
		if slot.Remain != nil && slot.Remain.Int() >= 0 {
			e.gauge("bambulab_external_spool_remaining_percent", "Filament remaining on the external spool.",
				float64(*slot.Remain), "printer_name", printer, "slot", idx)
		}
		e.gauge("bambulab_external_spool_info", "Filament on the external spool holder, as labels.", 1,
			"printer_name", printer, "slot", idx,
			"type", slot.TrayType, "name", slot.Name(), "color", slot.TrayColor)
	}

	// The sliced job from the cloud tasks API, already polled for the
	// status page's plate render. Weight is the only source of filament
	// grams anywhere in the pipeline — the MQTT report never gives it.
	if tasks != nil {
		if t, ok := tasks.get(); ok {
			if t.Weight > 0 {
				e.gauge("bambulab_print_task_weight_grams", "Filament the slicer estimated for the most recent job.", t.Weight, pn...)
			}
			if t.CostTime > 0 {
				e.gauge("bambulab_print_task_duration_seconds", "How long the most recent job took.", float64(t.CostTime), pn...)
			}
			// Raw: the code-to-outcome mapping is not documented, and
			// guessing names for it would be worse than a number.
			e.gauge("bambulab_print_task_status", "Status code of the most recent cloud task, as reported.", float64(t.Status), pn...)
			if ts, err := time.Parse(time.RFC3339, t.StartTime); err == nil {
				e.gauge("bambulab_print_task_start_timestamp_seconds", "When the most recent job started.", float64(ts.Unix()), pn...)
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
