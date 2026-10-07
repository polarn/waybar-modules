package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
)

type PR struct {
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Number     int        `json:"number"`
	CreatedAt  string     `json:"createdAt"`
	IsDraft    bool       `json:"isDraft"`
	Repository Repository `json:"repository"`

	// Resolved separately and joined on by URL — the PR search reports
	// neither. Base is set only when it is not the repository's default
	// branch; Queue is nil unless the PR is queued or was recently refused,
	// which is the common case.
	Base     string      `json:"base,omitempty"`
	Queue    *QueueState `json:"queue,omitempty"`
	Comments int         `json:"comments,omitempty"`
}

const glyphComments = "\U000F0182"

func commentsSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" %s %d", glyphComments, n)
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

// Tracks the updated_at each notification was last notify-send'd at, so new
// activity on a thread that is still unread pops up again. Lives for the
// daemon's lifetime — restart loses state, but that's fine: on first poll
// after start we baseline (mark all current as seen) so the user doesn't
// get a startup flood for pre-existing unread items.
var (
	seenNotifs      = make(map[string]string)
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
	var hideTitles string
	var notify bool
	var swiftbar bool
	var notifyReasonsCSV string
	var runsEnabled bool
	var queueEnabled bool
	var reviewsEnabled bool
	var watchRunsCSV string
	var runsTTL time.Duration
	var failedTTL time.Duration
	var discoverOnly bool
	var applyRepo string
	var applyWorkflow string
	var applyWindow time.Duration
	var applyDryRun bool
	var applyIncludeUnapplied bool
	var queueDryRun bool
	var scopeName string
	var workOwnersCSV string
	var hideEmpty bool
	flag.IntVar(&interval, "interval", 120, "Interval of polling in seconds")
	flag.StringVar(&scopeName, "scope", "",
		"work or personal: keep only repos owned by --work-owners, or only the rest (empty keeps everything). Also names the --open cache")
	flag.StringVar(&workOwnersCSV, "work-owners", "",
		"Comma-separated owners (users or orgs) whose repos count as work for --scope")
	flag.BoolVar(&hideEmpty, "hide-empty", false,
		"Print empty text, which hides the pill, when nothing is open or pending")
	flag.BoolVar(&open, "open", false, "Open PRs interactively and exit")
	flag.StringVar(&hideTitles, "hide-titles", "",
		"With --open, leave out PRs (and their notifications) whose title matches this regexp")
	flag.BoolVar(&notify, "notify", true, "Fire notify-send for new GitHub notifications")
	flag.BoolVar(&swiftbar, "swiftbar", false, "Emit SwiftBar streamable format instead of waybar JSON (implies --notify=false)")
	flag.StringVar(&notifyReasonsCSV, "notify-reasons",
		"mention,team_mention,review_requested,assign,comment,author",
		"Comma-separated reasons that should produce notify-send + count toward the pill")
	flag.BoolVar(&runsEnabled, "runs", true,
		"Track GitHub Actions runs that need attention (waiting on my approval, or dispatched by me and still going)")
	flag.BoolVar(&queueEnabled, "merge-queue", true,
		"Track whether my PRs are sitting in a merge queue, or were refused by one")
	flag.BoolVar(&reviewsEnabled, "reviews", true,
		"Track open PRs whose review is requested from me or one of my teams")
	flag.StringVar(&watchRunsCSV, "watch-runs", "",
		"Extra owner/repo entries to poll for runs, comma-separated — unioned with the auto-discovered set")
	flag.DurationVar(&runsTTL, "runs-ttl", time.Hour,
		"How long the discovered set of reviewer-gated repos stays cached before re-scanning")
	flag.DurationVar(&failedTTL, "failed-ttl", 72*time.Hour,
		"How long a failed run stays on the pill when nothing newer has run the same thing (0 keeps it until dismissed)")
	flag.BoolVar(&discoverOnly, "discover-only", false,
		"Print the discovered reviewer-gated repos and exit")
	flag.StringVar(&applyRepo, "apply-repo", "",
		"owner/repo to track terraform roots merged but not yet applied (empty disables)")
	flag.StringVar(&applyWorkflow, "apply-workflow", "apply.yml",
		"Dispatch workflow whose `root` choice input enumerates the roots")
	flag.DurationVar(&applyWindow, "apply-window", 336*time.Hour,
		"How far back on main to look for unapplied merges")
	flag.BoolVar(&applyDryRun, "apply-dry-run", false,
		"Print the roots pending apply, with the reasoning, and exit")
	flag.BoolVar(&queueDryRun, "queue-dry-run", false,
		"Print the merge-queue state of my open PRs and exit")
	flag.BoolVar(&applyIncludeUnapplied, "apply-include-unapplied", false,
		"Also report roots that have never been applied through the workflow (no baseline, noisy)")
	flag.Parse()

	sc := scope{name: scopeName, owners: map[string]bool{}}
	for _, o := range strings.Split(workOwnersCSV, ",") {
		if o = strings.TrimSpace(o); o != "" {
			sc.owners[strings.ToLower(o)] = true
		}
	}
	switch scopeName {
	case "", "work", "personal":
	default:
		log.Fatalf("--scope must be work or personal, got %q", scopeName)
	}

	if open {
		var hide *regexp.Regexp
		if hideTitles != "" {
			re, err := regexp.Compile(hideTitles)
			if err != nil {
				log.Fatalf("--hide-titles: %s", err)
			}
			hide = re
		}
		openPRs(sc.name, hide)
		return
	}

	if sc.name != "" && len(sc.owners) == 0 {
		log.Fatal("--scope needs --work-owners")
	}

	var watchRuns []string
	for _, r := range strings.Split(watchRunsCSV, ",") {
		if r = strings.TrimSpace(r); r != "" {
			watchRuns = append(watchRuns, r)
		}
	}

	if applyDryRun {
		if applyRepo == "" {
			log.Fatal("--apply-dry-run needs --apply-repo")
		}
		pending, err := fetchPending(applyRepo, applyWorkflow, applyWindow, applyIncludeUnapplied)
		if err != nil {
			log.Fatalf("apply tracking: %s", err)
		}
		roots, rerr := fetchRoots(applyRepo, applyWorkflow)
		if rerr != nil {
			log.Fatalf("root list: %s", rerr)
		}
		fmt.Printf("%d roots parsed from %s\n", len(roots), applyWorkflow)
		if len(pending) == 0 {
			fmt.Println("no roots pending apply")
		}
		for _, p := range pending {
			fmt.Printf("%-32s %d commit(s), newest %s (%s)\n",
				p.Root, p.Commits, p.NewestSHA[:7], p.NewestWhen)
		}
		return
	}

	if queueDryRun {
		f := fetchPRFacts()
		if !f.Complete {
			log.Fatal("PR facts query failed; see the error above")
		}
		if len(f.Queue) == 0 {
			fmt.Println("no open PRs")
		}
		for url, s := range f.Queue {
			summary := s.Summary()
			if summary == "" {
				summary = "not queued"
			}
			if base := f.Base[url]; base != "" {
				summary += "  (base " + base + ")"
			}
			fmt.Printf("%-60s %s\n", url, summary)
		}
		return
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
		approved, all = keepPRs(approved, sc), keepPRs(all, sc)

		// Runs are fetched independently of the PR calls above: a repo that
		// fails to answer must not blank the PR counts. When any watched repo
		// fails, runsResult.Complete goes false and the run segment is omitted
		// entirely rather than reported as a count that is quietly short.
		// The zero runsResult has Complete false, so disabling runs omits the
		// segment by the same path a failed poll does.
		var runs runsResult
		var approvalRuns, runningRuns, failedRuns []Run
		if runsEnabled && !swiftbar {
			repos, login := watchedRepos(watchRuns, runsTTL)
			runs = fetchRuns(keepRepos(repos, sc), login, failedTTL)
			approvalRuns, runningRuns, failedRuns = splitRuns(runs.Runs)
			notifyApprovals(approvalRuns, notify)
		}

		// Roots that landed on main and were never applied. Independent of
		// the runs poll above: a failure here must not disturb either the PR
		// counts or the run states.
		var pending []PendingRoot
		if applyRepo != "" && !swiftbar {
			p, err := fetchPending(applyRepo, applyWorkflow, applyWindow, applyIncludeUnapplied)
			if err != nil {
				log.Printf("Error tracking unapplied roots: %s", err)
			} else {
				pending = p
				notifyPending(pending, notify)
			}
		}

		// Merge-queue state for my own PRs. Independent of everything above
		// for the same reason the runs poll is: a failure here must not
		// disturb the PR counts.
		var queue prFacts
		var inQueue, refused int
		if queueEnabled && !swiftbar {
			queue = fetchPRFacts()
			annotatePRs(all, queue)
			inQueue, refused = splitQueue(all)
			notifyRejections(all, notify)
		}

		var reviews reviewsResult
		if reviewsEnabled && !swiftbar {
			reviews = fetchReviews(sc)
		}

		// Pull notifications, fire notify-send for any new ones, and feed the
		// filtered count into the pill / tooltip / left-click menu.
		notifs := processNotifications(notifyReasons, notify, sc, reviews)
		reasons, loose := foldNotifications(all, reviews.Requests, notifs)

		var tooltips, draftTips []string
		for _, pr := range all {
			log.Printf("%s: %s - %s", pr.Repository.NameWithOwner, pr.Title, pr.URL)
			prefix := "  "
			switch {
			case pr.IsDraft:
				prefix = glyphDraft + " "
			case isApproved(pr, approved):
				prefix = "✓ "
			}
			line := fmt.Sprintf("%s[%s] %s", prefix,
				pangoEscape(pr.Repository.NameWithOwner), pangoEscape(trimRunes(pr.Title, tooltipTitleRunes)))
			if pr.Queue != nil {
				line += " · " + pangoEscape(pr.Queue.Summary())
			}
			line += commentsSuffix(pr.Comments) + pangoEscape(reasons[pr.URL])
			if pr.IsDraft {
				draftTips = append(draftTips, line)
			} else {
				tooltips = append(tooltips, line)
			}
		}
		if len(draftTips) > 0 {
			tooltips = append(tooltips, "", "<b>Drafts</b>")
			tooltips = append(tooltips, draftTips...)
		}

		status := "none"
		if len(approved) > 0 {
			status = "found"
		}

		if !swiftbar {
			writePRCache(sc.name, all, approved, reviews.Requests, notifs, runs.Runs, pending)
		}

		if swiftbar {
			printSwiftBarGitHub(approved, all, notifs)
			return
		}

		text := fmt.Sprintf("%d·%d", len(approved), len(all))
		if queue.Complete {
			if inQueue > 0 {
				text += fmt.Sprintf(" %s %d", glyphQueue, inQueue)
			}
			if refused > 0 {
				text += fmt.Sprintf(" %s %d", glyphDequeued, refused)
			}
		}
		if n := len(reviews.Requests); n > 0 {
			text += fmt.Sprintf(" %s %d", glyphReview, n)
		}
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
			if n := len(failedRuns); n > 0 {
				text += fmt.Sprintf(" 󰀨 %d", n)
			}
		}
		if n := len(pending); n > 0 {
			text += fmt.Sprintf(" 󰅧 %d", n)
		}

		if len(reviews.Requests) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Review requested</b>")
			for _, r := range reviews.Requests {
				tooltips = append(tooltips, fmt.Sprintf("  %s %s%s%s", glyphReview, pangoEscape(r.Head()),
					pangoEscape(trimRunes(r.Title, tooltipTitleRunes)), pangoEscape(r.Suffix()+reasons[r.URL])))
			}
		}

		if len(loose) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Notifications</b>")
			for _, n := range loose {
				tooltips = append(tooltips, fmt.Sprintf("  [%s] %s · %s", pangoEscape(n.Reason),
					pangoEscape(n.Repository.FullName), pangoEscape(trimRunes(n.Subject.Title, tooltipTitleRunes))))
			}
		}

		if runs.Complete && len(runs.Runs) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Workflow runs</b>")
			ordered := append(append(append([]Run{}, approvalRuns...), failedRuns...), runningRuns...)
			for _, r := range ordered {
				glyph := "󰑐"
				suffix := ""
				switch {
				case r.NeedsMe:
					glyph = "󰥔"
					if r.Environment != "" {
						suffix = " · " + pangoEscape(r.Environment)
					}
				case r.Failed:
					glyph = "󰀨"
					suffix = " · " + pangoEscape(r.Conclusion)
				}
				line := fmt.Sprintf("  %s [%s] %s%s", glyph,
					pangoEscape(r.Repo), pangoEscape(trimRunes(r.Title(), tooltipTitleRunes)), suffix)
				tooltips = append(tooltips, line)
			}
		}

		if len(pending) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Needs terraform apply</b>")
			for _, p := range pending {
				tooltips = append(tooltips, fmt.Sprintf("  󰅧 %s · %d commit(s)",
					pangoEscape(trimRunes(p.Root, 60)), p.Commits))
			}
		}

		w := waybar.New()
		w.Text = text
		if hideEmpty && len(all) == 0 && len(reviews.Requests) == 0 && len(notifs) == 0 && len(pending) == 0 &&
			!(runs.Complete && len(runs.Runs) > 0) {
			w.Text = ""
		}
		w.ToolTip = unwrapped(tooltips)
		// class carries two independent dimensions, so it goes out as an
		// array: the PR status, plus the run state when there is one.
		//
		// Gated on runs.Complete for the same reason the text and tooltip
		// are: an incomplete poll must not colour the pill for a state it
		// cannot also show a count for.
		classes := []string{status}
		if queue.Complete {
			if inQueue > 0 {
				classes = append(classes, "queued")
			}
			if refused > 0 {
				classes = append(classes, "dequeued")
			}
		}
		if len(pending) > 0 {
			classes = append(classes, "pending")
		}
		if len(reviews.Requests) > 0 {
			classes = append(classes, "review")
		}
		if runs.Complete {
			if len(runningRuns) > 0 {
				classes = append(classes, "running")
			}
			if len(approvalRuns) > 0 {
				classes = append(classes, "approval")
			}
			// Declared last in style.css too: a failure is an unexpected bad
			// state and must never be masked by an expected one. Both counts
			// stay visible in the text either way.
			if len(failedRuns) > 0 {
				classes = append(classes, "failed")
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
func processNotifications(reasons map[string]bool, notify bool, sc scope, reviews reviewsResult) []Notification {
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
		if !sc.keeps(n.Repository.FullName) {
			continue
		}
		if reviews.stale(n) {
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
	for _, n := range toAnnounce(filtered) {
		notifySendForGitHub(n)
	}
	return filtered
}

func toAnnounce(ns []Notification) []Notification {
	if !notifsBaselined {
		for _, n := range ns {
			seenNotifs[n.ID] = n.UpdatedAt
		}
		notifsBaselined = true
		return nil
	}
	var fresh []Notification
	for _, n := range ns {
		if seenNotifs[n.ID] == n.UpdatedAt {
			continue
		}
		seenNotifs[n.ID] = n.UpdatedAt
		fresh = append(fresh, n)
	}
	return fresh
}

// foldNotifications splits notifications into reasons keyed by the URL of a
// listed PR or review request, and the rest. A notification about your own PR
// names a PR that is already listed, so the two queries overlap and the same
// thing would be shown twice; its reason goes on the PR's own line instead.
func foldNotifications(prs []PR, reviews []ReviewRequest, notifs []Notification) (map[string]string, []Notification) {
	listed := make(map[string]bool, len(prs)+len(reviews))
	for _, pr := range prs {
		listed[pr.URL] = true
	}
	for _, r := range reviews {
		listed[r.URL] = true
	}
	reasons := make(map[string]string)
	var loose []Notification
	for _, n := range notifs {
		if url := subjectWebURL(n); listed[url] {
			reasons[url] += " " + glyphNotif + " " + n.Reason
			continue
		}
		loose = append(loose, n)
	}
	return reasons, loose
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
		"--json=title,url,number,repository,createdAt,isDraft",
		"--limit=100",
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

type scope struct {
	name   string
	owners map[string]bool
}

func (s scope) keeps(repo string) bool {
	if s.name == "" {
		return true
	}
	owner, _, _ := strings.Cut(repo, "/")
	return s.owners[strings.ToLower(owner)] == (s.name == "work")
}

func keepPRs(prs []PR, s scope) []PR {
	var out []PR
	for _, pr := range prs {
		if s.keeps(pr.Repository.NameWithOwner) {
			out = append(out, pr)
		}
	}
	return out
}

func keepRepos(repos []string, s scope) []string {
	var out []string
	for _, r := range repos {
		if s.keeps(r) {
			out = append(out, r)
		}
	}
	return out
}

func isApproved(pr PR, approved []PR) bool {
	for _, a := range approved {
		if a.URL == pr.URL {
			return true
		}
	}
	return false
}

func cacheFilePath(scope string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	if scope != "" {
		return filepath.Join(dir, "waybar-github-prs-"+scope+".json")
	}
	return filepath.Join(dir, "waybar-github-prs.json")
}

type PRCache struct {
	All           []PR            `json:"all"`
	Approved      []PR            `json:"approved"`
	Reviews       []ReviewRequest `json:"reviews,omitempty"`
	Notifications []Notification  `json:"notifications,omitempty"`
	Runs          []Run           `json:"runs,omitempty"`
	Pending       []PendingRoot   `json:"pending,omitempty"`
}

func writePRCache(scope string, all, approved []PR, reviews []ReviewRequest, notifs []Notification, runs []Run, pending []PendingRoot) {
	data, err := json.Marshal(PRCache{
		All: all, Approved: approved, Reviews: reviews, Notifications: notifs, Runs: runs, Pending: pending,
	})
	if err != nil {
		log.Printf("Error marshaling PR cache: %s", err)
		return
	}
	if err := os.WriteFile(cacheFilePath(scope), data, 0600); err != nil {
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

// pickerWidth is the fuzzel window width, in characters. Wide enough for the
// longest shape a row takes — glyph, age, `[owner/repo]` and a title — so
// what gets ellipsized is the tail of a title rather than the part that says
// which repo the row belongs to.
const pickerWidth = 120

const pickerMaxLines = 40

const glyphNotif = "\U000f009c"

const glyphDraft = "◌"

type item struct {
	label string
	url   string
	// Non-zero for a failed run: opening it is also how you acknowledge
	// it, which is what stops the daemon resurfacing it next tick.
	dismiss int64
	// Set for a root pending apply, or for all of them on the apply-all row.
	// Selecting it starts the workflow rather than opening a page, so it is a
	// side effect, not a link.
	dispatch []PendingRoot
	// A labelled rule introducing a group. dmenu has no notion of an inert
	// line, so selecting one re-opens the picker instead of acting.
	divider bool
}

// section is one group of rows. title labels the divider drawn above the
// group, which is only rendered when a second group exists to separate it
// from.
type section struct {
	title string
	items []item
}

// row lays an entry out as glyph, a right-aligned age column, then the text.
// The age goes before the text, not after it, because an age on the far side
// of a long title is the first thing fuzzel ellipsizes away — which would
// lose exactly the context the column exists to add.
func row(glyph string, when time.Time, text string) string {
	return fmt.Sprintf("%s %4s  %s", glyph, humanAge(when), text)
}

// rowTextWidth is what a row has left for its text once row() has spent the
// leading glyph, the age column and the gutter. It stops short of pickerWidth
// because fuzzel's width counts characters and excludes the window's own
// padding, so drawing to the full width would truncate.
const rowTextWidth = pickerWidth - 8

// fitText composes a row's text as head + an elastic middle + suffix, cutting
// the middle when the row would overflow. The suffix is where a row's state
// lives — a merge-queue position, a refusal reason, a needs-approval
// environment — and the middle is a title, so the title is what gives way.
// Letting fuzzel ellipsize the right-hand end instead would drop precisely
// the part that makes the row worth reading.
func fitText(head, middle, suffix string) string {
	room := rowTextWidth - len([]rune(head)) - len([]rune(suffix))
	if room < 12 {
		room = 12 // a suffix long enough to squeeze the title out entirely
	}
	return head + trimRunes(middle, room) + suffix
}

// divider draws the rule that introduces a group, to a fixed length so the
// rules line up with each other.
func divider(title string) string {
	const (
		lead   = 4
		budget = rowTextWidth
	)
	title = trimRunes(title, budget-2*lead-2)
	tail := budget - lead - 2 - len([]rune(title))
	if tail < lead {
		tail = lead
	}
	return fmt.Sprintf("%s %s %s",
		strings.Repeat("─", lead), title, strings.Repeat("─", tail))
}

// openPRs opens a fuzzel picker that merges the cached open PRs with any
// unread notifications, workflow runs and roots pending apply. Selecting an
// entry opens the relevant URL in the default browser.
func openPRs(scope string, hide *regexp.Regexp) {
	data, err := os.ReadFile(cacheFilePath(scope))
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}

	var cache PRCache
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("Error reading cache: %s", err)
		return
	}

	cache, hidden := withoutTitles(cache, hide)
	prompt := "GitHub > "
	if hidden > 0 {
		prompt = fmt.Sprintf("GitHub · %d hidden > ", hidden)
	}

	items := pickerItems(cache)
	if len(items) == 0 {
		return
	}

	// The single-item shortcut skips the menu entirely, which is fine for a
	// link but not for an entry that starts a terraform run — one click on the
	// pill would dispatch against prod with nothing shown. Only take the
	// shortcut when the lone item is inert. A one-item list never carries a
	// divider, so this cannot fire on one.
	if len(items) == 1 && items[0].dispatch == nil {
		openItem(items[0].url, items[0].dismiss)
		return
	}

	for {
		it, ok := pick(items, prompt)
		if !ok {
			return // cancelled, or typed something that matched nothing
		}
		if it.divider {
			continue
		}
		if it.dispatch != nil {
			dispatchApplies(it.dispatch)
			return
		}
		openItem(it.url, it.dismiss)
		return
	}
}

func withoutTitles(cache PRCache, hide *regexp.Regexp) (PRCache, int) {
	if hide == nil {
		return cache, 0
	}
	gone := make(map[string]bool)
	var all []PR
	for _, pr := range cache.All {
		if hide.MatchString(pr.Title) {
			gone[pr.URL] = true
			continue
		}
		all = append(all, pr)
	}
	var reviews []ReviewRequest
	for _, r := range cache.Reviews {
		if hide.MatchString(r.Title) {
			gone[r.URL] = true
			continue
		}
		reviews = append(reviews, r)
	}
	var notifs []Notification
	for _, n := range cache.Notifications {
		if gone[subjectWebURL(n)] || (n.Subject.Type == "PullRequest" && hide.MatchString(n.Subject.Title)) {
			continue
		}
		notifs = append(notifs, n)
	}
	cache.All, cache.Reviews, cache.Notifications = all, reviews, notifs
	return cache, len(gone)
}

// pickerItems turns one poll's cache into the rows the picker offers, in the
// order they appear: open PRs, drafts, review requests, workflow runs, roots
// pending apply, then notifications, each group after the first introduced by
// a divider.
func pickerItems(cache PRCache) []item {
	// Resolved before the PR rows are built rather than patched onto them
	// afterwards, so a reason is part of the suffix each row is sized around
	// instead of a tail hung past the window's edge.
	reasons, loose := foldNotifications(cache.All, cache.Reviews, cache.Notifications)

	prs := section{title: "Pull requests"}
	drafts := section{title: "Drafts"}
	for _, pr := range cache.All {
		prefix := "○"
		switch {
		case pr.IsDraft:
			prefix = glyphDraft
		case isApproved(pr, cache.Approved):
			prefix = "✓"
		}
		suffix := ""
		if pr.Queue != nil {
			suffix = " · " + pr.Queue.Summary()
		}
		suffix += commentsSuffix(pr.Comments) + reasons[pr.URL]
		it := item{
			label: row(prefix, parseGHTime(pr.CreatedAt),
				fitText(prHead(pr), pr.Title, suffix)),
			url: pr.URL,
		}
		if pr.IsDraft {
			drafts.items = append(drafts.items, it)
		} else {
			prs.items = append(prs.items, it)
		}
	}

	reviews := section{title: "Review requested"}
	for _, r := range cache.Reviews {
		reviews.items = append(reviews.items, item{
			label: row(glyphReview, parseGHTime(r.UpdatedAt),
				fitText(r.Head(), r.Title, r.Suffix()+reasons[r.URL])),
			url: r.URL,
		})
	}

	runs := section{title: "Workflow runs"}
	for _, r := range cache.Runs {
		// The run ID tells two runs of the same workflow apart at a glance.
		// It is no longer load-bearing — selection goes by index, not by
		// label — so it can go if the row ever needs the width.
		prefix, suffix := "󰑐", ""
		var dismiss int64
		switch {
		case r.NeedsMe:
			prefix = "󰥔"
			suffix = " · needs approval"
			if r.Environment != "" {
				suffix = " · needs approval (" + r.Environment + ")"
			}
		case r.Failed:
			prefix = "󰀨"
			suffix = " · " + r.Conclusion
			dismiss = r.ID
		}
		runs.items = append(runs.items, item{
			label: row(prefix, r.When(), fitText(fmt.Sprintf("[%s] ", r.Repo),
				r.Title(), suffix+fmt.Sprintf(" #%d", r.ID))),
			url:     r.HTMLURL,
			dismiss: dismiss,
		})
	}

	applies := section{title: "Needs terraform apply"}
	for _, p := range cache.Pending {
		applies.items = append(applies.items, item{
			label: row(glyphApply, parseGHTime(p.NewestWhen),
				fitText(fmt.Sprintf("[%s] ", p.Repo), p.Root,
					fmt.Sprintf(" · needs apply (%d commit(s))", p.Commits))),
			url:      p.CompareURL,
			dispatch: []PendingRoot{p},
		})
	}
	if n := len(cache.Pending); n > 1 {
		applies.items = append(applies.items, item{
			label: row(glyphApply, time.Time{},
				fmt.Sprintf("[%s] all %d roots · dispatch every apply", cache.Pending[0].Repo, n)),
			dispatch: cache.Pending,
		})
	}

	notifs := section{title: "Notifications"}
	for _, n := range loose {
		notifs.items = append(notifs.items, item{
			label: row("󰂜", parseGHTime(n.UpdatedAt),
				fitText(fmt.Sprintf("[%s] [%s] ", n.Reason, n.Repository.FullName),
					n.Subject.Title, "")),
			url: subjectWebURL(n),
		})
	}

	var populated []section
	for _, s := range []section{prs, drafts, reviews, runs, applies, notifs} {
		if len(s.items) > 0 {
			populated = append(populated, s)
		}
	}

	var items []item
	for i, s := range populated {
		// No divider above the first group: it separates nothing, and it
		// would sit under the cursor fuzzel opens with, so Enter on the
		// default selection would do nothing at all.
		if i > 0 {
			items = append(items, item{label: divider(s.title), divider: true})
		}
		items = append(items, s.items...)
	}
	return items
}

// prHead names the PR a row belongs to. The number is what makes two rows
// distinguishable when the titles match, and the base branch is what makes
// them meaningful: a fix backported across release branches is one title on
// several PRs, and the branch is the only thing separating them.
func prHead(pr PR) string {
	head := "[" + pr.Repository.NameWithOwner
	if pr.Number > 0 {
		head += fmt.Sprintf("#%d", pr.Number)
	}
	if pr.Base != "" {
		head += " → " + pr.Base
	}
	return head + "] "
}

// pick shows the menu and returns the chosen item.
func pick(items []item, prompt string) (item, bool) {
	entries := make([]string, 0, len(items))
	for _, it := range items {
		entries = append(entries, it.label)
	}

	lines := min(len(items), pickerMaxLines)
	cmd := exec.Command("fuzzel", "--dmenu", "--index",
		fmt.Sprintf("--width=%d", pickerWidth), fmt.Sprintf("--lines=%d", lines), "--prompt", prompt)
	cmd.Stdin = strings.NewReader(strings.Join(entries, "\n"))
	out, err := cmd.Output()
	if err != nil {
		return item{}, false // user cancelled
	}
	return resolve(items, string(out))
}

// resolve maps fuzzel's --index output back to the item it names.
//
// Asking for the index rather than the entry text is what makes selection
// safe: matching the returned line against the labels fed in silently
// resolved to the first of two identical ones, and two PRs can legitimately
// share a title — the same fix backported to two release branches renders
// identically once the ages round the same way.
//
// Anything that is not an index in range means nothing was chosen: no match
// for what was typed, an empty line, or a fuzzel that answered with text.
func resolve(items []item, out string) (item, bool) {
	i, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || i < 0 || i >= len(items) {
		return item{}, false
	}
	return items[i], true
}

// openItem hands a URL to the browser and, for a failed run, records the
// dismissal first — the write must land before the daemon's next tick, so it
// is done synchronously while xdg-open is not.
func openItem(url string, dismiss int64) {
	if dismiss != 0 {
		dismissRun(dismiss)
	}
	exec.Command("xdg-open", url).Start()
}

// parseGHTime parses one of the RFC3339 timestamps GitHub returns, yielding
// the zero time for anything it cannot read. An age is decoration, so a
// timestamp in a surprising shape must cost the decoration, not the row.
func parseGHTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// humanAge renders an age as a single coarse unit — minutes below an hour,
// hours below a day, days above — so it always fits the fixed-width column
// the picker aligns on. Empty for an unknown time, which pads to blank.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
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

const tooltipTitleRunes = 80

func unwrapped(lines []string) string {
	return `<span allow_breaks="false">` + strings.Join(lines, "\n") + `</span>`
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
