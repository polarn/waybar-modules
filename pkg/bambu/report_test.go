package bambu

import (
	"encoding/json"
	"testing"
)

// P2S-generation firmware reports no flat chamber_temper at all; the
// chamber lives under device.ctc. Trimmed from a live `status --raw`.
const p2sReport = `{"print":{
  "bed_temper": 75.0,
  "nozzle_temper": 245.0,
  "device": {"ctc": {"info": {"temp": 41}, "state": 0}}
}}`

// X1-era firmware has the flat field and no device.ctc.
const x1Report = `{"print":{"chamber_temper":"28"}}`

// A firmware that kept a stubbed flat field alongside the real ctc one.
const bothReport = `{"print":{
  "chamber_temper": 0,
  "device": {"ctc": {"info": {"temp": 41}}}
}}`

// Sibling device.* temps pack (target << 16) | current — 0x3c0029 is a
// 60 °C target with the chamber at 41. Can't be reproduced against a
// printer without running a heated-chamber print, hence the fixture.
const packedReport = `{"print":{"device":{"ctc":{"info":{"temp":3932201}}}}}`

// No chamber sensor (A1, P1P) — must be distinguishable from 0 °C.
const noChamberReport = `{"print":{"bed_temper":60.0}}`

func TestReportChamberTemp(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantTemp int
		wantOK   bool
	}{
		{"p2s reports device.ctc", p2sReport, 41, true},
		{"x1 falls back to flat field", x1Report, 28, true},
		{"ctc wins over a stubbed flat field", bothReport, 41, true},
		{"packed target is masked off", packedReport, 41, true},
		{"no sensor is not 0 °C", noChamberReport, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rep Report
			if err := json.Unmarshal([]byte(tc.body), &rep); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			temp, ok := rep.ChamberTemp()
			if temp != tc.wantTemp || ok != tc.wantOK {
				t.Errorf("ChamberTemp() = (%d, %t), want (%d, %t)",
					temp, ok, tc.wantTemp, tc.wantOK)
			}
		})
	}
}

// amsReport is trimmed from a live P2S `status --raw` with one AMS 2 Pro
// loaded: four slots, slot 2 feeding a mid-print job. Numbers arrive as
// numeric strings here and as bare numbers for remain, which is the mix
// Num exists to absorb. Note vir_slot, not vt_tray — this firmware has no
// vt_tray key at all, so the external spool shows up as an empty slot
// rather than as an absent field.
const amsReport = `{"print":{
  "gcode_state": "RUNNING",
  "ams_mapping": [2,-1,-1,-1],
  "nozzle_type": "HS01",
  "hms": [{"attr": 83887616, "code": 131184, "ts_unix": "20260807225940"}],
  "ams": {
    "tray_now": "2", "tray_tar": "2", "tray_pre": "2",
    "tray_exist_bits": "f", "tray_is_bbl_bits": "f",
    "ams": [{
      "id": "0", "humidity": "1", "humidity_raw": "41", "temp": "30.0", "dry_time": 0,
      "tray": [
        {"id": "0", "tray_type": "PLA", "tray_sub_brands": "PLA Basic",
         "tray_color": "FFFFFFFF", "tray_info_idx": "GFA00", "tray_id_name": "A00-W1",
         "tag_uid": "12775BEE00000100", "remain": 100, "tray_weight": "1000",
         "tray_diameter": "1.75", "total_len": 330000,
         "nozzle_temp_min": "190", "nozzle_temp_max": "230", "state": 11},
        {"id": "1", "tray_type": "PLA", "tray_sub_brands": "PLA Basic",
         "tray_color": "000000FF", "remain": 100, "state": 11},
        {"id": "2", "tray_type": "PETG", "tray_sub_brands": "PETG Translucent",
         "tray_color": "F9C1BD80", "tray_info_idx": "GFG01", "tag_uid": "4277732B00000100",
         "remain": 96, "tray_weight": "1000", "drying_temp": "65", "drying_time": "8",
         "nozzle_temp_min": "230", "nozzle_temp_max": "260", "state": 27},
        {"id": "3", "tray_type": "PETG", "remain": 67, "state": 11}
      ]}]},
  "vir_slot": [{"id": "255", "tray_type": "", "tray_color": "00000000",
                "remain": 0, "tag_uid": "0000000000000000"}]
 },
 "info": {"module": [
   {"name": "ota", "product_name": "Bambu Lab P2S", "hw_ver": "N/A", "sw_ver": "01.02.00.00", "visible": true},
   {"name": "n3f/0", "product_name": "AMS 2 Pro (1)", "hw_ver": "N3F05", "sw_ver": "04.00.21.87", "visible": true}
 ]}}`

func parseAMS(t *testing.T) *Report {
	t.Helper()
	var rep Report
	if err := json.Unmarshal([]byte(amsReport), &rep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &rep
}

func TestActiveFilament(t *testing.T) {
	rep := parseAMS(t)
	tray, ams, slot, ok := rep.ActiveFilament()
	if !ok {
		t.Fatal("ActiveFilament() not ok, want the slot 2 spool")
	}
	if ams != 0 || slot != 2 {
		t.Errorf("ActiveFilament() = ams %d slot %d, want ams 0 slot 2", ams, slot)
	}
	if got := tray.Name(); got != "PETG Translucent" {
		t.Errorf("Name() = %q, want %q", got, "PETG Translucent")
	}
	if tray.TrayColor != "F9C1BD80" {
		t.Errorf("TrayColor = %q, want F9C1BD80", tray.TrayColor)
	}
	// Numeric strings and bare numbers in the same struct.
	if got := tray.Remain.Int(); got != 96 {
		t.Errorf("Remain = %d, want 96", got)
	}
	if got := tray.NozzleTempMax.Int(); got != 260 {
		t.Errorf("NozzleTempMax = %d, want 260", got)
	}
}

func TestActiveTrayEdges(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"nothing loaded", `{"print":{"ams":{"tray_now":"255","ams":[{"tray":[{}]}]}}}`, false},
		// Single-slot AMS HT units number from 128 up instead of continuing
		// the fours, so the fours arithmetic must not point at a real slot.
		{"ams ht index", `{"print":{"ams":{"tray_now":"128","ams":[{"tray":[{}]}]}}}`, false},
		{"field absent", `{"print":{"ams":{"ams":[{"tray":[{}]}]}}}`, false},
		{"slot beyond reported trays", `{"print":{"ams":{"tray_now":"3","ams":[{"tray":[{}]}]}}}`, false},
		{"first slot", `{"print":{"ams":{"tray_now":"0","ams":[{"tray":[{"tray_type":"PLA"}]}]}}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rep Report
			if err := json.Unmarshal([]byte(tc.body), &rep); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, _, _, ok := rep.ActiveFilament(); ok != tc.want {
				t.Errorf("ActiveFilament() ok = %t, want %t", ok, tc.want)
			}
		})
	}
}

func TestTrayPresentDistinguishesEmptyFromAbsent(t *testing.T) {
	rep := parseAMS(t)
	for slot := 0; slot < 4; slot++ {
		present, ok := rep.TrayPresent(0, slot)
		if !ok || !present {
			t.Errorf("TrayPresent(0, %d) = (%t, %t), want (true, true) from bits %q",
				slot, present, ok, rep.Print.AMS.TrayExistBits)
		}
	}
	// "f" is slots 0-3 only; slot 4 of a second unit is not set.
	if present, ok := rep.TrayPresent(1, 0); !ok || present {
		t.Errorf("TrayPresent(1, 0) = (%t, %t), want (false, true)", present, ok)
	}
	// An absent field must not read as "empty" — that was the ambiguity
	// this metric exists to remove.
	var bare Report
	if _, ok := bare.TrayPresent(0, 0); ok {
		t.Error("TrayPresent() ok = true with no bitfield reported, want false")
	}
}

func TestTrayNameFallsBackToMaterial(t *testing.T) {
	rep := parseAMS(t)
	trays := rep.Print.AMS.AMS[0].Tray
	if got := trays[3].Name(); got != "PETG" {
		t.Errorf("Name() with no sub-brand = %q, want %q", got, "PETG")
	}
	if !trays[3].Loaded() {
		t.Error("Loaded() = false for a slot with a material but no sub-brand")
	}
	// The external spool is empty, and must not be reported as filament.
	if got := rep.Print.VirSlot; len(got) != 1 {
		t.Fatalf("VirSlot length = %d, want 1", len(got))
	}
	if rep.Print.VirSlot[0].Loaded() {
		t.Error("empty vir_slot reports Loaded() = true")
	}
}

func TestJobSlots(t *testing.T) {
	rep := parseAMS(t)
	if got := rep.JobSlots(); len(got) != 1 || got[0] != 2 {
		t.Errorf("JobSlots() = %v, want [2]", got)
	}
	// Multi-material: consumption cannot be credited to one spool.
	var multi Report
	if err := json.Unmarshal([]byte(`{"print":{"ams_mapping":[3,0,-1,1]}}`), &multi); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := multi.JobSlots()
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 3 {
		t.Errorf("JobSlots() = %v, want [0 1 3]", got)
	}
}

func TestHMS(t *testing.T) {
	rep := parseAMS(t)
	if len(rep.Print.HMS) != 1 {
		t.Fatalf("HMS length = %d, want 1", len(rep.Print.HMS))
	}
	e := rep.Print.HMS[0]
	// attr 83887616 is 0x05000600, code 131184 is 0x00020070.
	if got := HMSCode(e.Attr, e.Code); got != "0500_0600_0002_0070" {
		t.Errorf("HMSCode() = %q, want %q", got, "0500_0600_0002_0070")
	}
	if got := HMSSeverity(e.Code); got != "serious" {
		t.Errorf("HMSSeverity() = %q, want %q", got, "serious")
	}
	if got := HMSCode(nil, nil); got != "" {
		t.Errorf("HMSCode(nil, nil) = %q, want empty", got)
	}
	// Bambu's catalogue only uses 1-3; a fourth value must not be invented
	// into a name the alert rules would then never match.
	four := Num(4 << 16)
	if got := HMSSeverity(&four); got != "unknown" {
		t.Errorf("HMSSeverity(4) = %q, want %q", got, "unknown")
	}
}

func TestOTAModule(t *testing.T) {
	rep := parseAMS(t)
	m, ok := rep.OTAModule()
	if !ok {
		t.Fatal("OTAModule() not ok")
	}
	if m.ProductName != "Bambu Lab P2S" || m.SWVer != "01.02.00.00" {
		t.Errorf("OTAModule() = %+v, want the P2S at 01.02.00.00", m)
	}
	var bare Report
	if _, ok := bare.OTAModule(); ok {
		t.Error("OTAModule() ok = true with no module list, want false")
	}
}

func TestSpeedProfile(t *testing.T) {
	for _, tc := range []struct {
		lvl  int
		want string
	}{{1, "silent"}, {2, "standard"}, {3, "sport"}, {4, "ludicrous"}, {9, ""}} {
		n := Num(tc.lvl)
		if got := SpeedProfile(&n); got != tc.want {
			t.Errorf("SpeedProfile(%d) = %q, want %q", tc.lvl, got, tc.want)
		}
	}
	if got := SpeedProfile(nil); got != "" {
		t.Errorf("SpeedProfile(nil) = %q, want empty", got)
	}
}
