package main

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const glyphRelease = ""

// PendingRelease is a repo whose main carries changes its latest release does not.
type PendingRelease struct {
	Repo       string `json:"repo"`
	Workflow   string `json:"workflow"`
	LastTag    string `json:"last_tag"`
	Commits    int    `json:"commits"`
	NewestWhen string `json:"newest_when"`
	CompareURL string `json:"compare_url"`
	Bump       string `json:"bump"` // patch | minor | major
}

func (p PendingRelease) Next() string {
	v, _ := parseSemver(p.LastTag)
	return v.bump(p.Bump).String()
}

type semver struct {
	prefix              string
	major, minor, patch int
}

var semverRe = regexp.MustCompile(`^(v?)(\d+)\.(\d+)\.(\d+)$`)

var versionInputRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

func parseSemver(s string) (semver, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	v := semver{prefix: m[1]}
	v.major, _ = strconv.Atoi(m[2])
	v.minor, _ = strconv.Atoi(m[3])
	v.patch, _ = strconv.Atoi(m[4])
	return v, true
}

func (v semver) String() string {
	return fmt.Sprintf("%s%d.%d.%d", v.prefix, v.major, v.minor, v.patch)
}

func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v semver) bump(kind string) semver {
	switch kind {
	case "major":
		return semver{prefix: v.prefix, major: v.major + 1}
	case "minor":
		return semver{prefix: v.prefix, major: v.major, minor: v.minor + 1}
	}
	return semver{prefix: v.prefix, major: v.major, minor: v.minor, patch: v.patch + 1}
}

var bumpKinds = []string{"patch", "minor", "major"}

var conventionalRe = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)?([a-z]+)(\([^)]*\))?(!)?:`)

// bumpFor suggests a bump from conventional-commit subjects, with or without
// a leading [TICKET]: `!` is major, feat is minor, anything else patch.
func bumpFor(messages []string) string {
	kind := "patch"
	for _, msg := range messages {
		subject, _, _ := strings.Cut(msg, "\n")
		m := conventionalRe.FindStringSubmatch(subject)
		switch {
		case m == nil:
		case m[3] == "!":
			return "major"
		case m[1] == "feat":
			kind = "minor"
		}
	}
	return kind
}

// releaseRelevantFile reports whether a change to name can reach a release.
// CI config, Claude config and markdown cannot.
func releaseRelevantFile(name string) bool {
	if strings.HasPrefix(name, ".github/") || strings.HasPrefix(name, ".claude/") {
		return false
	}
	return !strings.HasSuffix(strings.ToLower(name), ".md")
}

func latestReleaseTag(tags []string) string {
	var best semver
	found := ""
	for _, t := range tags {
		v, ok := parseSemver(t)
		if !ok {
			continue
		}
		if found == "" || best.less(v) {
			best, found = v, t
		}
	}
	return found
}

type comparison struct {
	AheadBy int          `json:"ahead_by"`
	HTMLURL string       `json:"html_url"`
	Commits []mainCommit `json:"commits"`
	Files   []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}

// The compare endpoint returns at most 300 files.
const compareFileCap = 300

func substantiveRelease(c comparison) bool {
	if len(c.Files) >= compareFileCap {
		return true
	}
	for _, f := range c.Files {
		if releaseRelevantFile(f.Filename) {
			return true
		}
	}
	return false
}

// pendingRelease returns nil and the reason when repo has nothing to release.
func pendingRelease(repo, workflow string) (*PendingRelease, string, error) {
	var rels []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := ghJSON(&rels, "/repos/"+repo+"/releases?per_page=20"); err != nil {
		return nil, "", fmt.Errorf("releases: %w", err)
	}
	var tags []string
	for _, r := range rels {
		if !r.Draft && !r.Prerelease {
			tags = append(tags, r.TagName)
		}
	}
	tag := latestReleaseTag(tags)
	if tag == "" {
		return nil, "no semver release yet", nil
	}

	var c comparison
	if err := ghJSON(&c, fmt.Sprintf("/repos/%s/compare/%s...main", repo, tag)); err != nil {
		return nil, "", fmt.Errorf("compare %s...main: %w", tag, err)
	}
	if c.AheadBy == 0 {
		return nil, "main is at " + tag, nil
	}
	if !substantiveRelease(c) {
		return nil, fmt.Sprintf("%d commit(s) since %s touch only CI and docs", c.AheadBy, tag), nil
	}

	var recent applyRunList
	if err := ghJSON(&recent, fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?per_page=10", repo, workflow)); err != nil {
		return nil, "", fmt.Errorf("release runs: %w", err)
	}
	for _, r := range recent.WorkflowRuns {
		if inFlightStatus(r.Status) {
			return nil, "a release is already running: " + r.DisplayTitle, nil
		}
	}

	var messages []string
	newest := ""
	for _, cm := range c.Commits {
		messages = append(messages, cm.Commit.Message)
		newest = cm.Commit.Committer.Date
	}
	compareURL := c.HTMLURL
	if compareURL == "" {
		compareURL = fmt.Sprintf("https://github.com/%s/compare/%s...main", repo, tag)
	}
	return &PendingRelease{
		Repo:       repo,
		Workflow:   workflow,
		LastTag:    tag,
		Commits:    c.AheadBy,
		NewestWhen: newest,
		CompareURL: compareURL,
		Bump:       bumpFor(messages),
	}, "", nil
}

// releaseMemo holds each repo's last answer, served when a poll of it fails.
var releaseMemo = make(map[string]*PendingRelease)

func fetchReleases(repos []string, workflow string) []PendingRelease {
	var out []PendingRelease
	for _, repo := range repos {
		p, _, err := pendingRelease(repo, workflow)
		if err != nil {
			log.Printf("Error checking %s for unreleased changes: %s", repo, err)
			p = releaseMemo[repo]
		} else {
			releaseMemo[repo] = p
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// notify + dispatch
// ---------------------------------------------------------------------------

var (
	seenReleases      = make(map[string]bool)
	releasesBaselined = false
)

func notifyReleases(ps []PendingRelease, enabled bool) {
	present := make(map[string]bool, len(ps))
	for _, p := range ps {
		present[p.Repo+"@"+p.LastTag] = true
	}
	if !releasesBaselined {
		seenReleases = present
		releasesBaselined = true
		return
	}
	for _, p := range ps {
		key := p.Repo + "@" + p.LastTag
		if seenReleases[key] {
			continue
		}
		seenReleases[key] = true
		if enabled {
			notifySend("Unreleased changes",
				fmt.Sprintf("[%s] %d commit(s) since %s", p.Repo, p.Commits, p.LastTag))
		}
	}
	for key := range seenReleases {
		if !present[key] {
			delete(seenReleases, key)
		}
	}
}

// releaseChoices lists the suggested version first, the other bumps after it,
// then the compare link.
func releaseChoices(p PendingRelease) (labels []string, compare string) {
	v, _ := parseSemver(p.LastTag)
	labels = append(labels, fmt.Sprintf("%-10s %s · suggested", v.bump(p.Bump), p.Bump))
	for _, k := range bumpKinds {
		if k != p.Bump {
			labels = append(labels, fmt.Sprintf("%-10s %s", v.bump(k), k))
		}
	}
	compare = fmt.Sprintf("Compare %s...main (%d commit(s))", p.LastTag, p.Commits)
	return append(labels, compare), compare
}

// chosenVersion maps the confirm prompt's output to a version to release,
// or to nothing when it names neither a listed bump nor a typed version.
func chosenVersion(out string) (string, bool) {
	fields := strings.Fields(out)
	if len(fields) == 0 || !versionInputRe.MatchString(fields[0]) {
		return "", false
	}
	return fields[0], true
}

func chooseRelease(p PendingRelease) {
	labels, compare := releaseChoices(p)
	cmd := exec.Command("fuzzel", "--dmenu", "--match-mode=exact",
		fmt.Sprintf("--width=%d", pickerWidth/2), fmt.Sprintf("--lines=%d", len(labels)),
		"--placeholder=pick a version, or type one",
		"--prompt", fmt.Sprintf("Release %s > ", p.Repo))
	cmd.Stdin = strings.NewReader(strings.Join(labels, "\n"))
	out, err := cmd.Output()
	if err != nil {
		return
	}
	choice := strings.TrimSpace(string(out))
	if choice == compare {
		exec.Command("xdg-open", p.CompareURL).Start()
		return
	}
	version, ok := chosenVersion(choice)
	if !ok {
		notifySend("Release not dispatched", fmt.Sprintf("[%s] %q is not a version", p.Repo, choice))
		return
	}
	dispatchRelease(p, version)
}

func dispatchRelease(p PendingRelease, version string) {
	cmd := ghCommand("workflow", "run", p.Workflow, "--repo", p.Repo, "-f", "version="+version)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		log.Printf("Error dispatching release %s of %s: %s: %s", version, p.Repo, err, msg)
		notifySend("Release dispatch failed", fmt.Sprintf("[%s] %s · %s", p.Repo, version, msg))
		return
	}
	notifySend("Release dispatched",
		fmt.Sprintf("[%s] %s · %d commit(s) since %s", p.Repo, version, p.Commits, p.LastTag))
}
