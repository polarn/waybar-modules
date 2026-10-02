package main

import (
	"strings"
	"testing"
)

func TestScopeKeeps(t *testing.T) {
	owners := map[string]bool{"validio-internal": true, "validio-io": true}
	cases := []struct {
		scope string
		repo  string
		want  bool
	}{
		{"", "validio-internal/infra", true},
		{"", "polarn/flux", true},
		{"work", "validio-internal/infra", true},
		{"work", "Validio-IO/platform", true},
		{"work", "polarn/flux", false},
		{"work", "validio-internal-fork/x", false},
		{"personal", "polarn/flux", true},
		{"personal", "golang/go", true},
		{"personal", "validio-io/platform", false},
	}
	for _, c := range cases {
		if got := (scope{name: c.scope, owners: owners}).keeps(c.repo); got != c.want {
			t.Errorf("scope %q keeps(%q) = %v, want %v", c.scope, c.repo, got, c.want)
		}
	}
}

// A second comment on a thread that is still unread keeps its ID and moves its
// updated_at, and has to pop up again; a poll with nothing new must not.
func TestToAnnounce(t *testing.T) {
	seenNotifs = make(map[string]string)
	notifsBaselined = false
	at := func(id, when string) Notification { return Notification{ID: id, UpdatedAt: when} }

	if got := toAnnounce([]Notification{at("1", "t1")}); len(got) != 0 {
		t.Fatalf("baseline poll announced %d, want 0", len(got))
	}
	steps := []struct {
		name string
		poll []Notification
		want []string
	}{
		{"unchanged", []Notification{at("1", "t1")}, nil},
		{"new comment on an unread thread", []Notification{at("1", "t2")}, []string{"1"}},
		{"same update seen again", []Notification{at("1", "t2")}, nil},
		{"new thread", []Notification{at("1", "t2"), at("2", "t1")}, []string{"2"}},
	}
	for _, s := range steps {
		var ids []string
		for _, n := range toAnnounce(s.poll) {
			ids = append(ids, n.ID)
		}
		if strings.Join(ids, ",") != strings.Join(s.want, ",") {
			t.Errorf("%s: announced %v, want %v", s.name, ids, s.want)
		}
	}
}

func TestFoldNotifications(t *testing.T) {
	mine := PR{URL: "https://github.com/o/r/pull/1"}
	review := ReviewRequest{URL: "https://github.com/o/r/pull/2"}
	on := func(id, reason, api string) Notification {
		n := Notification{ID: id, Reason: reason}
		n.Subject.URL = api
		return n
	}
	reasons, loose := foldNotifications([]PR{mine}, []ReviewRequest{review}, []Notification{
		on("a", "author", "https://api.github.com/repos/o/r/pulls/1"),
		on("b", "review_requested", "https://api.github.com/repos/o/r/pulls/2"),
		on("c", "mention", "https://api.github.com/repos/o/other/issues/9"),
	})
	if !strings.HasSuffix(reasons[mine.URL], "author") {
		t.Errorf("own PR reason = %q, want it to end with author", reasons[mine.URL])
	}
	if !strings.HasSuffix(reasons[review.URL], "review_requested") {
		t.Errorf("review request reason = %q, want it to end with review_requested", reasons[review.URL])
	}
	if len(loose) != 1 || loose[0].ID != "c" {
		t.Errorf("loose = %v, want only the mention elsewhere", loose)
	}
}
