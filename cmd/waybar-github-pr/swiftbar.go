package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	glyphGitHub   = "\uf09b"
	glyphWarning  = "\uf071"
	glyphApproval = "\U000f0954"
	glyphFailed   = "\U000f0028"
)

func swiftBarTitle(approved, all, reviews, needApproval, failed int) string {
	title := fmt.Sprintf("%d·%d", approved, all)
	for _, seg := range []struct {
		glyph string
		n     int
	}{{glyphReview, reviews}, {glyphApproval, needApproval}, {glyphFailed, failed}} {
		if seg.n > 0 {
			title += fmt.Sprintf(" %s %d", seg.glyph, seg.n)
		}
	}
	return title
}

type swiftBarMenu struct {
	title    string
	sections []section
	font     string
	self     string
	scope    string
}

var swiftBarBare = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./#~-]+$`)

func swiftBarText(s string) string {
	return strings.NewReplacer("|", "│", "\n", " ", "\r", " ", "\t", " ").Replace(s)
}

func swiftBarValue(v string) (string, bool) {
	if swiftBarBare.MatchString(v) {
		return v, true
	}
	if v == "" || strings.ContainsAny(v, "\"\\|\n\r") {
		return "", false
	}
	return `"` + v + `"`, true
}

func swiftBarLink(url string) (string, bool) {
	if !strings.HasPrefix(url, "https://") {
		return "", false
	}
	v, ok := swiftBarValue(url)
	return "href=" + v, ok
}

func swiftBarSelf() string {
	if p := os.Getenv("SWIFTBAR_PLUGIN_PATH"); p != "" {
		return p
	}
	self, err := os.Executable()
	if err != nil {
		return "waybar-github-pr"
	}
	return self
}

const swiftBarPlain = "emojize=false symbolize=false"

func (m swiftBarMenu) titleStyle() string {
	if font, ok := swiftBarValue(m.font); ok {
		return "font=" + font + " " + swiftBarPlain
	}
	return swiftBarPlain
}

func (m swiftBarMenu) rowStyle() string {
	if font, ok := swiftBarValue(m.font); ok {
		return "font=" + font + " size=12 " + swiftBarPlain
	}
	return swiftBarPlain
}

func (m swiftBarMenu) line(depth int, text string, params ...string) string {
	params = append(params, m.rowStyle())
	return strings.Repeat("--", depth) + swiftBarText(text) + " | " + strings.Join(params, " ")
}

func (m swiftBarMenu) action(flag string) (string, bool) {
	args := []string{flag}
	if m.scope != "" {
		args = []string{"--scope=" + m.scope, flag}
	}
	self, ok := swiftBarValue(m.self)
	if !ok {
		return "", false
	}
	out := "shell=" + self
	for i, a := range args {
		v, ok := swiftBarValue(a)
		if !ok {
			return "", false
		}
		out += fmt.Sprintf(" param%d=%s", i+1, v)
	}
	return out + " terminal=false refresh=true", true
}

func (m swiftBarMenu) rows(depth int, it item) []string {
	text := it.glyph + " " + it.text
	var params []string
	if age := humanAge(it.when); age != "" {
		params = append(params, "badge="+age)
	}

	switch {
	case it.dispatch != nil:
		lines := []string{m.line(depth, text, params...)}
		var roots []string
		for _, p := range it.dispatch {
			roots = append(roots, p.Root)
		}
		label := "Dispatch apply"
		if len(roots) > 1 {
			label = fmt.Sprintf("Dispatch all %d applies", len(roots))
		}
		listable := !slices.ContainsFunc(roots, func(r string) bool { return strings.Contains(r, ",") })
		if a, ok := m.action("--dispatch-apply=" + strings.Join(roots, ",")); ok && listable {
			lines = append(lines, m.line(depth+1, label, a))
		}
		if href, ok := swiftBarLink(it.url); ok {
			lines = append(lines, m.line(depth+1, "Open compare", href))
		}
		return lines

	case it.release != nil:
		p := *it.release
		lines := []string{m.line(depth, text, params...)}
		labels, compare := releaseChoices(p)
		for _, label := range labels {
			if label == compare {
				if href, ok := swiftBarLink(p.CompareURL); ok {
					lines = append(lines, m.line(depth+1, label, href))
				}
				continue
			}
			version, ok := chosenVersion(label)
			if !ok {
				continue
			}
			if a, ok := m.action("--dispatch-release=" + p.Repo + "@" + version); ok {
				lines = append(lines, m.line(depth+1, "Release "+label, a))
			}
		}
		return lines

	case it.dismiss != 0:
		if a, ok := m.action(fmt.Sprintf("--open-run=%d", it.dismiss)); ok {
			return []string{m.line(depth, text, append(params, a)...)}
		}
	}

	if href, ok := swiftBarLink(it.url); ok {
		params = append(params, href)
	}
	return []string{m.line(depth, text, params...)}
}

func bulkCount(s section) int {
	n := 0
	for _, it := range s.items {
		if len(it.dispatch) <= 1 {
			n++
		}
	}
	return n
}

const swiftBarDim = "color=#6e6e6e,#9a9a9a " + swiftBarPlain

func printSwiftBar(w io.Writer, m swiftBarMenu) {
	fmt.Fprintln(w, swiftBarText(glyphGitHub+" "+m.title)+" | "+m.titleStyle())
	fmt.Fprintln(w, "---")

	var flat, bulk []section
	for _, s := range m.sections {
		if s.bulk {
			bulk = append(bulk, s)
		} else {
			flat = append(flat, s)
		}
	}

	for _, s := range flat {
		fmt.Fprintln(w, swiftBarText(strings.ToUpper(s.title))+" | size=10 "+swiftBarDim)
		for _, it := range s.items {
			for _, l := range m.rows(0, it) {
				fmt.Fprintln(w, l)
			}
		}
	}
	if len(flat) > 0 && len(bulk) > 0 {
		fmt.Fprintln(w, "---")
	}
	for _, s := range bulk {
		fmt.Fprintln(w, m.line(0, s.title, fmt.Sprintf("badge=%d", bulkCount(s))))
		for _, it := range s.items {
			for _, l := range m.rows(1, it) {
				fmt.Fprintln(w, l)
			}
		}
	}
	if len(m.sections) == 0 {
		fmt.Fprintln(w, "Nothing open or pending | "+swiftBarDim)
	}

	fmt.Fprintln(w, "---")
	fmt.Fprintln(w, "Refresh now | refresh=true")
}

func printSwiftBarError(w io.Writer, font string, err error) {
	m := swiftBarMenu{font: font}
	fmt.Fprintln(w, glyphGitHub+" "+glyphWarning+" | "+m.titleStyle())
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w, swiftBarText(err.Error())+" | "+swiftBarDim)
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w, "Refresh now | refresh=true")
}

func openRun(scope string, id int64) {
	cache, err := readPRCache(scope)
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}
	for _, r := range cache.Runs {
		if r.ID != id {
			continue
		}
		var dismiss int64
		if r.Failed {
			dismiss = r.ID
		}
		openItem(r.HTMLURL, dismiss)
		return
	}
	notifySend("Workflow run not opened", fmt.Sprintf("run %d is no longer listed", id))
}

func pendingRoots(pending []PendingRoot, rootsCSV string) (found []PendingRoot, gone []string) {
	for _, root := range strings.Split(rootsCSV, ",") {
		i := slices.IndexFunc(pending, func(p PendingRoot) bool { return p.Root == root })
		if i < 0 {
			gone = append(gone, root)
			continue
		}
		found = append(found, pending[i])
	}
	return found, gone
}

func dispatchCachedApplies(scope, rootsCSV string) {
	cache, err := readPRCache(scope)
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}
	found, gone := pendingRoots(cache.Pending, rootsCSV)
	if len(gone) > 0 {
		notifySend("Terraform apply not dispatched", strings.Join(gone, ", ")+" · no longer pending")
	}
	dispatchApplies(found)
}

func dispatchCachedRelease(scope, spec string) {
	cache, err := readPRCache(scope)
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}
	repo, version, _ := strings.Cut(spec, "@")
	i := slices.IndexFunc(cache.Releases, func(p PendingRelease) bool { return p.Repo == repo })
	if i < 0 || !versionInputRe.MatchString(version) {
		notifySend("Release not dispatched", fmt.Sprintf("[%s] %q is not a pending release", repo, version))
		return
	}
	dispatchRelease(cache.Releases[i], version)
}

var subjectDoneSaved map[string]bool

func subjectDonePath() string {
	return filepath.Join(filepath.Dir(repoCachePath()), "subject-done.json")
}

func loadSubjectDone() {
	data, err := os.ReadFile(subjectDonePath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &subjectDoneSaved)
}

func saveSubjectDone() {
	data, err := json.Marshal(subjectDone)
	if err != nil {
		log.Printf("Error marshaling subject states: %s", err)
		return
	}
	path := subjectDonePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("Error creating cache dir: %s", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("Error writing subject states: %s", err)
	}
}
