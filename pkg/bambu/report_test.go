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
