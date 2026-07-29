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
		McPercent       *Num   `json:"mc_percent"`
		McRemainingTime *Num   `json:"mc_remaining_time"`
		LayerNum        *Num   `json:"layer_num"`
		TotalLayerNum   *Num   `json:"total_layer_num"`
		NozzleTemper    *Num   `json:"nozzle_temper"`
		BedTemper       *Num   `json:"bed_temper"`
		ChamberTemper   *Num   `json:"chamber_temper"`
		AMS             struct {
			AMS []struct {
				Humidity *Num `json:"humidity"`
				Temp     *Num `json:"temp"`
				Tray     []struct {
					TrayType  string `json:"tray_type"`
					TrayColor string `json:"tray_color"`
					Remain    *Num   `json:"remain"`
				} `json:"tray"`
			} `json:"ams"`
		} `json:"ams"`
	} `json:"print"`
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
