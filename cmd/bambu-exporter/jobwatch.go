package main

import (
	"html"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
)

// jobWatch accumulates the two things a per-scrape snapshot cannot
// produce: how many prints reached a terminal state, and how much filament
// each one consumed.
//
// Everything else in this exporter is derived fresh from the latest report
// on each scrape, which is what makes stale series disappear on their own.
// Counters need the opposite, so this is the one piece that holds state.
// It is deliberately in-process and not persisted: a volume would cost the
// container its readOnlyRootFilesystem, and a counter reset is something
// Prometheus already models — query these with increase(), not by reading
// the raw total.
//
// It observes on a ticker rather than hooking the MQTT path so pkg/bambu
// keeps its current API. Terminal states latch until the next print
// starts, so nothing is missed by sampling; the cost is one JSON round
// trip per tick, since State.Report re-decodes the merged map every call.
type jobWatch struct {
	tasks *taskCache

	mu       sync.Mutex
	prints   map[string]float64
	filament map[filamentKey]float64

	// Transition tracking. lastState is empty until the first observation,
	// so an exporter that starts up while the printer is sitting at FINISH
	// does not count a print it never saw run.
	lastState string
	lastTitle string

	// Run-scoped: the filament seen feeding during the current job, reset
	// whenever the job title changes.
	running      filamentKey
	runningValid bool
	multi        bool

	pending *pendingCredit
}

// filamentKey identifies a spool's filament for consumption accounting.
// The RFID uid is deliberately not part of it: two spools of the same
// product should add up, not fork the total.
type filamentKey struct {
	typ, name, color string
}

// multiMaterial is the bucket for prints drawing from more than one slot.
// Crediting the whole weight to one spool would be a plausible-looking
// lie, and the report gives no per-slot breakdown to do better.
var multiMaterial = filamentKey{name: "multi-material"}

// pendingCredit is a finished print waiting for its filament weight. The
// weight only exists in the cloud task history, which lags the MQTT
// report, so the credit is applied once a fetch returns the matching job.
type pendingCredit struct {
	key   filamentKey
	title string
	since time.Time
}

// printResults is a fixed list so both series exist from startup. Without
// a zero baseline, increase() has nothing to measure the first failure
// against.
var printResults = []string{"finished", "failed"}

// terminalResult maps a gcode_state to the outcome it represents, or "".
func terminalResult(state string) string {
	switch state {
	case "FINISH":
		return "finished"
	case "FAILED":
		return "failed"
	}
	return ""
}

func newJobWatch(tasks *taskCache) *jobWatch {
	return &jobWatch{
		tasks:    tasks,
		prints:   make(map[string]float64),
		filament: make(map[filamentKey]float64),
	}
}

// run observes state transitions until done.
func (w *jobWatch) run(done <-chan struct{}, st *bambu.State, logf func(string, ...any)) {
	const every = 5 * time.Second
	for {
		select {
		case <-done:
			return
		case <-time.After(every):
		}
		if rep, ok := st.Report(); ok {
			w.observe(rep, logf)
		}
	}
}

func (w *jobWatch) observe(rep *bambu.Report, logf func(string, ...any)) {
	state := strings.ToUpper(rep.Print.GcodeState)
	if state == "" {
		state = "IDLE"
	}
	title := html.UnescapeString(rep.Print.SubtaskName)

	w.mu.Lock()
	defer w.mu.Unlock()

	// A new job wipes what was recorded for the previous one. Keying on the
	// title rather than on the state machine survives a PAUSE mid-print,
	// which would otherwise look like the end of a run.
	if title != w.lastTitle {
		w.lastTitle = title
		w.running, w.runningValid, w.multi = filamentKey{}, false, false
	}

	// Remember what is feeding while the print is live: by the time it
	// finishes, the printer may already have unloaded.
	if state == "RUNNING" {
		if slots := rep.JobSlots(); len(slots) > 1 {
			w.multi = true
		}
		if tray, _, _, ok := rep.ActiveFilament(); ok && tray.Loaded() {
			w.running = filamentKey{typ: tray.TrayType, name: tray.Name(), color: tray.TrayColor}
			w.runningValid = true
		}
	}

	w.resolvePending(logf)

	if state == w.lastState {
		return
	}
	prev := w.lastState
	w.lastState = state
	if prev == "" {
		return // first observation: adopt, do not count
	}
	result := terminalResult(state)
	if result == "" {
		return
	}
	w.prints[result]++

	// Only a completed print has a meaningful weight; a failure stopped
	// somewhere unknown, and the slicer's estimate would overstate it.
	if result != "finished" || !w.runningValid {
		return
	}
	key := w.running
	if w.multi {
		key = multiMaterial
	}
	w.pending = &pendingCredit{key: key, title: title, since: time.Now()}
	// The poller is on a five-minute interval and the task normally appears
	// within seconds of the print ending, so ask for it now.
	w.tasks.refreshSoon()
}

// resolvePending credits a finished print's filament weight once the cloud
// task history catches up. Called with w.mu held.
func (w *jobWatch) resolvePending(logf func(string, ...any)) {
	if w.pending == nil {
		return
	}
	// Ten minutes is well past the five-minute poll plus the nudge. Giving
	// up loses the grams for that print, which is the right way to fail:
	// the alternative is crediting them to whatever job happens to be
	// newest, which would be silently wrong.
	if time.Since(w.pending.since) > 10*time.Minute {
		logf("filament accounting: no cloud task matched %q, dropping its weight", w.pending.title)
		w.pending = nil
		return
	}
	t, ok := w.tasks.get()
	if !ok || t.Weight <= 0 {
		return
	}
	if html.UnescapeString(t.Title) != w.pending.title {
		return
	}
	w.filament[w.pending.key] += t.Weight
	w.pending = nil
}

// collect writes the counters. Sorted so /metrics reads the same way twice
// in a row; Prometheus does not care about sample order, humans do.
func (w *jobWatch) collect(e *exposition, printer string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, r := range printResults {
		e.counter("bambulab_prints_total",
			"Prints that reached a terminal state since the exporter started. In-process, so this resets when the pod restarts — query it with increase().",
			w.prints[r], "printer_name", printer, "result", r)
	}

	keys := make([]filamentKey, 0, len(w.filament))
	for k := range w.filament {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].color < keys[j].color
	})
	for _, k := range keys {
		e.counter("bambulab_filament_grams_total",
			"Filament consumed by completed prints, from the slicer's estimate in the cloud task history. Prints drawing from more than one slot are credited to name=\"multi-material\" rather than guessed at. Resets on restart — use increase().",
			w.filament[k], "printer_name", printer,
			"type", k.typ, "name", k.name, "color", k.color)
	}
}
