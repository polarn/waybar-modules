package main

import (
	"fmt"
	"log"
	"strings"
)

// Glyphs for the two merge-queue states. Named rather than inlined because
// each is written once here and read by the pill, the tooltip and the picker.
const (
	// nf-oct-git_merge_queue — GitHub's own merge-queue icon.
	glyphQueue = ""
	// nf-md-close_octagon — a merge that was turned away. Deliberately not
	// the alert_circle the pill uses for a failed run: a bounced merge and a
	// broken apply are independent states and both can be on the pill at
	// once, so they must not share a glyph.
	glyphDequeued = "󰅜"
)

// QueueState is what we know about one PR's relationship to its repository's
// merge queue. On a queue-protected branch, merging a PR only enqueues it —
// the merge happens later, behind a fresh CI run against the queued commit,
// and can be refused. So "I merged it" is not the end of the story, which is
// the whole reason this exists.
type QueueState struct {
	InQueue    bool   `json:"in_queue"`
	State      string `json:"state,omitempty"`    // QUEUED | AWAITING_CHECKS | MERGEABLE | UNMERGEABLE | LOCKED
	Position   int    `json:"position,omitempty"` // 1-based, within its own repo's queue
	Total      int    `json:"total,omitempty"`
	EnqueuedAt string `json:"enqueued_at,omitempty"`

	// The PR's latest exit from the queue, set only when that exit was a
	// refusal and the PR has not gone back in since. A PR that bounced, got
	// fixed and was re-enqueued is in flight again, so its old rejection is
	// history and must stop being flagged.
	RejectedReason string `json:"rejected_reason,omitempty"`
	RejectedAt     string `json:"rejected_at,omitempty"`
}

func (q QueueState) Rejected() bool { return q.RejectedReason != "" }

// Summary renders the state as the tail of a picker row or tooltip line.
// Empty for a PR that is neither queued nor recently refused, so the caller
// can append it unconditionally.
func (q QueueState) Summary() string {
	switch {
	case q.InQueue:
		s := glyphQueue
		switch {
		case q.Position > 0 && q.Total > 0:
			s += fmt.Sprintf(" #%d/%d", q.Position, q.Total)
		case q.Position > 0:
			s += fmt.Sprintf(" #%d", q.Position)
		}
		if q.State != "" {
			s += " " + humanQueueState(q.State)
		}
		return s
	case q.Rejected():
		s := glyphDequeued + " " + humanReason(q.RejectedReason)
		if age := humanAge(parseGHTime(q.RejectedAt)); age != "" {
			s += " (" + age + ")"
		}
		return s
	}
	return ""
}

// humanQueueState renders a MergeQueueEntryState for a menu row. The enum is
// SCREAMING_SNAKE_CASE and its members read fine as words, so this needs no
// table: QUEUED, AWAITING_CHECKS, MERGEABLE, UNMERGEABLE, LOCKED.
func humanQueueState(state string) string {
	return strings.ToLower(strings.ReplaceAll(state, "_", " "))
}

// humanReason renders a removal reason. Only the ones that do not read well
// on their own are mapped; anything else — including a reason GitHub adds
// later — passes through with its underscores opened up, rather than being
// folded into a guess.
func humanReason(reason string) string {
	switch reason {
	case "failed_checks":
		return "CI failed"
	case "checks_timed_out":
		return "CI timed out"
	}
	return strings.ReplaceAll(reason, "_", " ")
}

// dequeueRejection reports whether a removal reason means the merge was
// turned away, as opposed to completed or withdrawn. The reasons this estate
// actually produces, by frequency: merged, failed_checks, merge_conflict,
// manual, checks_timed_out.
//
// "merged" is the happy path and "manual" is a deliberate withdrawal, so
// neither is worth flagging. Everything else counts, unknown values
// included — a reason we do not recognise should surface as something to
// look at rather than disappear.
func dequeueRejection(reason string) bool {
	switch reason {
	case "", "merged", "manual":
		return false
	}
	return true
}

// prFacts carries what one GraphQL call resolves about my open PRs: the
// things `gh search prs` cannot answer, keyed by PR URL. Complete is false
// when the query failed or came back partial, in which case the caller
// leaves the PRs un-annotated and omits the queue segments — the same rule
// the runs poll follows, for the same reason: an absent value must not
// become a confident zero.
type prFacts struct {
	Queue map[string]QueueState
	// Base branch, set only when it is not the repository's default. A
	// backport onto v10.2 needs saying; a PR onto main does not, and
	// labelling every row with it would be noise.
	Base     map[string]string
	Complete bool
}

// factsQuery asks, in one request, for every open PR of mine: whether it sits
// in a merge queue and where, its most recent removal from one, and the base
// branch it targets.
//
// Reading the removal event rather than diffing successive polls is what lets
// a refusal survive a daemon restart — `make install` kills this process, and
// a bounced merge must not be forgotten because of that.
//
// `gh search prs` cannot answer any of it: its --json field set has no
// merge-queue member, hence GraphQL. first: 50 is well clear of the handful
// of PRs one person has open, and matches the search fetchPRs runs.
const factsQuery = `
{
  search(query: "is:open is:pr author:@me", type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        url
        baseRefName
        repository { defaultBranchRef { name } }
        isInMergeQueue
        mergeQueueEntry {
          state
          position
          enqueuedAt
          mergeQueue { entries { totalCount } }
        }
        timelineItems(last: 1, itemTypes: [REMOVED_FROM_MERGE_QUEUE_EVENT]) {
          nodes { ... on RemovedFromMergeQueueEvent { createdAt reason } }
        }
      }
    }
  }
}`

// fetchPRFacts resolves the facts the PR search cannot report, keyed by PR
// URL so the caller can join them onto the list `gh search prs` returned.
func fetchPRFacts() prFacts {
	var resp struct {
		Data struct {
			Search struct {
				Nodes []struct {
					URL         string `json:"url"`
					BaseRefName string `json:"baseRefName"`
					Repository  struct {
						DefaultBranchRef *struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					} `json:"repository"`
					IsInMergeQueue  bool `json:"isInMergeQueue"`
					MergeQueueEntry *struct {
						State      string `json:"state"`
						Position   int    `json:"position"`
						EnqueuedAt string `json:"enqueuedAt"`
						MergeQueue *struct {
							Entries struct {
								TotalCount int `json:"totalCount"`
							} `json:"entries"`
						} `json:"mergeQueue"`
					} `json:"mergeQueueEntry"`
					TimelineItems struct {
						Nodes []struct {
							CreatedAt string `json:"createdAt"`
							Reason    string `json:"reason"`
						} `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := ghJSON(&resp, "graphql", "-f", "query="+factsQuery); err != nil {
		log.Printf("Error fetching PR facts: %s", err)
		return prFacts{}
	}
	// GraphQL answers 200 with a partial result and an errors array, so a
	// clean exit status is not proof the answer is whole.
	if len(resp.Errors) > 0 {
		log.Printf("PR facts query returned errors: %s", resp.Errors[0].Message)
		return prFacts{}
	}

	out := prFacts{
		Queue:    make(map[string]QueueState, len(resp.Data.Search.Nodes)),
		Base:     make(map[string]string),
		Complete: true,
	}
	for _, n := range resp.Data.Search.Nodes {
		if n.URL == "" {
			continue // a search hit that was not a PullRequest
		}
		// A missing defaultBranchRef means an empty repository, so there is
		// nothing to call default and the base is worth showing.
		def := ""
		if d := n.Repository.DefaultBranchRef; d != nil {
			def = d.Name
		}
		if n.BaseRefName != "" && n.BaseRefName != def {
			out.Base[n.URL] = n.BaseRefName
		}

		q := QueueState{InQueue: n.IsInMergeQueue}
		if e := n.MergeQueueEntry; e != nil {
			q.State, q.Position, q.EnqueuedAt = e.State, e.Position, e.EnqueuedAt
			if e.MergeQueue != nil {
				q.Total = e.MergeQueue.Entries.TotalCount
			}
		}
		if !q.InQueue && len(n.TimelineItems.Nodes) > 0 {
			if ev := n.TimelineItems.Nodes[0]; dequeueRejection(ev.Reason) {
				q.RejectedReason, q.RejectedAt = ev.Reason, ev.CreatedAt
			}
		}
		out.Queue[n.URL] = q
	}
	return out
}

// annotatePRs attaches the resolved facts to the PRs they belong to. The join
// is by URL because the two fetches are independent — `gh search prs` gives
// the list, GraphQL the rest — so a PR missing from either simply goes
// un-annotated rather than blocking the tick.
func annotatePRs(prs []PR, f prFacts) {
	if !f.Complete {
		return
	}
	for i := range prs {
		prs[i].Base = f.Base[prs[i].URL]
		if s, ok := f.Queue[prs[i].URL]; ok && (s.InQueue || s.Rejected()) {
			prs[i].Queue = &s
		}
	}
}

// splitQueue counts the two merge-queue states the pill shows.
func splitQueue(prs []PR) (inQueue, rejected int) {
	for _, pr := range prs {
		switch q := pr.Queue; {
		case q == nil:
		case q.InQueue:
			inQueue++
		case q.Rejected():
			rejected++
		}
	}
	return inQueue, rejected
}

// Tracks the rejections already announced. Keyed on the removal event, not
// just the PR, so a PR that bounces, is fixed, and bounces again notifies
// both times.
var (
	seenRejections      = make(map[string]bool)
	rejectionsBaselined = false
)

// notifyRejections fires once per newly-refused merge. Baselines on the first
// poll like the other notifiers, so a restart does not re-announce a backlog
// that was already sitting there.
func notifyRejections(prs []PR, enabled bool) {
	present := make(map[string]bool)
	rejected := make(map[string]PR)
	for _, pr := range prs {
		if q := pr.Queue; q != nil && q.Rejected() {
			key := pr.URL + "@" + q.RejectedAt
			present[key] = true
			rejected[key] = pr
		}
	}

	if !rejectionsBaselined {
		seenRejections = present
		rejectionsBaselined = true
		return
	}

	for key, pr := range rejected {
		if seenRejections[key] {
			continue
		}
		seenRejections[key] = true
		if !enabled {
			continue
		}
		notifySend("Merge queue refused a PR", fmt.Sprintf("[%s] %s · %s",
			pr.Repository.NameWithOwner, pr.Title, humanReason(pr.Queue.RejectedReason)))
	}

	for key := range seenRejections {
		if !present[key] {
			delete(seenRejections, key)
		}
	}
}
