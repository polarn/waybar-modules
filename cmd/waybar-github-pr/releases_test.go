package main

import (
	"strings"
	"testing"
)

func TestBumpFor(t *testing.T) {
	cases := []struct {
		subjects []string
		want     string
	}{
		{[]string{"ci: [PL-5108] Scan images (#9)", "chore(deps): bump x (#10)", "fix: [PL-5202] Bump Go (#11)"}, "patch"},
		{[]string{"fix: a", "feat: [PL-4967] Add podLabels (#5)"}, "minor"},
		{[]string{"[UI-8704] feat(ui): Record the result count (#1819)", "[UI-8694] fix(api): x"}, "minor"},
		{[]string{"feat: a", "refactor(api)!: drop v1\n\nbody"}, "major"},
		{[]string{"Merge branch 'main'", "Bump x from 1 to 2"}, "patch"},
		{[]string{"fix: mention feat: in passing"}, "patch"},
		{nil, "patch"},
	}
	for _, c := range cases {
		if got := bumpFor(c.subjects); got != c.want {
			t.Errorf("bumpFor(%q) = %s, want %s", c.subjects, got, c.want)
		}
	}
}

func TestLatestReleaseTag(t *testing.T) {
	if got := latestReleaseTag([]string{"3.9.9", "3.10.0", "3.8.1", "nightly", "3.11.0-rc.1"}); got != "3.10.0" {
		t.Errorf("got %q, want 3.10.0 (numeric order, plain releases only)", got)
	}
	if got := latestReleaseTag([]string{"v1.2.3", "v1.2.10"}); got != "v1.2.10" {
		t.Errorf("got %q, want v1.2.10", got)
	}
	if got := latestReleaseTag([]string{"latest"}); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

func TestNext(t *testing.T) {
	cases := []struct{ tag, bump, want string }{
		{"3.8.1", "patch", "3.8.2"},
		{"3.8.1", "minor", "3.9.0"},
		{"3.8.1", "major", "4.0.0"},
		{"v1.9.4", "minor", "v1.10.0"},
	}
	for _, c := range cases {
		if got := (PendingRelease{LastTag: c.tag, Bump: c.bump}).Next(); got != c.want {
			t.Errorf("%s %s → %s, want %s", c.tag, c.bump, got, c.want)
		}
	}
}

func TestSubstantiveRelease(t *testing.T) {
	files := func(names ...string) comparison {
		var c comparison
		for _, n := range names {
			c.Files = append(c.Files, struct {
				Filename string `json:"filename"`
			}{n})
		}
		return c
	}
	if substantiveRelease(files(".github/CODEOWNERS", ".github/workflows/ci.yml", "README.md", "charts/x/README.md", ".claude/settings.json")) {
		t.Error("CI and docs only reported as releasable")
	}
	if !substantiveRelease(files(".github/workflows/ci.yml", "Dockerfile")) {
		t.Error("a Dockerfile change was not reported as releasable")
	}
	many := make([]string, compareFileCap)
	for i := range many {
		many[i] = ".github/x"
	}
	if !substantiveRelease(files(many...)) {
		t.Error("a truncated file list must count as releasable")
	}
}

func TestReleaseChoices(t *testing.T) {
	p := PendingRelease{Repo: "o/r", LastTag: "3.8.1", Bump: "minor", Commits: 3}
	labels, compare := releaseChoices(p)
	if len(labels) != 4 || labels[len(labels)-1] != compare {
		t.Fatalf("want three versions then the compare row, got %q", labels)
	}
	want := []string{"3.9.0", "3.8.2", "4.0.0"}
	for i, w := range want {
		if v, ok := chosenVersion(labels[i]); !ok || v != w {
			t.Errorf("row %d %q → %q, want %s", i, labels[i], v, w)
		}
	}
	if !strings.Contains(labels[0], "suggested") {
		t.Errorf("first row %q is not the suggestion", labels[0])
	}
	if _, ok := chosenVersion(compare); ok {
		t.Error("the compare row parsed as a version")
	}
}

func TestChosenVersion(t *testing.T) {
	for _, in := range []string{"3.10.0", "v2.0.0", "3.9.0-rc.1"} {
		if v, ok := chosenVersion(in); !ok || v != in {
			t.Errorf("typed %q → %q, %v", in, v, ok)
		}
	}
	for _, in := range []string{"", "3.9", "release it", "3.9.0; rm -rf"} {
		if v, ok := chosenVersion(in); ok {
			t.Errorf("typed %q accepted as %q", in, v)
		}
	}
}

func TestPickerReleaseRow(t *testing.T) {
	items := pickerItems(PRCache{Releases: []PendingRelease{
		{Repo: "o/a", LastTag: "1.0.0", Bump: "patch", Commits: 2},
		{Repo: "o/b", LastTag: "2.0.0", Bump: "minor", Commits: 1},
	}})
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for i, want := range []string{"o/a", "o/b"} {
		if items[i].release == nil || items[i].release.Repo != want {
			t.Errorf("item %d does not carry %s's release", i, want)
		}
	}
	if !strings.Contains(items[1].label, "release 2.1.0") {
		t.Errorf("label %q does not name the next version", items[1].label)
	}
}
