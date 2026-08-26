package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
)

type PR struct {
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Repository Repository `json:"repository"`
}

type Repository struct {
	NameWithOwner string `json:"nameWithOwner"`
}

// Notification mirrors the subset of fields we use from the GitHub
// /notifications endpoint. See gh api notifications | jq '.[0]' for the
// full structure.
type Notification struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	Unread     bool   `json:"unread"`
	UpdatedAt  string `json:"updated_at"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Subject struct {
		Title string `json:"title"`
		Type  string `json:"type"`
		URL   string `json:"url"`
	} `json:"subject"`
}

// Tracks notification IDs we've already notify-send'd for. Lives for the
// daemon's lifetime — restart loses state, but that's fine: on first poll
// after start we baseline (mark all current as seen) so the user doesn't
// get a startup flood for pre-existing unread items.
var (
	seenNotifs      = make(map[string]bool)
	notifsBaselined = false

	// Remembers whether a notification's subject PR was merged/closed, keyed
	// by subject URL + updated_at. New activity on the thread (including a
	// reopen) bumps updated_at and forces a re-check, so each thread costs
	// one gh api call per activity burst, not per poll.
	subjectDone = make(map[string]bool)
)

func main() {
	var interval int
	var open bool
	var notify bool
	var swiftbar bool
	var notifyReasonsCSV string
	var runsEnabled bool
	var watchRunsCSV string
	var runsTTL time.Duration
	var discoverOnly bool
	flag.IntVar(&interval, "interval", 120, "Interval of polling in seconds")
	flag.BoolVar(&open, "open", false, "Open PRs interactively and exit")
	flag.BoolVar(&notify, "notify", true, "Fire notify-send for new GitHub notifications")
	flag.BoolVar(&swiftbar, "swiftbar", false, "Emit SwiftBar streamable format instead of waybar JSON (implies --notify=false)")
	flag.StringVar(&notifyReasonsCSV, "notify-reasons",
		"mention,team_mention,review_requested,assign,comment,author",
		"Comma-separated reasons that should produce notify-send + count toward the pill")
	flag.BoolVar(&runsEnabled, "runs", true,
		"Track GitHub Actions runs that need attention (waiting on my approval, or dispatched by me and still going)")
	flag.StringVar(&watchRunsCSV, "watch-runs", "",
		"Extra owner/repo entries to poll for runs, comma-separated — unioned with the auto-discovered set")
	flag.DurationVar(&runsTTL, "runs-ttl", time.Hour,
		"How long the discovered set of reviewer-gated repos stays cached before re-scanning")
	flag.BoolVar(&discoverOnly, "discover-only", false,
		"Print the discovered reviewer-gated repos and exit")
	flag.Parse()

	if open {
		openPRs()
		return
	}

	var watchRuns []string
	for _, r := range strings.Split(watchRunsCSV, ",") {
		if r = strings.TrimSpace(r); r != "" {
			watchRuns = append(watchRuns, r)
		}
	}

	if discoverOnly {
		repos, login := watchedRepos(watchRuns, runsTTL)
		fmt.Printf("login: %s\n", login)
		for _, r := range repos {
			fmt.Println(r)
		}
		return
	}

	if swiftbar {
		notify = false
	}

	notifyReasons := map[string]bool{}
	for _, r := range strings.Split(notifyReasonsCSV, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			notifyReasons[r] = true
		}
	}

	for {
		approved, err := fetchPRs("approved")
		if err != nil {
			log.Printf("Error fetching approved PRs: %s", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		all, err := fetchPRs("")
		if err != nil {
			log.Printf("Error fetching all PRs: %s", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		// Runs are fetched independently of the PR calls above: a repo that
		// fails to answer must not blank the PR counts. When any watched repo
		// fails, runsResult.Complete goes false and the run segment is omitted
		// entirely rather than reported as a count that is quietly short.
		// The zero runsResult has Complete false, so disabling runs omits the
		// segment by the same path a failed poll does.
		var runs runsResult
		var approvalRuns, runningRuns []Run
		if runsEnabled && !swiftbar {
			repos, login := watchedRepos(watchRuns, runsTTL)
			runs = fetchRuns(repos, login)
			approvalRuns, runningRuns = splitRuns(runs.Runs)
			notifyApprovals(approvalRuns, notify)
		}

		var tooltips []string
		for _, pr := range all {
			log.Printf("%s: %s - %s", pr.Repository.NameWithOwner, pr.Title, pr.URL)
			prefix := "  "
			if isApproved(pr, approved) {
				prefix = "✓ "
			}
			line := fmt.Sprintf("%s[%s] %s", prefix, pr.Repository.NameWithOwner, pr.Title)
			if len(line) > 70 {
				line = line[:67] + "..."
			}
			tooltips = append(tooltips, line)
		}

		status := "none"
		if len(approved) > 0 {
			status = "found"
		}

		// Pull notifications, fire notify-send for any new ones, and feed the
		// filtered count into the pill / tooltip / left-click menu.
		notifs := processNotifications(notifyReasons, notify)
		if !swiftbar {
			writePRCache(all, approved, notifs, runs.Runs)
		}

		if swiftbar {
			printSwiftBarGitHub(approved, all, notifs)
			return
		}

		text := fmt.Sprintf("%d·%d", len(approved), len(all))
		if len(notifs) > 0 {
			text += fmt.Sprintf(" 󰂜 %d", len(notifs))
		}
		if runs.Complete {
			if n := len(approvalRuns); n > 0 {
				text += fmt.Sprintf(" 󰥔 %d", n)
			}
			if n := len(runningRuns); n > 0 {
				text += fmt.Sprintf(" 󰑐 %d", n)
			}
		}

		if len(notifs) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Notifications</b>")
			for _, n := range notifs {
				line := fmt.Sprintf("  [%s] %s · %s", n.Reason, n.Repository.FullName, n.Subject.Title)
				if len(line) > 80 {
					line = line[:77] + "..."
				}
				tooltips = append(tooltips, line)
			}
		}

		if runs.Complete && len(runs.Runs) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Workflow runs</b>")
			for _, r := range append(append([]Run{}, approvalRuns...), runningRuns...) {
				glyph := "󰑐"
				suffix := ""
				if r.NeedsMe {
					glyph = "󰥔"
					if r.Environment != "" {
						suffix = " · " + pangoEscape(r.Environment)
					}
				}
				line := fmt.Sprintf("  %s [%s] %s%s", glyph,
					pangoEscape(r.Repo), pangoEscape(trimRunes(r.Title(), 60)), suffix)
				tooltips = append(tooltips, line)
			}
		}

		w := waybar.New()
		w.Text = text
		w.ToolTip = strings.Join(tooltips, "\n")
		// class carries two independent dimensions, so it goes out as an
		// array: the PR status, plus the run state when there is one.
		//
		// Gated on runs.Complete for the same reason the text and tooltip
		// are: an incomplete poll must not colour the pill for a state it
		// cannot also show a count for.
		classes := []string{status}
		if runs.Complete {
			if len(runningRuns) > 0 {
				classes = append(classes, "running")
			}
			if len(approvalRuns) > 0 {
				classes = append(classes, "approval")
			}
		}
		w.Class = classes
		w.Alt = status

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

// printSwiftBarGitHub emits one SwiftBar plugin frame and exits. SwiftBar
// runs the script on its filename interval (e.g. github-pr.5m.sh → every 5
// minutes), so this is invoked fresh each tick — no loop, no separator.
func printSwiftBarGitHub(approved, all []PR, notifs []Notification) {
	title := fmt.Sprintf("%d·%d :arrow.triangle.pull:", len(approved), len(all))
	if len(notifs) > 0 {
		title = fmt.Sprintf("%d·%d :bell.badge: %d", len(approved), len(all), len(notifs))
	}
	fmt.Println(title)
	fmt.Println("---")
	fmt.Println("Open PRs | href=https://github.com/pulls")
}

// fetchNotifications reads the user's unread GitHub notifications via the
// already-authenticated gh CLI. Returns the raw list — caller filters.
func fetchNotifications() ([]Notification, error) {
	out, err := exec.Command("gh", "api", "notifications").Output()
	if err != nil {
		return nil, err
	}
	var n []Notification
	if err := json.Unmarshal(out, &n); err != nil {
		return nil, err
	}
	return n, nil
}

// processNotifications filters by reason, baselines on first run (so the
// daemon doesn't spam notify-send for everything sitting unread on startup),
// and fires notify-send for genuinely new notifications. Returns the filtered
// set so the caller can display a count and tooltip.
func processNotifications(reasons map[string]bool, notify bool) []Notification {
	all, err := fetchNotifications()
	if err != nil {
		log.Printf("notifications: %s", err)
		return nil
	}
	var filtered []Notification
	for _, n := range all {
		if !n.Unread {
			continue
		}
		if !reasons[n.Reason] {
			continue
		}
		if subjectIsDone(n) {
			continue
		}
		filtered = append(filtered, n)
	}
	if !notify {
		return filtered
	}
	if !notifsBaselined {
		for _, n := range filtered {
			seenNotifs[n.ID] = true
		}
		notifsBaselined = true
		return filtered
	}
	for _, n := range filtered {
		if seenNotifs[n.ID] {
			continue
		}
		seenNotifs[n.ID] = true
		notifySendForGitHub(n)
	}
	return filtered
}

// subjectIsDone reports whether a PullRequest notification points at a PR
// that is merged or closed (GitHub reports merged PRs as state "closed").
// GitHub keeps threads unread until explicitly marked read on github.com,
// so without this check merged-PR notifications linger forever. Fails open:
// better to show a stale notification than hide a live one.
func subjectIsDone(n Notification) bool {
	if n.Subject.Type != "PullRequest" || n.Subject.URL == "" {
		return false
	}
	key := n.Subject.URL + "@" + n.UpdatedAt
	if done, ok := subjectDone[key]; ok {
		return done
	}
	out, err := exec.Command("gh", "api", n.Subject.URL, "--jq", ".state").Output()
	if err != nil {
		log.Printf("pr state (%s): %s", n.Subject.URL, err)
		return false
	}
	done := strings.TrimSpace(string(out)) != "open"
	subjectDone[key] = done
	return done
}

// notifySendForGitHub turns a notification into a freedesktop-style desktop
// notification. swaync renders these and the user can click through to
// GitHub manually.
func notifySendForGitHub(n Notification) {
	title := titleForReason(n.Reason)
	body := fmt.Sprintf("[%s] %s", n.Repository.FullName, n.Subject.Title)
	cmd := exec.Command("notify-send",
		"--app-name", "GitHub",
		"--icon", "github",
		"--category", "im.received",
		title, body)
	if err := cmd.Run(); err != nil {
		log.Printf("notify-send failed: %s", err)
	}
}

func titleForReason(reason string) string {
	switch reason {
	case "mention":
		return "GitHub: you were mentioned"
	case "team_mention":
		return "GitHub: team mentioned"
	case "review_requested":
		return "GitHub: review requested"
	case "assign":
		return "GitHub: assigned"
	case "comment":
		return "GitHub: new comment"
	case "author":
		return "GitHub: activity on your PR"
	case "ci_activity":
		return "GitHub: CI activity"
	case "state_change":
		return "GitHub: state changed"
	case "security_alert":
		return "GitHub: security alert"
	default:
		return "GitHub: " + reason
	}
}

func fetchPRs(review string) ([]PR, error) {
	args := []string{"search", "prs",
		"--state=open",
		"--author=@me",
		"--json=title,url,repository",
	}
	if review != "" {
		args = append(args, "--review="+review)
	}

	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("gh search: %w", err)
	}

	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	return prs, nil
}

func isApproved(pr PR, approved []PR) bool {
	for _, a := range approved {
		if a.URL == pr.URL {
			return true
		}
	}
	return false
}

func cacheFilePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "waybar-github-prs.json")
}

type PRCache struct {
	All           []PR           `json:"all"`
	Approved      []PR           `json:"approved"`
	Notifications []Notification `json:"notifications,omitempty"`
	Runs          []Run          `json:"runs,omitempty"`
}

func writePRCache(all, approved []PR, notifs []Notification, runs []Run) {
	data, err := json.Marshal(PRCache{All: all, Approved: approved, Notifications: notifs, Runs: runs})
	if err != nil {
		log.Printf("Error marshaling PR cache: %s", err)
		return
	}
	if err := os.WriteFile(cacheFilePath(), data, 0600); err != nil {
		log.Printf("Error writing PR cache: %s", err)
	}
}

// subjectWebURL converts a notification's API URL to the equivalent
// github.com URL that xdg-open can sensibly hand to the browser. Covers
// PRs and issues (the vast majority of notifications); anything else
// falls back to the API URL, which the browser will redirect/render.
func subjectWebURL(n Notification) string {
	s := n.Subject.URL
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "https://api.github.com/repos/")
	s = strings.Replace(s, "/pulls/", "/pull/", 1) // singular on web
	return "https://github.com/" + s
}

// openPRs opens a fuzzel picker that merges the cached open PRs with any
// unread notifications. Selecting an entry opens the relevant URL in the
// default browser.
func openPRs() {
	data, err := os.ReadFile(cacheFilePath())
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}

	var cache PRCache
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("Error reading cache: %s", err)
		return
	}

	type item struct {
		label string
		url   string
	}
	var items []item
	for _, pr := range cache.All {
		prefix := "○"
		if isApproved(pr, cache.Approved) {
			prefix = "✓"
		}
		items = append(items, item{
			label: fmt.Sprintf("%s [%s] %s", prefix, pr.Repository.NameWithOwner, pr.Title),
			url:   pr.URL,
		})
	}
	for _, r := range cache.Runs {
		// The run ID keeps labels unique: the picker matches the fuzzel
		// selection back by exact label equality, so two runs of the same
		// workflow in the same repo would otherwise collide.
		prefix, suffix := "󰑐", ""
		if r.NeedsMe {
			prefix = "󰥔"
			suffix = " · needs approval"
			if r.Environment != "" {
				suffix = " · needs approval (" + r.Environment + ")"
			}
		}
		items = append(items, item{
			label: fmt.Sprintf("%s [%s] %s%s #%d", prefix, r.Repo, r.Title(), suffix, r.ID),
			url:   r.HTMLURL,
		})
	}

	for _, n := range cache.Notifications {
		items = append(items, item{
			label: fmt.Sprintf("󰂜 [%s] [%s] %s", n.Reason, n.Repository.FullName, n.Subject.Title),
			url:   subjectWebURL(n),
		})
	}

	if len(items) == 0 {
		return
	}
	if len(items) == 1 {
		exec.Command("xdg-open", items[0].url).Start()
		return
	}

	var entries []string
	for _, it := range items {
		entries = append(entries, it.label)
	}

	cmd := exec.Command("fuzzel", "--dmenu", "--width=75", "--prompt", "GitHub > ")
	cmd.Stdin = strings.NewReader(strings.Join(entries, "\n"))
	out, err := cmd.Output()
	if err != nil {
		return // user cancelled
	}

	selected := strings.TrimSpace(string(out))
	for _, it := range items {
		if it.label == selected {
			exec.Command("xdg-open", it.url).Start()
			return
		}
	}
}

// trimRunes shortens s to at most n runes, appending an ellipsis when it had
// to cut. Rune-based on purpose: the older byte slicing in this file can split
// a multi-byte character and emit invalid UTF-8.
func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// pangoEscape makes text safe for the tooltip, which waybar renders as Pango
// markup. A run title containing & or < would otherwise break rendering of the
// whole tooltip, not just its own line.
func pangoEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(s)
}
