package main

import (
	"encoding/json"
	"fmt"
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

func TestPickerDependabot(t *testing.T) {
	human := ReviewRequest{Title: "add template", URL: "https://github.com/o/r/pull/9", Number: 9,
		Repo: "o/r", Author: "gnuruzzi"}
	bot := ReviewRequest{Title: "bump boto3", URL: "https://github.com/o/r/pull/10", Number: 10,
		Repo: "o/r", Author: "dependabot", Teams: []string{"platform"}}

	items := pickerItems(PRCache{Reviews: []ReviewRequest{bot, human}})
	if len(items) != 3 {
		t.Fatalf("got %d rows, want review, divider, dependabot: %+v", len(items), items)
	}
	if !strings.HasPrefix(items[0].label, glyphReview) || items[0].url != human.URL {
		t.Errorf("first row %q should be the human review", items[0].label)
	}
	if !items[1].divider || !strings.Contains(items[1].label, "Dependabot") {
		t.Errorf("second row %q should be the Dependabot divider", items[1].label)
	}
	if !strings.HasPrefix(items[2].label, glyphDependabot) || items[2].url != bot.URL {
		t.Errorf("third row %q should be the dependabot PR", items[2].label)
	}
	if !strings.HasSuffix(items[2].label, "bump boto3 · for platform") {
		t.Errorf("dependabot row %q should drop the author but keep the team", items[2].label)
	}
}

func TestPickerRenovate(t *testing.T) {
	dep := ReviewRequest{Title: "bump boto3", URL: "https://github.com/o/r/pull/10", Number: 10,
		Repo: "o/r", Author: "dependabot"}
	hosted := ReviewRequest{Title: "update docker", URL: "https://github.com/o/r/pull/11", Number: 11,
		Repo: "o/r", Author: "renovate"}
	self := ReviewRequest{Title: "update redis", URL: "https://github.com/o/r/pull/12", Number: 12,
		Repo: "o/r", Author: "validio-renovate", Teams: []string{"platform"}}

	items := pickerItems(PRCache{Reviews: []ReviewRequest{self, dep, hosted}})
	if len(items) != 4 {
		t.Fatalf("got %d rows, want dependabot, divider, two renovate: %+v", len(items), items)
	}
	if !strings.HasPrefix(items[0].label, glyphDependabot) || items[0].url != dep.URL {
		t.Errorf("first row %q should be the dependabot PR", items[0].label)
	}
	if !items[1].divider || !strings.Contains(items[1].label, "Renovate") {
		t.Errorf("second row %q should be the Renovate divider", items[1].label)
	}
	for i, want := range []ReviewRequest{self, hosted} {
		if got := items[2+i]; !strings.HasPrefix(got.label, glyphRenovate) || got.url != want.URL {
			t.Errorf("row %d %q should be renovate PR %s", 2+i, got.label, want.URL)
		}
	}
	if !strings.HasSuffix(items[2].label, "update redis · for platform") {
		t.Errorf("renovate row %q should drop the author but keep the team", items[2].label)
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

func TestPickerGroupsReviews(t *testing.T) {
	req := func(repo string, n int, author, title string) ReviewRequest {
		return ReviewRequest{Title: title, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, n),
			Number: n, Repo: repo, Author: author}
	}
	reqs := []ReviewRequest{
		req("o/ui", 119, "ed", "feat: [UI-1] [UI-2] count failures"),
		req("o/flux", 56, "jb", "ci: [PL-4797] Auto-merge approved Renovate PRs"),
		req("o/releaser", 45, "jb", "ci: [PL-4798] Auto-merge approved Renovate PRs"),
		req("o/images", 220, "jb", "ci: [PL-5215] Start a Renovate run"),
		req("o/ui", 112, "ed", "fix: light mode"),
		req("o/dev-tools", 221, "jb", "ci: [PL-4798] Auto-merge approved Renovate PRs"),
		req("o/handbook", 112, "ed", "UI-8434 Decommission the key"),
		req("o/ui", 113, "ed", "fix: deprecations link"),
		req("o/images", 219, "dependabot", "chore(deps): bump"),
		req("o/docs", 1, "jb", "Start a Renovate run"),
		req("o/docs", 2, "jb", "Start a Renovate run"),
	}

	var got []string
	for _, it := range pickerItems(PRCache{Reviews: reqs}) {
		got = append(got, it.label)
	}
	want := []string{
		"[o/images#220] ci: [PL-5215] Start a Renovate run · jb",
		"[o/handbook#112] UI-8434 Decommission the key · ed",
		"[o/docs#1] Start a Renovate run · jb",
		"[o/docs#2] Start a Renovate run · jb",
		"──── ui · 3 ",
		"#112 fix: light mode · ed",
		"#113 fix: deprecations link · ed",
		"#119 feat: [UI-1] [UI-2] count failures · ed",
		"──── ci: Auto-merge approved Renovate PRs · 3 repos ",
		"[o/dev-tools#221] PL-4798 · jb",
		"[o/flux#56] PL-4797 · jb",
		"[o/releaser#45] PL-4798 · jb",
		"──── Dependabot ",
	}
	if len(got) != len(want)+1 {
		t.Fatalf("got %d rows, want %d:\n%s", len(got), len(want)+1, strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("row %d = %q, want it to contain %q", i, got[i], w)
		}
	}
}

func TestSplitTickets(t *testing.T) {
	cases := []struct{ title, rest, tickets string }{
		{"ci: [PL-4798] Auto-merge approved Renovate PRs", "ci: Auto-merge approved Renovate PRs", "PL-4798"},
		{"UI-8434 Decommission the key", "Decommission the key", "UI-8434"},
		{"feat: [UI-1] [UI-2] count", "feat: count", "UI-1 UI-2"},
		{"Add CODEOWNERS", "Add CODEOWNERS", ""},
	}
	for _, c := range cases {
		if rest, tickets := splitTickets(c.title); rest != c.rest || tickets != c.tickets {
			t.Errorf("splitTickets(%q) = %q, %q, want %q, %q", c.title, rest, tickets, c.rest, c.tickets)
		}
	}
}
