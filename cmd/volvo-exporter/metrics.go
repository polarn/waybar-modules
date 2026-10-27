package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/polarn/waybar-modules/pkg/volvo"
)

type family struct {
	typ     string
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

func (e *exposition) gauge(name, help string, v float64, labels ...string) {
	e.sample("gauge", name, help, v, labels...)
}

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
		fmt.Fprintf(&b, "# HELP %s %s\n", name, helpEscaper.Replace(f.help))
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
		b.WriteString(labelEscaper.Replace(kv[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

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

func bool01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func number(v volvo.FieldValue) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(string(v)), 64)
	return f, err == nil
}

func meters(v volvo.FieldValue, unit string) (float64, bool) {
	n, ok := number(v)
	switch {
	case !ok:
		return 0, false
	case unit == "km":
		return n * 1000, true
	case unit == "mi":
		return n * 1609.344, true
	}
	return 0, false
}

func enum(v volvo.FieldValue) string { return strings.TrimSpace(string(v)) }

func sortedKeys(m volvo.CVMap) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func collect(v view) string {
	e := newExposition()

	e.gauge("volvo_auth_ok", "1 while the refresh token works; 0 means log in again through /auth.", bool01(v.authOK))
	e.gauge("volvo_up", "1 if the last poll read the energy state.", bool01(v.lastErr == nil && !v.lastPoll.IsZero()))
	if !v.lastPoll.IsZero() {
		e.gauge("volvo_last_poll_timestamp_seconds", "When the last poll finished.", float64(v.lastPoll.Unix()))
	}
	if !v.lastOK.IsZero() {
		e.gauge("volvo_last_success_timestamp_seconds", "When a poll last read the energy state.", float64(v.lastOK.Unix()))
	}
	for _, r := range []string{"ok", "reauth", "rate_limited", "error"} {
		e.counter("volvo_polls_total", "Polls by result, since the exporter started.", v.polls[r], "result", r)
	}
	e.counter("volvo_partial_errors_total", "Failed location or Connected Vehicle reads in otherwise good polls.", v.softErrors)
	for _, r := range []struct {
		result string
		n      float64
	}{{"ok", v.fastOK}, {"error", v.fastErrors}} {
		e.counter("volvo_fast_polls_total", "Doors, windows and engine re-reads between polls while the car is awake, by result.", r.n, "result", r.result)
	}

	s := v.snap
	if s == nil {
		return e.String()
	}
	vin := []string{"vin", s.VIN}
	g := func(name, help string, val float64, labels ...string) {
		e.gauge(name, help, val, append(append([]string{}, vin...), labels...)...)
	}

	en := s.Energy
	if f := en.BatteryChargeLevel; f.OK() {
		if n, ok := number(f.Value); ok {
			g("volvo_battery_charge_level_percent", "State of charge.", n)
		}
		if !f.UpdatedAt.IsZero() {
			g("volvo_energy_updated_timestamp_seconds", "When the car last reported its energy state to Volvo's cloud.", float64(f.UpdatedAt.Unix()))
		}
	}
	if f := en.TargetBatteryChargeLevel; f.OK() {
		if n, ok := number(f.Value); ok {
			g("volvo_target_battery_charge_level_percent", "Charge limit set in the car.", n)
		}
	}
	if f := en.ElectricRange; f.OK() {
		if n, ok := meters(f.Value, f.Unit); ok {
			g("volvo_electric_range_meters", "Estimated electric range.", n)
		}
	}
	if f := en.ChargingStatus; f.OK() {
		g("volvo_charging", "1 while charging.", bool01(enum(f.Value) == "CHARGING"))
		g("volvo_charging_status_info", "Charging status as reported.", 1, "status", enum(f.Value))
	}
	if f := en.ChargerConnectionStatus; f.OK() {
		g("volvo_charger_connected", "1 while a charger is plugged in.", bool01(enum(f.Value) == "CONNECTED"))
		g("volvo_charger_connection_status_info", "Charger connection status as reported.", 1, "status", enum(f.Value))
	}
	if f := en.ChargingPower; f.OK() {
		if n, ok := number(f.Value); ok {
			g("volvo_charging_power_watts", "Charging power.", n)
		}
	}
	if f := en.ChargingTimeToTarget; f.OK() {
		if n, ok := number(f.Value); ok {
			g("volvo_charging_time_to_target_seconds", "Estimated time until the charge limit.", n*60)
		}
	}

	cv := s.Connected
	if f, ok := cv["odometer"]["odometer"]; ok {
		if n, ok := meters(f.Value, f.Unit); ok {
			g("volvo_odometer_meters", "Odometer.", n)
		}
	}
	for _, st := range []struct{ key, name string }{
		{"distanceToEmptyBattery", "volvo_distance_to_empty_meters"},
		{"tripMeterAutomatic", "volvo_trip_meter_automatic_meters"},
		{"tripMeterManual", "volvo_trip_meter_manual_meters"},
	} {
		if f, ok := cv["statistics"][st.key]; ok {
			if n, ok := meters(f.Value, f.Unit); ok {
				g(st.name, "Trip statistic "+st.key+".", n)
			}
		}
	}
	if f, ok := cv["statistics"]["averageEnergyConsumption"]; ok && f.Unit == "kWh/100km" {
		if n, ok := number(f.Value); ok {
			g("volvo_average_energy_consumption_kwh_per_100km", "Average energy consumption since the trip meter reset.", n)
		}
	}
	if f, ok := cv["diagnostics"]["distanceToService"]; ok {
		if n, ok := meters(f.Value, f.Unit); ok {
			g("volvo_distance_to_service_meters", "Distance until the next service.", n)
		}
	}
	if f, ok := cv["doors"]["centralLock"]; ok {
		if l := enum(f.Value); l == "LOCKED" || l == "UNLOCKED" {
			g("volvo_locked", "1 while centrally locked.", bool01(l == "LOCKED"))
		}
	}
	for _, leaf := range []string{"doors", "windows"} {
		for _, k := range sortedKeys(cv[leaf]) {
			if k == "centralLock" {
				continue
			}
			switch st := enum(cv[leaf][k].Value); st {
			case "OPEN", "AJAR", "CLOSED":
				g("volvo_open", "1 while a door, hatch or window is open or ajar.", bool01(st != "CLOSED"), "part", k)
			}
		}
	}
	if f, ok := cv["engine-status"]["engineStatus"]; ok {
		g("volvo_engine_running", "1 while the engine (ignition) is running.", bool01(enum(f.Value) == "RUNNING"))
	}
	for _, leaf := range []string{"tyres", "warnings", "diagnostics"} {
		for _, k := range sortedKeys(cv[leaf]) {
			if leaf == "diagnostics" && !strings.HasSuffix(k, "Warning") {
				continue
			}
			st := enum(cv[leaf][k].Value)
			if st == "" || st == "UNSPECIFIED" {
				continue
			}
			g("volvo_warning", "1 while a tyre, bulb or service warning is active.", bool01(st != "NO_WARNING"), "source", leaf, "name", k)
		}
	}
	for _, leaf := range leaves {
		if t := newestReport(cv, leaf); !t.IsZero() {
			g("volvo_reported_timestamp_seconds", "When the car last reported this Connected Vehicle resource to Volvo's cloud.", float64(t.Unix()), "leaf", leaf)
		}
	}
	g("volvo_snapshot_timestamp_seconds", "When the exporter fetched the data above.", float64(s.FetchedAt.Unix()))
	return e.String()
}
