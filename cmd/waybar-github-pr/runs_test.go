package main

import (
	"testing"
	"time"
)

// run builds a listing entry the way the API returns one: created_at orders
// attempts, updated_at is when it ended.
func run(id int64, title, status, conclusion string, created, updated time.Time) Run {
	return Run{
		ID: id, Name: title, DisplayTitle: title,
		Status: status, Conclusion: conclusion,
		CreatedAt: created.Format(time.RFC3339),
		UpdatedAt: updated.Format(time.RFC3339),
	}
}

func ids(runs []Run) []int64 {
	out := make([]int64, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

func same(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Modelled on a real validio-internal/infra listing: monitoring-prod failed
// twice, then a later run of it passed, while both failures were still being
// shown. The passing run is what should retire them.
func TestAttentionRunsSupersedes(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	const (
		mon = "terraform apply google/monitoring-prod (@polarn)"
		gws = "terraform apply googleworkspace (@polarn)"
		azp = "terraform apply azure/prod (@polarn)"
	)

	listing := []Run{
		run(1, azp, "in_progress", "", ago(2*time.Minute), ago(time.Minute)),
		run(2, mon, "completed", "success", ago(90*time.Minute), ago(80*time.Minute)),
		// Finished later than the run above it despite starting earlier —
		// the listing really does come back like this, which is why
		// supersession compares timestamps instead of trusting the order.
		run(3, gws, "completed", "success", ago(150*time.Minute), ago(75*time.Minute)),
		run(4, mon, "completed", "failure", ago(3*time.Hour), ago(170*time.Minute)),
		run(5, mon, "completed", "failure", ago(25*time.Hour), ago(24*time.Hour)),
	}

	got := ids(attentionRuns("o/r", listing, nil, 72*time.Hour))
	if want := []int64{1}; !same(got, want) {
		t.Errorf("got runs %v, want %v — the 11:52-style success should retire both failures", got, want)
	}
}

func TestAttentionRunsKeepsAnUnsupersededFailure(t *testing.T) {
	now := time.Now()
	const mon = "terraform apply google/monitoring-prod (@polarn)"

	listing := []Run{
		// Newer, but a different thing, so it supersedes nothing.
		run(1, "terraform apply github (@polarn)", "completed", "success",
			now.Add(-time.Hour), now.Add(-50*time.Minute)),
		run(2, mon, "completed", "failure", now.Add(-3*time.Hour), now.Add(-170*time.Minute)),
	}

	got := attentionRuns("o/r", listing, nil, 72*time.Hour)
	if want := []int64{2}; !same(ids(got), want) {
		t.Fatalf("got runs %v, want %v", ids(got), want)
	}
	if !got[0].Failed || got[0].Repo != "o/r" {
		t.Errorf("run not marked up: Failed=%v Repo=%q", got[0].Failed, got[0].Repo)
	}
}

// An older attempt must not retire a newer failure, whichever way round the
// listing happens to arrive.
func TestAttentionRunsOlderSuccessDoesNotSupersede(t *testing.T) {
	now := time.Now()
	const mon = "terraform apply google/monitoring-prod (@polarn)"

	listing := []Run{
		run(1, mon, "completed", "failure", now.Add(-time.Hour), now.Add(-50*time.Minute)),
		run(2, mon, "completed", "success", now.Add(-5*time.Hour), now.Add(-4*time.Hour)),
	}
	if got := ids(attentionRuns("o/r", listing, nil, 72*time.Hour)); !same(got, []int64{1}) {
		t.Errorf("got %v, want the newer failure to survive an older success", got)
	}
}

func TestAttentionRunsTTL(t *testing.T) {
	now := time.Now()
	const mon = "terraform apply google/monitoring-prod (@polarn)"
	stale := run(1, mon, "completed", "failure", now.Add(-100*time.Hour), now.Add(-99*time.Hour))

	if got := attentionRuns("o/r", []Run{stale}, nil, 72*time.Hour); len(got) != 0 {
		t.Errorf("a 99h-old failure survived a 72h ttl: %v", ids(got))
	}
	// A Friday-evening failure must still be there on Monday morning.
	friday := run(2, mon, "completed", "failure", now.Add(-64*time.Hour), now.Add(-63*time.Hour))
	if got := attentionRuns("o/r", []Run{friday}, nil, 72*time.Hour); len(got) != 1 {
		t.Errorf("a 63h-old failure was expired by a 72h ttl")
	}
	// 0 restores the old behaviour: it sticks until dismissed.
	if got := attentionRuns("o/r", []Run{stale}, nil, 0); len(got) != 1 {
		t.Errorf("ttl 0 expired a failure anyway: %v", ids(got))
	}
}

func TestAttentionRunsDismissal(t *testing.T) {
	now := time.Now()
	const mon = "terraform apply google/monitoring-prod (@polarn)"
	listing := []Run{run(7, mon, "completed", "failure", now.Add(-time.Hour), now.Add(-50*time.Minute))}

	if got := attentionRuns("o/r", listing, map[int64]bool{7: true}, 72*time.Hour); len(got) != 0 {
		t.Errorf("a dismissed failure came back: %v", ids(got))
	}
}

// Cancelled runs are not failures, but they are still a later attempt.
func TestAttentionRunsCancelledStillSupersedes(t *testing.T) {
	now := time.Now()
	const mon = "terraform apply google/monitoring-prod (@polarn)"
	listing := []Run{
		run(1, mon, "completed", "cancelled", now.Add(-time.Hour), now.Add(-50*time.Minute)),
		run(2, mon, "completed", "failure", now.Add(-3*time.Hour), now.Add(-170*time.Minute)),
	}
	if got := attentionRuns("o/r", listing, nil, 72*time.Hour); len(got) != 0 {
		t.Errorf("got %v, want the cancelled retry to retire the older failure", ids(got))
	}
}
