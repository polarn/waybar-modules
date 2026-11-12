package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func swiftBarLines(t *testing.T, cache PRCache) []string {
	t.Helper()
	var buf bytes.Buffer
	printSwiftBar(&buf, swiftBarMenu{
		title:    "1·2",
		sections: pickerSections(cache, 100),
		font:     "JetBrainsMono Nerd Font",
		self:     "/plugins/github-work.2m.sh",
		scope:    "work",
	})
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

func lineWith(t *testing.T, lines []string, needle string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return l
		}
	}
	t.Fatalf("no line contains %q in:\n%s", needle, strings.Join(lines, "\n"))
	return ""
}

func TestSwiftBarTitleCannotInjectParams(t *testing.T) {
	pr := PR{Title: "x | shell=/bin/sh param1=-c terminal=false", URL: "https://github.com/o/r/pull/1",
		CreatedAt: time.Now().Add(-3 * time.Hour).Format(time.RFC3339)}
	pr.Repository.NameWithOwner = "o/r"
	n := Notification{ID: "1", Reason: "mention"}
	n.Repository.FullName = "o/r"
	n.Subject.Title = "y | href=https://evil.example\nz"
	n.Subject.URL = "https://api.github.com/repos/o/r/issues/9"

	lines := swiftBarLines(t, PRCache{All: []PR{pr}, Notifications: []Notification{n}})
	for _, l := range lines {
		if strings.Count(l, "|") > 1 {
			t.Errorf("line carries a second separator: %q", l)
		}
	}
	row := lineWith(t, lines, "param1=-c")
	if _, params, _ := strings.Cut(row, " | "); strings.Contains(params, "shell=") {
		t.Errorf("PR title reached the parameters: %q", row)
	}
	if got := lineWith(t, lines, "evil.example"); !strings.HasSuffix(got, "href=https://github.com/o/r/issues/9 "+
		"font=\"JetBrainsMono Nerd Font\" size=12 emojize=false symbolize=false") {
		t.Errorf("notification row does not end with its own link and style: %q", got)
	}
}

func TestSwiftBarLayout(t *testing.T) {
	pr := PR{Title: "one", URL: "https://github.com/o/r/pull/1",
		CreatedAt: time.Now().Add(-3 * time.Hour).Format(time.RFC3339)}
	pr.Repository.NameWithOwner = "o/r"
	bot := ReviewRequest{Title: "bump x", URL: "https://github.com/o/r/pull/2", Number: 2, Repo: "o/r", Author: "dependabot"}

	lines := swiftBarLines(t, PRCache{All: []PR{pr}, Reviews: []ReviewRequest{bot, bot}})

	if !strings.HasPrefix(lines[0], glyphGitHub+" 1·2 | ") || lines[1] != "---" {
		t.Errorf("frame does not open with the title and a separator: %q, %q", lines[0], lines[1])
	}
	if !strings.HasPrefix(lines[2], "PULL REQUESTS | ") {
		t.Errorf("flat section has no header: %q", lines[2])
	}
	if row := lines[3]; !strings.Contains(row, "badge=3h") || !strings.Contains(row, "href="+pr.URL) {
		t.Errorf("PR row lacks its age badge or link: %q", row)
	}
	parent := lineWith(t, lines, "Dependabot")
	if strings.HasPrefix(parent, "--") || !strings.Contains(parent, "badge=2") || strings.Contains(parent, "href=") {
		t.Errorf("bulk section is not a counted, inert parent: %q", parent)
	}
	if child := lineWith(t, lines, "bump x"); !strings.HasPrefix(child, "--") || !strings.Contains(child, "href="+bot.URL) {
		t.Errorf("bulk row is not a linked submenu row: %q", child)
	}
	if last := lines[len(lines)-1]; last != "Refresh now | refresh=true" {
		t.Errorf("frame ends with %q", last)
	}
}

func TestSwiftBarActions(t *testing.T) {
	const act = "shell=/plugins/github-work.2m.sh param1=--scope=work param2="
	pending := []PendingRoot{
		{Root: "aws/dev", Repo: "o/infra", Commits: 1, CompareURL: "https://github.com/o/infra/compare/a...b"},
		{Root: "aws/prod", Repo: "o/infra", Commits: 2, CompareURL: "https://github.com/o/infra/compare/c...d"},
	}
	lines := swiftBarLines(t, PRCache{
		Runs: []Run{
			{ID: 7, Repo: "o/r", DisplayTitle: "build", Status: "in_progress", HTMLURL: "https://github.com/o/r/actions/runs/7"},
			{ID: 8, Repo: "o/r", DisplayTitle: "deploy", Failed: true, Conclusion: "failure", HTMLURL: "https://github.com/o/r/actions/runs/8"},
		},
		Pending:  pending,
		Releases: []PendingRelease{{Repo: "o/pg", LastTag: "3.8.1", Commits: 3, Bump: "minor", CompareURL: "https://github.com/o/pg/compare/3.8.1...main"}},
	})

	if l := lineWith(t, lines, "build"); !strings.Contains(l, "href=https://github.com/o/r/actions/runs/7") {
		t.Errorf("running run is not a plain link: %q", l)
	}
	if l := lineWith(t, lines, "deploy"); !strings.Contains(l, act+"--open-run=8 terminal=false refresh=true") {
		t.Errorf("failed run does not dismiss through --open-run: %q", l)
	}

	if l := lineWith(t, lines, "Needs terraform apply"); !strings.Contains(l, "badge=2") {
		t.Errorf("apply parent counts the apply-all row: %q", l)
	}
	root := lineWith(t, lines, "aws/dev · needs apply")
	if !strings.HasPrefix(root, "--") || strings.HasPrefix(root, "----") ||
		strings.Contains(root, "shell=") || strings.Contains(root, "href=") {
		t.Errorf("a root row must not act by itself: %q", root)
	}
	if l := lineWith(t, lines, "--dispatch-apply=aws/dev "); !strings.HasPrefix(l, "----Dispatch apply | "+act) {
		t.Errorf("single-root dispatch row: %q", l)
	}
	if l := lineWith(t, lines, "--dispatch-apply=aws/dev,aws/prod "); !strings.HasPrefix(l, "----Dispatch all 2 applies | ") {
		t.Errorf("apply-all dispatch row: %q", l)
	}

	if l := lineWith(t, lines, "--dispatch-release=o/pg@3.9.0 "); !strings.HasPrefix(l, "----Release 3.9.0") ||
		!strings.Contains(l, "suggested") || !strings.Contains(l, "terminal=false") {
		t.Errorf("suggested release row: %q", l)
	}
	lineWith(t, lines, "--dispatch-release=o/pg@3.8.2 ")
	lineWith(t, lines, "--dispatch-release=o/pg@4.0.0 ")
	if l := lineWith(t, lines, "----Compare 3.8.1...main"); !strings.Contains(l, "href=https://github.com/o/pg/compare/3.8.1...main") {
		t.Errorf("compare row: %q", l)
	}
}

func TestSwiftBarEmptyAndError(t *testing.T) {
	lines := swiftBarLines(t, PRCache{})
	lineWith(t, lines, "Nothing open or pending")

	var buf bytes.Buffer
	printSwiftBarError(&buf, "", errors.New("gh search: exit status 1 | shell=/bin/sh"))
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if strings.Count(l, "|") > 1 {
			t.Errorf("error frame line carries a second separator: %q", l)
		}
	}
}

func TestSwiftBarTitle(t *testing.T) {
	if got := swiftBarTitle(0, 12, 0, 0, 0); got != "0·12" {
		t.Errorf("quiet title = %q", got)
	}
	want := "1·3 " + glyphReview + " 2 " + glyphApproval + " 1 " + glyphFailed + " 4"
	if got := swiftBarTitle(1, 3, 2, 1, 4); got != want {
		t.Errorf("busy title = %q, want %q", got, want)
	}
}

func TestSwiftBarValue(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"--scope=work", "--scope=work", true},
		{"JetBrainsMono Nerd Font", `"JetBrainsMono Nerd Font"`, true},
		{"https://github.com/o/r/compare/a...b?x=1&y=2", `"https://github.com/o/r/compare/a...b?x=1&y=2"`, true},
		{"", "", false},
		{`a"b`, "", false},
		{`a\b`, "", false},
		{"a|b", "", false},
		{"a\nb", "", false},
	}
	for _, c := range cases {
		if got, ok := swiftBarValue(c.in); got != c.want || ok != c.ok {
			t.Errorf("swiftBarValue(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPendingRoots(t *testing.T) {
	pending := []PendingRoot{{Root: "aws/dev"}, {Root: "aws/prod"}}
	found, gone := pendingRoots(pending, "aws/prod,google/x")
	if len(found) != 1 || found[0].Root != "aws/prod" {
		t.Errorf("found = %+v, want aws/prod alone", found)
	}
	if len(gone) != 1 || gone[0] != "google/x" {
		t.Errorf("gone = %v, want google/x", gone)
	}
}
