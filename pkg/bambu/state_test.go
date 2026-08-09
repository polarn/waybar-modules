package bambu

import "testing"

// A pushall snapshot, trimmed to the fields that matter here.
const snapshot = `{"print":{
  "gcode_state": "RUNNING",
  "subtask_name": "snake",
  "mc_percent": 15,
  "nozzle_temper": 245.0,
  "bed_temper": 75.0,
  "device": {"ctc": {"info": {"temp": 41}}, "bed": {"info": {"temp": 4915275}}},
  "ams": {"ams": [{"humidity_raw": "39", "temp": "36.0"}]}
}}`

// A partial push of the kind the printer streams between pushalls: it
// carries progress and nothing else. Merging is what stops every other
// field reading as zero.
const progressDelta = `{"print":{"mc_percent":16,"layer_num":8}}`

func TestStateMergesPartialPushes(t *testing.T) {
	var st State

	if _, ok := st.Report(); ok {
		t.Fatal("Report() reported data before anything was applied")
	}
	if _, ok := st.Age(); ok {
		t.Fatal("Age() reported data before anything was applied")
	}

	if err := st.Apply([]byte(snapshot)); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if err := st.Apply([]byte(progressDelta)); err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	rep, ok := st.Report()
	if !ok {
		t.Fatal("Report() returned nothing after two applies")
	}

	// The delta's own fields win.
	if got := rep.Print.McPercent.Int(); got != 16 {
		t.Errorf("mc_percent = %d, want 16 (delta should win)", got)
	}
	if got := rep.Print.LayerNum.Int(); got != 8 {
		t.Errorf("layer_num = %d, want 8", got)
	}

	// Everything the delta omitted must survive. Without merging these all
	// read as zero, which is exactly how the chamber bug stayed invisible.
	if got := rep.Print.NozzleTemper.Int(); got != 245 {
		t.Errorf("nozzle_temper = %d, want 245 (survives a delta that omits it)", got)
	}
	if got := rep.Print.BedTemper.Int(); got != 75 {
		t.Errorf("bed_temper = %d, want 75", got)
	}
	if got := rep.Print.GcodeState; got != "RUNNING" {
		t.Errorf("gcode_state = %q, want RUNNING", got)
	}
	chamber, ok := rep.ChamberTemp()
	if !ok || chamber != 41 {
		t.Errorf("ChamberTemp() = (%d, %t), want (41, true)", chamber, ok)
	}
	if len(rep.Print.AMS.AMS) != 1 || rep.Print.AMS.AMS[0].HumidityRaw.Int() != 39 {
		t.Errorf("AMS block did not survive the delta: %+v", rep.Print.AMS.AMS)
	}
}

func TestStateMergeSemantics(t *testing.T) {
	cases := []struct {
		name  string
		apply []string
		check func(*testing.T, *Report)
	}{
		{
			name:  "sibling keys in a nested object are preserved",
			apply: []string{snapshot, `{"print":{"device":{"bed":{"info":{"temp":1}}}}}`},
			check: func(t *testing.T, rep *Report) {
				// Writing device.bed must not drop device.ctc.
				if c, ok := rep.ChamberTemp(); !ok || c != 41 {
					t.Errorf("ChamberTemp() = (%d, %t), want (41, true)", c, ok)
				}
			},
		},
		{
			name: "arrays replace wholesale rather than merging by index",
			apply: []string{snapshot,
				`{"print":{"ams":{"ams":[{"humidity_raw":"12"}]}}}`},
			check: func(t *testing.T, rep *Report) {
				if got := rep.Print.AMS.AMS[0].HumidityRaw.Int(); got != 12 {
					t.Errorf("humidity_raw = %d, want 12", got)
				}
				// temp came only from the replaced element, so it is gone —
				// documenting the trade, not asserting it is desirable.
				if rep.Print.AMS.AMS[0].Temp != nil {
					t.Errorf("temp = %v, want nil after wholesale array replace",
						rep.Print.AMS.AMS[0].Temp)
				}
			},
		},
		{
			name:  "later scalar wins",
			apply: []string{snapshot, `{"print":{"gcode_state":"PAUSE"}}`},
			check: func(t *testing.T, rep *Report) {
				if rep.Print.GcodeState != "PAUSE" {
					t.Errorf("gcode_state = %q, want PAUSE", rep.Print.GcodeState)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var st State
			for i, body := range tc.apply {
				if err := st.Apply([]byte(body)); err != nil {
					t.Fatalf("apply %d: %v", i, err)
				}
			}
			rep, ok := st.Report()
			if !ok {
				t.Fatal("Report() returned nothing")
			}
			tc.check(t, rep)
		})
	}
}

func TestStateRejectsGarbage(t *testing.T) {
	var st State
	if err := st.Apply([]byte(`{"print":`)); err == nil {
		t.Error("Apply accepted truncated JSON")
	}
	// A rejected message must not count as data.
	if _, ok := st.Report(); ok {
		t.Error("Report() returned data after only a failed Apply")
	}
}
