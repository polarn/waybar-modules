package main

import (
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

// The picker resolves a fuzzel selection by matching the returned line
// against the labels it fed in, so a duplicate label would act on the wrong
// row. Divider placement is checked here for the same reason it exists: the
// row fuzzel opens with must be actionable.
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
		seen := make(map[string]bool)
		for _, it := range items {
			if it.divider {
				dividers++
			}
			if seen[it.label] {
				t.Errorf("duplicate label %q — selection would resolve to the wrong row", it.label)
			}
			seen[it.label] = true
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
