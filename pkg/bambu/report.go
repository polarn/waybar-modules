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
		GcodeState  string `json:"gcode_state"`
		SubtaskName string `json:"subtask_name"`
		StgCur      *Num   `json:"stg_cur"` // current stage — see StageName

		// Job provenance. PrintType is "cloud" for a MakerWorld/Handy
		// slice and "local" for one sent from the slicer; ModelID and
		// DesignID identify the MakerWorld model behind a cloud print.
		PrintType string `json:"print_type"`
		ModelID   string `json:"model_id"`
		DesignID  string `json:"design_id"`

		McPercent       *Num `json:"mc_percent"`
		McRemainingTime *Num `json:"mc_remaining_time"`
		LayerNum        *Num `json:"layer_num"`
		TotalLayerNum   *Num `json:"total_layer_num"`
		NozzleTemper    *Num `json:"nozzle_temper"`
		BedTemper       *Num `json:"bed_temper"`
		ChamberTemper   *Num `json:"chamber_temper"` // X1-era only — use Report.ChamberTemp

		NozzleTargetTemper *Num   `json:"nozzle_target_temper"`
		BedTargetTemper    *Num   `json:"bed_target_temper"`
		NozzleDiameter     *Num   `json:"nozzle_diameter"`
		NozzleType         string `json:"nozzle_type"` // e.g. "HS01" (hardened steel)
		PrintError         *Num   `json:"print_error"`
		McPrintErrorCode   *Num   `json:"mc_print_error_code"`
		FailReason         *Num   `json:"fail_reason"`
		SpdLvl             *Num   `json:"spd_lvl"` // 1=silent 2=standard 3=sport 4=ludicrous
		SpdMag             *Num   `json:"spd_mag"` // speed as a percentage

		// Fan speeds are 0-15 gear values, not percentages.
		CoolingFanSpeed   *Num `json:"cooling_fan_speed"`
		BigFan1Speed      *Num `json:"big_fan1_speed"`
		BigFan2Speed      *Num `json:"big_fan2_speed"`
		HeatbreakFanSpeed *Num `json:"heatbreak_fan_speed"`

		// WifiSignal carries its unit ("-56dBm"), so Num can't parse it —
		// see WifiSignalDBm.
		WifiSignal string `json:"wifi_signal"`

		// HMS is the printer's latched fault list; see hms.go.
		HMS []HMSEntry `json:"hms"`

		// AMSMapping maps each of the job's filaments to an AMS slot, -1
		// where unused. More than one live entry is a multi-material
		// print — see JobSlots.
		AMSMapping []Num `json:"ams_mapping"`

		LightsReport []struct {
			Node string `json:"node"` // chamber_light, work_light
			Mode string `json:"mode"` // on, off, flashing
		} `json:"lights_report"`

		IPCam struct {
			// Timelapse storage, reported in kibibytes.
			TLInternalFreeKB  *Num `json:"tl_internal_free_kb"`
			TLInternalTotalKB *Num `json:"tl_internal_total_kb"`
		} `json:"ipcam"`

		UpgradeState struct {
			// Semantics are not documented and this printer reports 2 while
			// up to date; exported as a raw number, not as a boolean.
			NewVersionState *Num `json:"new_version_state"`
		} `json:"upgrade_state"`

		Device struct {
			CTC struct { // chamber temperature control
				Info struct {
					Temp *Num `json:"temp"`
				} `json:"info"`
			} `json:"ctc"`
			Nozzle struct {
				Info []struct {
					Diameter *Num   `json:"diameter"`
					Type     string `json:"type"`
					Wear     *Num   `json:"wear"`
				} `json:"info"`
			} `json:"nozzle"`
		} `json:"device"`

		AMS struct {
			AMS []AMSUnit `json:"ams"`

			// Slot indices are global across units, four per unit; 255
			// means none. TrayNow is what is loaded, TrayTar what the
			// printer is switching to.
			TrayNow *Num `json:"tray_now"`
			TrayTar *Num `json:"tray_tar"`
			TrayPre *Num `json:"tray_pre"`

			// Nibble bitfields as hex strings ("f" = slots 0-3 of AMS 0).
			// See TrayPresent / TrayIsBBL.
			TrayExistBits string `json:"tray_exist_bits"`
			TrayIsBBLBits string `json:"tray_is_bbl_bits"`
		} `json:"ams"`

		// VirSlot is the external spool holder. Note this firmware has no
		// vt_tray key at all — the field every guide names does not exist
		// here, and reads as an empty slot rather than as absent.
		VirSlot []Tray `json:"vir_slot"`
	} `json:"print"`

	// Info arrives on a separate topic (the get_version response) rather
	// than in a status push, so it is absent until one has been merged in.
	Info struct {
		Module []Module `json:"module"`
	} `json:"info"`
}

// Tray is one filament slot: an AMS slot, or the external spool under
// print.vir_slot. An empty slot still reports, with blank strings and
// zeroed numbers — see Loaded.
type Tray struct {
	ID            string `json:"id"`
	TrayType      string `json:"tray_type"`       // material, e.g. "PETG"
	TraySubBrands string `json:"tray_sub_brands"` // product, e.g. "PETG Translucent"
	TrayColor     string `json:"tray_color"`      // RRGGBBAA
	TrayInfoIdx   string `json:"tray_info_idx"`   // Bambu filament code, e.g. "GFG01"
	TrayIDName    string `json:"tray_id_name"`    // e.g. "G01-P1"
	TagUID        string `json:"tag_uid"`         // per-spool RFID tag
	TrayUUID      string `json:"tray_uuid"`

	Remain       *Num `json:"remain"`        // percent, -1 when not estimable
	TrayWeight   *Num `json:"tray_weight"`   // grams, nominal full spool
	TrayDiameter *Num `json:"tray_diameter"` // mm
	TotalLen     *Num `json:"total_len"`     // mm of a full spool

	NozzleTempMin *Num `json:"nozzle_temp_min"` // the filament's range, °C
	NozzleTempMax *Num `json:"nozzle_temp_max"`
	DryingTemp    *Num `json:"drying_temp"` // °C
	DryingTime    *Num `json:"drying_time"` // hours

	// State is a firmware internal with no published meaning (this printer
	// reports 11 for idle slots and 27 for the feeding one, but that is an
	// observation, not a contract). TrayNow is the authoritative answer to
	// which slot is active.
	State *Num `json:"state"`
}

// Name is the most specific description of the loaded filament, falling
// back to the bare material when the printer reports no sub-brand.
func (t *Tray) Name() string {
	if t.TraySubBrands != "" {
		return t.TraySubBrands
	}
	return t.TrayType
}

// Loaded reports whether the slot holds identifiable filament. An empty
// slot and an absent AMS both report nothing useful, so callers that need
// to tell those apart want Report.TrayPresent as well.
func (t *Tray) Loaded() bool { return t.Name() != "" }

// AMSUnit is one AMS. Humidity comes two ways: the coarse 1-5 index every
// model reports, and a real percentage on AMS 2 Pro and newer.
type AMSUnit struct {
	ID          string `json:"id"`
	Humidity    *Num   `json:"humidity"`
	HumidityRaw *Num   `json:"humidity_raw"`
	Temp        *Num   `json:"temp"`
	DryTime     *Num   `json:"dry_time"` // minutes left of an active drying run
	Tray        []Tray `json:"tray"`
}

// HMSEntry is one latched fault. See HMSCode and HMSSeverity.
type HMSEntry struct {
	Attr *Num `json:"attr"`
	Code *Num `json:"code"`
}

// Module is one hardware component from the printer's get_version
// response — the main controller, each AMS, the filament buffer, and
// several invisible submodules.
//
// The serial (sn) is deliberately not modelled: it is credential-adjacent
// and this feeds Prometheus labels and a status page.
type Module struct {
	Name        string `json:"name"` // "ota" is the main controller
	ProductName string `json:"product_name"`
	HWVer       string `json:"hw_ver"`
	SWVer       string `json:"sw_ver"`
	Visible     bool   `json:"visible"`
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

// WifiSignalDBm parses the signal strength, which the printer reports
// with its unit attached ("-56dBm"). ok is false when the field is absent
// or shaped differently on some other firmware.
func (r *Report) WifiSignalDBm() (int, bool) {
	s := strings.TrimSuffix(strings.TrimSpace(r.Print.WifiSignal), "dBm")
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ActiveTray resolves ams.tray_now to a unit and slot. Slots are numbered
// globally in fours, so index 6 is unit 1 slot 2, and 255 means nothing is
// loaded.
//
// The bounds check against the reported units matters: single-slot AMS HT
// units number from 128 up rather than continuing the fours, so on a
// printer with one of those attached this returns false instead of
// pointing at a slot that does not exist.
func (r *Report) ActiveTray() (ams, slot int, ok bool) {
	n := r.Print.AMS.TrayNow
	if n == nil {
		return 0, 0, false
	}
	idx := n.Int()
	if idx < 0 || idx/4 >= len(r.Print.AMS.AMS) {
		return 0, 0, false
	}
	return idx / 4, idx % 4, true
}

// ActiveFilament returns the tray currently feeding the hotend.
func (r *Report) ActiveFilament() (*Tray, int, int, bool) {
	ams, slot, ok := r.ActiveTray()
	if !ok {
		return nil, 0, 0, false
	}
	trays := r.Print.AMS.AMS[ams].Tray
	if slot >= len(trays) {
		return nil, 0, 0, false
	}
	return &trays[slot], ams, slot, true
}

// JobSlots returns the distinct AMS slots the running job draws filament
// from, in ascending order. More than one means a multi-material print,
// which is why per-print consumption cannot be credited to one spool.
func (r *Report) JobSlots() []int {
	seen := make(map[int]bool)
	var out []int
	for _, m := range r.Print.AMSMapping {
		v := int(m)
		if v < 0 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sortInts(out)
	return out
}

// TrayPresent reports whether a slot is physically occupied, from the
// ams.tray_exist_bits nibble field. ok is false when the printer did not
// report the field — which has to stay distinct from "slot is empty",
// because an absent AMS reports neither.
func (r *Report) TrayPresent(ams, slot int) (present, ok bool) {
	return bitSet(r.Print.AMS.TrayExistBits, ams*4+slot)
}

// TrayIsBBL reports whether the slot holds a genuine Bambu spool (one
// whose RFID tag the printer could read), from ams.tray_is_bbl_bits.
func (r *Report) TrayIsBBL(ams, slot int) (bbl, ok bool) {
	return bitSet(r.Print.AMS.TrayIsBBLBits, ams*4+slot)
}

// OTAModule returns the main controller's entry, which carries the
// printer's product name and the firmware version Handy shows.
func (r *Report) OTAModule() (Module, bool) {
	for _, m := range r.Info.Module {
		if m.Name == "ota" {
			return m, true
		}
	}
	return Module{}, false
}

// bitSet reads one bit out of a hex-string bitfield ("f" = bits 0-3 set).
// ok is false for an absent or unparsable field.
func bitSet(field string, idx int) (set, ok bool) {
	if field == "" || idx < 0 || idx >= 64 {
		return false, false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(field, "0x"), 16, 64)
	if err != nil {
		return false, false
	}
	return v&(1<<uint(idx)) != 0, true
}

// sortInts is an insertion sort; the slices here are at most a handful of
// AMS slots and the repo has no sort import elsewhere in this package.
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// FanPercent converts a 0-15 fan gear value to a percentage.
func FanPercent(n *Num) float64 {
	if n == nil {
		return 0
	}
	return float64(*n) / 15 * 100
}

// SpeedProfile names a spd_lvl value the way the printer's own UI does.
func SpeedProfile(n *Num) string {
	if n == nil {
		return ""
	}
	switch n.Int() {
	case 1:
		return "silent"
	case 2:
		return "standard"
	case 3:
		return "sport"
	case 4:
		return "ludicrous"
	}
	return ""
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
