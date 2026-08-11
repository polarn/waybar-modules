package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
)

func quiet(string, ...any) {}

// jobReport is a report with one AMS, slot 2 feeding PETG Translucent —
// the shape ActiveFilament has to resolve for consumption to be credited
// to anything. mapping selects how many slots the job draws from.
func jobReport(t *testing.T, state, title, mapping string) *bambu.Report {
	t.Helper()
	body := `{"print":{
	  "gcode_state": "` + state + `",
	  "subtask_name": "` + title + `",
	  "ams_mapping": ` + mapping + `,
	  "ams": {"tray_now": "2", "ams": [{"tray": [{}, {},
	    {"tray_type": "PETG", "tray_sub_brands": "PETG Translucent", "tray_color": "F9C1BD80"},
	    {}]}]}}}`
	var rep bambu.Report
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &rep
}

func newTestWatch() *jobWatch {
	return newJobWatch(newTaskCache("token", "serial"))
}

// setTask plants a cloud task without going near the network.
func setTask(w *jobWatch, title string, weight float64) {
	w.tasks.mu.Lock()
	w.tasks.task = &bambu.Task{Title: title, Weight: weight}
	w.tasks.mu.Unlock()
}

func TestFirstObservationDoesNotCount(t *testing.T) {
	// A pod restarting while the printer sits at FINISH must not invent a
	// print it never saw run.
	w := newTestWatch()
	w.observe(jobReport(t, "FINISH", "old job", "[2,-1,-1,-1]"), quiet)
	if got := w.prints["finished"]; got != 0 {
		t.Errorf("prints[finished] = %v after adopting FINISH, want 0", got)
	}
	if w.pending != nil {
		t.Error("pending credit created on first observation")
	}
}

func TestFinishedPrintCreditsFilament(t *testing.T) {
	w := newTestWatch()
	w.observe(jobReport(t, "RUNNING", "lamp bracket", "[2,-1,-1,-1]"), quiet)
	w.observe(jobReport(t, "FINISH", "lamp bracket", "[2,-1,-1,-1]"), quiet)

	if got := w.prints["finished"]; got != 1 {
		t.Fatalf("prints[finished] = %v, want 1", got)
	}
	if w.pending == nil {
		t.Fatal("no pending credit after a finished print")
	}
	if w.pending.key.name != "PETG Translucent" {
		t.Errorf("credited to %q, want PETG Translucent", w.pending.key.name)
	}

	// The weight only exists in the cloud task history, which lags.
	setTask(w, "lamp bracket", 20.12)
	w.observe(jobReport(t, "FINISH", "lamp bracket", "[2,-1,-1,-1]"), quiet)

	want := filamentKey{typ: "PETG", name: "PETG Translucent", color: "F9C1BD80"}
	if got := w.filament[want]; got != 20.12 {
		t.Errorf("filament[%+v] = %v, want 20.12", want, got)
	}
	if w.pending != nil {
		t.Error("pending credit not cleared after being applied")
	}
	// Re-observing the same terminal state must not double-count.
	w.observe(jobReport(t, "FINISH", "lamp bracket", "[2,-1,-1,-1]"), quiet)
	if got := w.filament[want]; got != 20.12 {
		t.Errorf("filament re-credited on a repeat scrape: %v", got)
	}
	if got := w.prints["finished"]; got != 1 {
		t.Errorf("prints[finished] = %v after a repeat observation, want 1", got)
	}
}

func TestMismatchedTaskIsNotCredited(t *testing.T) {
	// Crediting whatever task happens to be newest would be silently
	// wrong, so a mismatch waits and then gives up.
	w := newTestWatch()
	w.observe(jobReport(t, "RUNNING", "job a", "[2,-1,-1,-1]"), quiet)
	w.observe(jobReport(t, "FINISH", "job a", "[2,-1,-1,-1]"), quiet)
	setTask(w, "some other job", 99)
	w.observe(jobReport(t, "FINISH", "job a", "[2,-1,-1,-1]"), quiet)

	if len(w.filament) != 0 {
		t.Errorf("credited %v from a mismatched task, want nothing", w.filament)
	}
	if w.pending == nil {
		t.Fatal("pending credit dropped too early")
	}

	var logged string
	w.pending.since = time.Now().Add(-11 * time.Minute)
	w.observe(jobReport(t, "FINISH", "job a", "[2,-1,-1,-1]"),
		func(f string, a ...any) { logged = f })
	if w.pending != nil {
		t.Error("pending credit not dropped after the timeout")
	}
	if !strings.Contains(logged, "no cloud task matched") {
		t.Errorf("timeout was not logged, got %q", logged)
	}
	if len(w.filament) != 0 {
		t.Errorf("credited %v after giving up, want nothing", w.filament)
	}
}

func TestFailedPrintCountsButCreditsNothing(t *testing.T) {
	// A failure stopped somewhere unknown; the slicer's estimate is for the
	// whole plate and would overstate what was actually extruded.
	w := newTestWatch()
	w.observe(jobReport(t, "RUNNING", "doomed", "[2,-1,-1,-1]"), quiet)
	w.observe(jobReport(t, "FAILED", "doomed", "[2,-1,-1,-1]"), quiet)

	if got := w.prints["failed"]; got != 1 {
		t.Errorf("prints[failed] = %v, want 1", got)
	}
	if got := w.prints["finished"]; got != 0 {
		t.Errorf("prints[finished] = %v, want 0", got)
	}
	if w.pending != nil {
		t.Error("a failed print created a filament credit")
	}
}

func TestMultiMaterialIsNotAttributedToOneSpool(t *testing.T) {
	w := newTestWatch()
	w.observe(jobReport(t, "RUNNING", "two colours", "[2,0,-1,-1]"), quiet)
	w.observe(jobReport(t, "FINISH", "two colours", "[2,0,-1,-1]"), quiet)
	setTask(w, "two colours", 42)
	w.observe(jobReport(t, "FINISH", "two colours", "[2,0,-1,-1]"), quiet)

	if got := w.filament[multiMaterial]; got != 42 {
		t.Errorf("filament[multi-material] = %v, want 42", got)
	}
	for k := range w.filament {
		if k.name == "PETG Translucent" {
			t.Error("multi-material weight credited to a single spool")
		}
	}
}

func TestPauseDoesNotLoseTheRunningFilament(t *testing.T) {
	// Run state is keyed on the job title, not the state machine, so a
	// pause mid-print does not read as the end of a run.
	w := newTestWatch()
	w.observe(jobReport(t, "RUNNING", "paused job", "[2,-1,-1,-1]"), quiet)
	w.observe(jobReport(t, "PAUSE", "paused job", "[2,-1,-1,-1]"), quiet)
	w.observe(jobReport(t, "FINISH", "paused job", "[2,-1,-1,-1]"), quiet)

	if w.pending == nil {
		t.Fatal("no pending credit after a print that was paused")
	}
	if w.pending.key.name != "PETG Translucent" {
		t.Errorf("credited to %q, want PETG Translucent", w.pending.key.name)
	}
}

func TestCollectEmitsZeroBaseline(t *testing.T) {
	// increase() needs something to measure the first failure against.
	w := newTestWatch()
	e := newExposition()
	w.collect(e, "bambis")
	out := e.String()

	for _, want := range []string{
		"# TYPE bambulab_prints_total counter",
		`bambulab_prints_total{printer_name="bambis",result="finished"} 0`,
		`bambulab_prints_total{printer_name="bambis",result="failed"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("collect() missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bambulab_prints_total gauge") {
		t.Error("counter emitted with TYPE gauge")
	}
}
