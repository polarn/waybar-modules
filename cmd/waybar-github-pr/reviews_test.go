package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseReviews(t *testing.T) {
	const body = `{"data":{"viewer":{"login":"polarn"},"search":{"nodes":[
	  {"url":"https://github.com/validio-internal/infra/pull/136","number":136,"title":"team only",
	   "author":{"login":"gnuruzzi"},"repository":{"nameWithOwner":"validio-internal/infra"},
	   "reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"Team","slug":"platform"}}]}},
	  {"url":"https://github.com/validio-internal/infra/pull/137","number":137,"title":"me and a team",
	   "author":null,"repository":{"nameWithOwner":"validio-internal/infra"},
	   "reviewRequests":{"nodes":[
	     {"requestedReviewer":{"__typename":"Team","slug":"platform"}},
	     {"requestedReviewer":{"__typename":"User","login":"Polarn"}}]}},
	  {"url":"https://github.com/someone/hobby/pull/2","number":2,"title":"personal",
	   "author":{"login":"friend"},"repository":{"nameWithOwner":"someone/hobby"},
	   "reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"polarn"}}]}},
	  {}
	]}}}`
	var resp reviewsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	work := scope{name: "work", owners: map[string]bool{"validio-internal": true}}

	got := parseReviews(resp, work)
	if len(got) != 2 {
		t.Fatalf("got %d requests, want the 2 work ones: %+v", len(got), got)
	}
	if want := []string{"platform"}; !reflect.DeepEqual(got[0].Teams, want) {
		t.Errorf("team request: Teams = %v, want %v", got[0].Teams, want)
	}
	if got[1].Teams != nil {
		t.Errorf("direct request: Teams = %v, want none", got[1].Teams)
	}

	if s := got[0].Suffix(); s != " · gnuruzzi · for platform" {
		t.Errorf("team suffix = %q", s)
	}
	if s := got[1].Suffix(); s != "" {
		t.Errorf("direct request from a ghost author: suffix = %q, want empty", s)
	}

	if got := parseReviews(resp, scope{name: "personal", owners: work.owners}); len(got) != 1 || got[0].Number != 2 {
		t.Errorf("personal scope kept %+v, want only someone/hobby#2", got)
	}
}

func TestPickerReviews(t *testing.T) {
	r := ReviewRequest{Title: "add template", URL: "https://github.com/o/r/pull/9", Number: 9,
		Repo: "o/r", Author: "gnuruzzi", Teams: []string{"platform"}}
	n := Notification{ID: "1", Reason: "review_requested"}
	n.Subject.URL = "https://api.github.com/repos/o/r/pulls/9"

	items := pickerItems(PRCache{Reviews: []ReviewRequest{r}, Notifications: []Notification{n}})
	if len(items) != 1 {
		t.Fatalf("got %d rows, want the review row alone", len(items))
	}
	if !strings.Contains(items[0].label, "[o/r#9] add template · gnuruzzi · for platform") {
		t.Errorf("review row %q is missing its head or suffix", items[0].label)
	}
	if !strings.HasSuffix(items[0].label, "review_requested") {
		t.Errorf("review row %q does not carry the notification reason", items[0].label)
	}
	if items[0].url != r.URL {
		t.Errorf("review row opens %q, want %q", items[0].url, r.URL)
	}
}

func TestStaleReviewRequest(t *testing.T) {
	notif := func(reason, pull string) Notification {
		var n Notification
		n.Reason = reason
		n.Subject.URL = "https://api.github.com/repos/validio-internal/redis/pulls/" + pull
		return n
	}
	listed := reviewsResult{Complete: true, Requests: []ReviewRequest{
		{URL: "https://github.com/validio-internal/redis/pull/8"},
	}}

	cases := []struct {
		name    string
		reviews reviewsResult
		n       Notification
		want    bool
	}{
		{"still requested", listed, notif("review_requested", "8"), false},
		{"already reviewed", listed, notif("review_requested", "7"), true},
		{"search failed", reviewsResult{}, notif("review_requested", "7"), false},
		{"other reason", listed, notif("mention", "7"), false},
	}
	for _, c := range cases {
		if got := c.reviews.stale(c.n); got != c.want {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.want)
		}
	}
}
