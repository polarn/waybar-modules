package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHumanAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		when time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(time.Minute), "now"}, // clock skew must not print "-1m"
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-90 * time.Minute), "1h"},
		{now.Add(-47 * time.Hour), "1d"},
		{now.Add(-30 * 24 * time.Hour), "30d"},
	}
	for _, c := range cases {
		if got := humanAge(c.when); got != c.want {
			t.Errorf("humanAge(%s) = %q, want %q", c.when, got, c.want)
		}
	}
}

// Divider placement is checked here for the same reason it exists: the row
// fuzzel opens with must be actionable.
func TestPickerItems(t *testing.T) {
	pr := PR{Title: "one", URL: "https://github.com/o/r/pull/1",
		CreatedAt: time.Now().Add(-3 * time.Hour).Format(time.RFC3339)}
	pr.Repository.NameWithOwner = "o/r"

	t.Run("one section takes no divider", func(t *testing.T) {
		items := pickerItems(PRCache{All: []PR{pr, pr}})
		for _, it := range items {
			if it.divider {
				t.Fatalf("divider in a single-section list: %q", it.label)
			}
		}
	})

	t.Run("groups are divided and the first row acts", func(t *testing.T) {
		cache := PRCache{
			All:     []PR{pr},
			Runs:    []Run{{ID: 7, Repo: "o/r", DisplayTitle: "build", Status: "in_progress"}},
			Pending: []PendingRoot{{Root: "google/x", Repo: "o/r", Commits: 2}},
			Notifications: []Notification{{
				ID: "1", Reason: "review_requested", UpdatedAt: time.Now().Format(time.RFC3339),
			}},
		}
		items := pickerItems(cache)

		if items[0].divider {
			t.Error("first row is a divider; Enter on fuzzel's default selection would do nothing")
		}
		var dividers int
		for _, it := range items {
			if it.divider {
				dividers++
			}
		}
		if dividers != 3 {
			t.Errorf("got %d dividers across 4 groups, want 3", dividers)
		}
	})

	t.Run("a notification on my own PR annotates its row", func(t *testing.T) {
		items := pickerItems(PRCache{
			All: []PR{pr},
			Notifications: []Notification{{
				ID: "1", Reason: "author",
				Subject: struct {
					Title string `json:"title"`
					Type  string `json:"type"`
					URL   string `json:"url"`
				}{URL: "https://api.github.com/repos/o/r/pulls/1"},
			}},
		})
		if len(items) != 1 {
			t.Fatalf("got %d rows, want the PR row alone", len(items))
		}
		if !strings.HasSuffix(items[0].label, "author") {
			t.Errorf("PR row %q does not carry the notification reason", items[0].label)
		}
	})
}

func TestDividerFitsPickerWidth(t *testing.T) {
	for _, title := range []string{"Workflow runs", "Needs terraform apply", strings.Repeat("x", 200)} {
		if n := len([]rune(divider(title))); n > pickerWidth {
			t.Errorf("divider(%.20q) is %d chars, wider than the %d-char window", title, n, pickerWidth)
		}
	}
}

// The same fix backported to two release branches is two PRs with one title.
// Rows for those must still name which is which — the number always, and the
// base branch whenever it is not the repository default.
func TestPRHead(t *testing.T) {
	mk := func(n int, base string) PR {
		pr := PR{Number: n, Base: base}
		pr.Repository.NameWithOwner = "validio-internal/docs"
		return pr
	}
	cases := []struct {
		pr   PR
		want string
	}{
		{mk(142, "v10.3"), "[validio-internal/docs#142 → v10.3] "},
		{mk(141, "v10.2"), "[validio-internal/docs#141 → v10.2] "},
		// Base empty: it matched the default, so there is nothing to say.
		{mk(64, ""), "[validio-internal/docs#64] "},
		// A cache written by an older build carries no number.
		{mk(0, ""), "[validio-internal/docs] "},
	}
	for _, c := range cases {
		if got := prHead(c.pr); got != c.want {
			t.Errorf("prHead(#%d, %q) = %q, want %q", c.pr.Number, c.pr.Base, got, c.want)
		}
	}
}

// Selection goes by index, so two rows that render identically still resolve
// to the PR they were built from. This is the case that used to open the
// wrong one.
func TestResolve(t *testing.T) {
	at := time.Date(2026, 9, 9, 14, 9, 29, 0, time.UTC).Format(time.RFC3339)
	mk := func(url string) PR {
		pr := PR{Title: "[PRO-1226] docs: Correct CPU recommendations", URL: url, CreatedAt: at}
		pr.Repository.NameWithOwner = "validio-internal/docs"
		return pr
	}
	// No numbers and no base, so the two labels really are identical.
	items := pickerItems(PRCache{All: []PR{mk("u/141"), mk("u/142")}})
	if items[0].label != items[1].label {
		t.Fatalf("test no longer exercises a collision: %q vs %q", items[0].label, items[1].label)
	}
	for i, want := range []string{"u/141", "u/142"} {
		got, ok := resolve(items, fmt.Sprintf("%d\n", i))
		if !ok || got.url != want {
			t.Errorf("resolve(%d) = (%q, %v), want %q", i, got.url, ok, want)
		}
	}

	// Anything that is not an index in range means nothing was chosen.
	for _, out := range []string{"", "  ", "-1", "2", "99", "some typed text", "1.5"} {
		if _, ok := resolve(items, out); ok {
			t.Errorf("resolve(%q) reported a selection", out)
		}
	}
}
