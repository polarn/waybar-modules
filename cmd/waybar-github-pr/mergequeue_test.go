package main

import (
	"strings"
	"testing"
	"time"
)

// The reasons here are the ones this estate actually produces, taken from the
// RemovedFromMergeQueueEvent history across the migrated repos.
func TestDequeueRejection(t *testing.T) {
	cases := map[string]bool{
		"merged":                        false, // the happy path
		"manual":                        false, // withdrawn on purpose
		"":                              false, // never enqueued
		"failed_checks":                 true,
		"merge_conflict":                true,
		"checks_timed_out":              true,
		"some_future_thing_github_adds": true, // unknown must surface, not vanish
	}
	for reason, want := range cases {
		if got := dequeueRejection(reason); got != want {
			t.Errorf("dequeueRejection(%q) = %v, want %v", reason, got, want)
		}
	}
}

func TestHumanReason(t *testing.T) {
	cases := map[string]string{
		"failed_checks":    "CI failed",
		"checks_timed_out": "CI timed out",
		"merge_conflict":   "merge conflict", // unmapped: underscores opened up
		"queue_cleared":    "queue cleared",
	}
	for reason, want := range cases {
		if got := humanReason(reason); got != want {
			t.Errorf("humanReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestQueueStateSummary(t *testing.T) {
	t.Run("queued shows position and state", func(t *testing.T) {
		q := QueueState{InQueue: true, State: "AWAITING_CHECKS", Position: 2, Total: 3}
		if got, want := q.Summary(), glyphQueue+" #2/3 awaiting checks"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("queued without a queue length omits the total", func(t *testing.T) {
		q := QueueState{InQueue: true, State: "QUEUED", Position: 1}
		if got, want := q.Summary(), glyphQueue+" #1 queued"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("refused shows the reason and how long ago", func(t *testing.T) {
		q := QueueState{
			RejectedReason: "failed_checks",
			RejectedAt:     time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		}
		if got, want := q.Summary(), glyphDequeued+" CI failed (3h)"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("nothing to say", func(t *testing.T) {
		if got := (QueueState{}).Summary(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	// A PR back in the queue is in flight again, so its position is what
	// matters — never a refusal it has already moved past.
	t.Run("queued wins over an old refusal", func(t *testing.T) {
		q := QueueState{InQueue: true, State: "MERGEABLE", Position: 1, Total: 1,
			RejectedReason: "failed_checks"}
		if got := q.Summary(); !strings.HasPrefix(got, glyphQueue) {
			t.Errorf("got %q, want the queue glyph to lead", got)
		}
	})
}

// An incomplete poll must leave the PRs alone rather than asserting that
// nothing is queued: the pill omits the segment in that case, and an
// annotation would contradict it.
func TestAnnotateQueueIgnoresIncomplete(t *testing.T) {
	prs := []PR{{URL: "u1"}}
	annotatePRs(prs, prFacts{
		Queue:    map[string]QueueState{"u1": {InQueue: true, Position: 1}},
		Complete: false,
	})
	if prs[0].Queue != nil {
		t.Errorf("annotated from an incomplete result: %+v", prs[0].Queue)
	}
}

func TestAnnotateQueueAndSplit(t *testing.T) {
	prs := []PR{{URL: "queued"}, {URL: "refused"}, {URL: "quiet"}, {URL: "absent"}}
	annotatePRs(prs, prFacts{Complete: true, Queue: map[string]QueueState{
		"queued":  {InQueue: true, Position: 1, Total: 2, State: "QUEUED"},
		"refused": {RejectedReason: "failed_checks", RejectedAt: time.Now().Format(time.RFC3339)},
		// Neither queued nor refused: no annotation, so the row stays as
		// short as it was before any of this existed.
		"quiet": {},
	}})

	if prs[0].Queue == nil || !prs[0].Queue.InQueue {
		t.Error("queued PR was not annotated")
	}
	if prs[1].Queue == nil || !prs[1].Queue.Rejected() {
		t.Error("refused PR was not annotated")
	}
	for _, i := range []int{2, 3} {
		if prs[i].Queue != nil {
			t.Errorf("PR %q annotated with %+v, want nil", prs[i].URL, prs[i].Queue)
		}
	}

	if in, out := splitQueue(prs); in != 1 || out != 1 {
		t.Errorf("splitQueue = (%d, %d), want (1, 1)", in, out)
	}
}

// notify-send must not fire for refusals already on record when the daemon
// starts — make install kills this process, and a restart that re-announced
// the backlog would be worse than useless.
func TestNotifyRejectionsBaselines(t *testing.T) {
	t.Cleanup(func() {
		seenRejections = make(map[string]bool)
		rejectionsBaselined = false
	})
	seenRejections = make(map[string]bool)
	rejectionsBaselined = false

	at := time.Now().Format(time.RFC3339)
	notifyRejections([]PR{
		{URL: "u1", Queue: &QueueState{RejectedReason: "failed_checks", RejectedAt: at}},
	}, false)
	if !rejectionsBaselined {
		t.Fatal("first poll did not baseline")
	}
	if !seenRejections["u1@"+at] {
		t.Error("the pre-existing refusal was not baselined as seen")
	}

	// A second refusal of the same PR is a distinct event and must register.
	later := time.Now().Add(time.Minute).Format(time.RFC3339)
	notifyRejections([]PR{
		{URL: "u1", Queue: &QueueState{RejectedReason: "failed_checks", RejectedAt: later}},
	}, false)
	if !seenRejections["u1@"+later] {
		t.Error("a re-refusal of the same PR was not recorded")
	}
	// The superseded event is dropped so the map cannot grow without bound.
	if seenRejections["u1@"+at] {
		t.Error("the superseded refusal was not forgotten")
	}
}
