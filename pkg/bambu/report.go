package bambu

import (
	"strconv"
	"strings"
)

// Report is the slice of the printer's MQTT report that gets rendered.
// Bambu mixes JSON numbers and numeric strings across firmwares and
// fields, hence Num everywhere.
type Report struct {
	Print struct {
		GcodeState      string `json:"gcode_state"`
		SubtaskName     string `json:"subtask_name"`
		StgCur          *Num   `json:"stg_cur"` // current stage — see StageName

		McPercent       *Num   `json:"mc_percent"`
		McRemainingTime *Num   `json:"mc_remaining_time"`
		LayerNum        *Num   `json:"layer_num"`
		TotalLayerNum   *Num   `json:"total_layer_num"`
		NozzleTemper    *Num   `json:"nozzle_temper"`
		BedTemper       *Num   `json:"bed_temper"`
		ChamberTemper   *Num   `json:"chamber_temper"` // X1-era only — use Report.ChamberTemp
		Device          struct {
			CTC struct { // chamber temperature control
				Info struct {
					Temp *Num `json:"temp"`
				} `json:"info"`
			} `json:"ctc"`
		} `json:"device"`
		AMS struct {
			AMS []struct {
				Humidity *Num `json:"humidity"` // coarse 1-5 index (all AMS models)
				// HumidityRaw is actual %RH — AMS 2 Pro and newer only.
				HumidityRaw *Num `json:"humidity_raw"`
				Temp        *Num `json:"temp"`
				DryTime     *Num `json:"dry_time"` // minutes left of an active drying run
				Tray        []struct {
					TrayType      string `json:"tray_type"`
					TraySubBrands string `json:"tray_sub_brands"` // e.g. "PLA Basic"
					TrayColor     string `json:"tray_color"`
					Remain        *Num   `json:"remain"`
				} `json:"tray"`
			} `json:"ams"`
		} `json:"ams"`
	} `json:"print"`
}

// ChamberTemp returns the chamber temperature in °C, and false when the
// printer reports none. P2S-generation firmware dropped the flat
// chamber_temper field and reports the chamber under device.ctc
// ("chamber temperature control") instead; X1-era firmware has only the
// flat field. Preferring ctc means a firmware that re-adds a stubbed
// chamber_temper: 0 can't silently win.
func (r *Report) ChamberTemp() (int, bool) {
	if t := r.Print.Device.CTC.Info.Temp; t != nil {
		// device.* packs temps as (target << 16) | current, the way the bed
		// and extruder siblings do. The chamber has no target today (high
		// half 0); mask so a future one can't render as a 7-digit °C.
		return t.Int() & 0xffff, true
	}
	if t := r.Print.ChamberTemper; t != nil {
		return t.Int(), true
	}
	return 0, false
}

// Num tolerates a JSON number or a numeric string. Junk parses to 0
// rather than failing the whole report.
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*n = Num(v)
	}
	return nil
}

// Int renders the value rounded down; nil-safe (nil -> 0).
func (n *Num) Int() int {
	if n == nil {
		return 0
	}
	return int(*n)
}
