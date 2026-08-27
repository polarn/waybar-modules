package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Run is the subset of a GitHub Actions workflow run we surface in the pill,
// plus two fields we resolve ourselves (Repo, Environment) because the runs
// listing doesn't carry them in a usable form.
type Run struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	Status       string `json:"status"`     // queued | in_progress | waiting | completed
	Conclusion   string `json:"conclusion"` // null while running; failure/success/... when completed
	UpdatedAt    string `json:"updated_at"`
	HTMLURL      string `json:"html_url"`

	Repo        string `json:"repo"`        // owner/name; filled in by us
	Environment string `json:"environment"` // only set for approval-pending runs
	NeedsMe     bool   `json:"needs_me"`    // current_user_can_approve
	Failed      bool   `json:"failed"`      // finished badly, sticks until dismissed
}

// Title prefers display_title (the rendered run title, e.g. "terraform apply
// google/monitoring-prod (@polarn)") and falls back to the workflow name.
func (r Run) Title() string {
	if r.DisplayTitle != "" {
		return r.DisplayTitle
	}
	return r.Name
}

// Tracks run IDs we've already notify-send'd an approval request for. Same
// shape and same reasoning as seenNotifs in main.go: on the first poll after
// start we baseline, so a daemon restart (make install pkills it) doesn't
// re-notify for a gate that was already waiting.
//
// Deliberately a separate map from seenNotifs — run IDs and notification IDs
// are different key spaces, and sharing one map would let either baseline the
// other.
var (
	seenApprovals      = make(map[int64]bool)
	approvalsBaselined = false

	// Earliest time a failed discovery sweep may be retried. Without this a
	// gh outage turns every 60s tick into another ~70 doomed calls, because
	// the cache stays stale for as long as the failure lasts.
	nextDiscovery time.Time

	// Memoises pending_deployments per run, keyed on the run's updated_at so
	// a state transition forces a re-check. Same trick as subjectDone in
	// main.go: a run that sits in the gate for an hour costs one call, not
	// one per tick. Only ever touched from the serial poll loop.
	pendingMemo = make(map[string]pendingResult)
)

type pendingResult struct {
	env string
	can bool
}

// runsResult carries a tick's worth of run state. Complete is false when at
// least one watched repo failed to answer, in which case the caller omits the
// run segment entirely rather than rendering a count that is quietly missing
// a repo's runs — an absent value must not become a confident zero.
// ghTimeout bounds a single gh invocation. Nothing in the runs path may block
// a tick indefinitely — the pill's PR counts must keep updating regardless.
const ghTimeout = 20 * time.Second

type runsResult struct {
	Runs     []Run
	Complete bool
}

// ---------------------------------------------------------------------------
// Repo discovery
// ---------------------------------------------------------------------------

// repoCache persists the discovered repo set so a daemon restart doesn't redo
// the ~70-call sweep. Kept under XDG_CACHE_HOME rather than XDG_RUNTIME_DIR
// precisely because the latter is cleared on logout, which would make the
// cache useless for the case it exists to cover.
type repoCache struct {
	Login     string    `json:"login"`
	Repos     []string  `json:"repos"`
	Refreshed time.Time `json:"refreshed"`
}

func repoCachePath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "waybar-github-gated-repos.json")
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "waybar-github-modules", "gated-repos.json")
}

func readRepoCache() (repoCache, bool) {
	var c repoCache
	data, err := os.ReadFile(repoCachePath())
	if err != nil {
		return c, false
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false
	}
	return c, true
}

func writeRepoCache(c repoCache) {
	path := repoCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("Error creating repo cache dir: %s", err)
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		log.Printf("Error marshaling repo cache: %s", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("Error writing repo cache: %s", err)
	}
}

// ghJSON runs `gh api <args...>` and decodes stdout into out.
//
// Two things gh does that make the naive version unhelpful: on failure it
// writes the error body to stdout and the human-readable message to stderr,
// so .Output() alone reduces a "404 Not Found" to "exit status 1" — with ~70
// subprocesses in a discovery sweep that is miserable to debug. And a wedged
// gh would otherwise block a tick forever, so the call is bounded.
func ghJSON(out any, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", append([]string{"api"}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	data, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	return json.Unmarshal(data, out)
}

// currentLogin resolves the authenticated user's login. Note that the literal
// "@me" convention understood by `gh search prs` does NOT work on the REST
// runs endpoint — it silently matches nothing — so the real login is required.
func currentLogin() (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := ghJSON(&u, "user"); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", fmt.Errorf("empty login from gh api user")
	}
	return u.Login, nil
}

// discoverRepos finds every non-archived repo, across the orgs the account
// belongs to, that has at least one environment gated on required_reviewers.
// Those are the only repos where a run can stop and wait for a human.
//
// There is no cross-repo API for workflow runs (GraphQL has no run search, and
// a deployment-review request produces no /notifications entry), so this sweep
// is how the module learns what to poll.
func discoverRepos() ([]string, error) {
	var orgs []struct {
		Login string `json:"login"`
	}
	if err := ghJSON(&orgs, "/user/orgs?per_page=100", "--paginate"); err != nil {
		return nil, fmt.Errorf("listing orgs: %w", err)
	}

	var candidates []string
	for _, o := range orgs {
		var repos []struct {
			FullName string `json:"full_name"`
			Archived bool   `json:"archived"`
		}
		path := fmt.Sprintf("/orgs/%s/repos?per_page=100", o.Login)
		if err := ghJSON(&repos, path, "--paginate"); err != nil {
			// One unreadable org shouldn't sink discovery of the others.
			log.Printf("Error listing repos for %s: %s", o.Login, err)
			continue
		}
		for _, r := range repos {
			if !r.Archived {
				candidates = append(candidates, r.FullName)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no candidate repos found")
	}

	// Probing ~70 repos serially takes ~30s; 8 at a time brings it to ~4s,
	// which is short enough to do inline on the tick where the cache expires.
	const workers = 8
	var (
		mu     sync.Mutex
		gated  []string
		wg     sync.WaitGroup
		tokens = make(chan struct{}, workers)
	)

	for _, repo := range candidates {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			tokens <- struct{}{}
			defer func() { <-tokens }()

			var envs struct {
				Environments []struct {
					Name            string `json:"name"`
					ProtectionRules []struct {
						Type string `json:"type"`
					} `json:"protection_rules"`
				} `json:"environments"`
			}
			// A repo with no environments 404s or returns an empty list —
			// both mean "not gated", not "failure", so this is quiet.
			if err := ghJSON(&envs, "/repos/"+repo+"/environments"); err != nil {
				return
			}
			for _, e := range envs.Environments {
				for _, p := range e.ProtectionRules {
					if p.Type == "required_reviewers" {
						mu.Lock()
						gated = append(gated, repo)
						mu.Unlock()
						return
					}
				}
			}
		}(repo)
	}
	wg.Wait()

	sort.Strings(gated)
	return gated, nil
}

// watchedRepos returns the repo set to poll this tick, refreshing the cached
// discovery when it is older than ttl. extra is unioned in so a repo without a
// gated environment (which discovery cannot find) can still be tracked.
func watchedRepos(extra []string, ttl time.Duration) (repos []string, login string) {
	c, ok := readRepoCache()
	now := time.Now()
	stale := !ok || now.Sub(c.Refreshed) > ttl

	if stale && now.After(nextDiscovery) {
		discovered, err := discoverRepos()
		if err != nil {
			// Serve the stale list rather than nothing, and back off.
			log.Printf("Error discovering gated repos: %s", err)
			nextDiscovery = now.Add(10 * time.Minute)
		} else {
			c.Repos = discovered
			c.Refreshed = time.Now()
		}
		if l, err := currentLogin(); err != nil {
			log.Printf("Error resolving GitHub login: %s", err)
		} else {
			c.Login = l
		}
		if c.Login != "" && c.Repos != nil {
			writeRepoCache(c)
		}
	}

	seen := make(map[string]bool, len(c.Repos)+len(extra))
	for _, r := range append(append([]string{}, c.Repos...), extra...) {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return repos, c.Login
}

// ---------------------------------------------------------------------------
// Run polling
// ---------------------------------------------------------------------------

type runListing struct {
	WorkflowRuns []Run `json:"workflow_runs"`
}

// fetchRuns collects, for every watched repo, the runs that need attention:
// those waiting on a deployment gate this user can approve, and those this
// user dispatched that are currently queued or in progress.
func fetchRuns(repos []string, login string) runsResult {
	res := runsResult{Complete: true}
	if len(repos) == 0 || login == "" {
		res.Complete = false
		return res
	}

	dismissed := readDismissed()

	for _, repo := range repos {
		// Approval candidates deliberately span ALL actors: a colleague's run
		// waiting on your review is exactly the case worth surfacing.
		var waiting runListing
		if err := ghJSON(&waiting, "/repos/"+repo+"/actions/runs?status=waiting&per_page=20"); err != nil {
			log.Printf("Error fetching waiting runs for %s: %s", repo, err)
			res.Complete = false
			continue
		}
		for _, r := range waiting.WorkflowRuns {
			r.Repo = repo
			if env, ok := approvableBy(repo, r.ID, r.UpdatedAt); ok {
				r.NeedsMe = true
				r.Environment = env
				res.Runs = append(res.Runs, r)
			}
		}

		// Running is scoped to what this user dispatched — without that the
		// list fills with dependabot, merge_group and per-PR check runs.
		var mine runListing
		path := fmt.Sprintf("/repos/%s/actions/runs?actor=%s&event=workflow_dispatch&per_page=20", repo, login)
		if err := ghJSON(&mine, path); err != nil {
			log.Printf("Error fetching dispatched runs for %s: %s", repo, err)
			res.Complete = false
			continue
		}
		for _, r := range mine.WorkflowRuns {
			switch {
			case r.Status == "queued" || r.Status == "in_progress" || r.Status == "pending":
				r.Repo = repo
				res.Runs = append(res.Runs, r)
			case r.Status == "completed" && failedConclusion(r.Conclusion):
				// A failed run must not vanish the way a successful one
				// does — a broken production apply going quiet is the
				// exact thing this pill exists to prevent. It sticks
				// until dismissed from the picker.
				if dismissed[r.ID] {
					continue
				}
				r.Repo = repo
				r.Failed = true
				res.Runs = append(res.Runs, r)
			}
		}
	}

	return res
}

// approvableBy reports whether the current user can approve a pending
// deployment on this run, and which environment is waiting.
//
// The endpoint 404s for any run without a pending deployment — the normal case
// for a run held by a wait timer rather than a reviewer — so an error here
// means "not mine", not "failed".
func approvableBy(repo string, runID int64, updatedAt string) (string, bool) {
	key := fmt.Sprintf("%s#%d@%s", repo, runID, updatedAt)
	if p, ok := pendingMemo[key]; ok {
		return p.env, p.can
	}

	var pending []struct {
		Environment struct {
			Name string `json:"name"`
		} `json:"environment"`
		CurrentUserCanApprove bool `json:"current_user_can_approve"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d/pending_deployments", repo, runID)
	if err := ghJSON(&pending, path); err != nil {
		// 404 here is the normal case for a run held by a wait timer rather
		// than a reviewer. Not cached: an error is not a known answer.
		return "", false
	}
	for _, p := range pending {
		if p.CurrentUserCanApprove {
			pendingMemo[key] = pendingResult{env: p.Environment.Name, can: true}
			return p.Environment.Name, true
		}
	}
	pendingMemo[key] = pendingResult{}
	return "", false
}

// splitRuns partitions runs into the three pill states.
func splitRuns(runs []Run) (approval, running, failed []Run) {
	for _, r := range runs {
		switch {
		case r.NeedsMe:
			approval = append(approval, r)
		case r.Failed:
			failed = append(failed, r)
		default:
			running = append(running, r)
		}
	}
	return approval, running, failed
}

// notifyApprovals fires notify-send once per run that has newly entered the
// needs-my-approval state. Baselines on the first poll for the same reason
// processNotifications does.
func notifyApprovals(approval []Run, enabled bool) {
	present := make(map[int64]bool, len(approval))
	for _, r := range approval {
		present[r.ID] = true
	}

	if !approvalsBaselined {
		seenApprovals = present
		approvalsBaselined = true
		return
	}

	for _, r := range approval {
		if seenApprovals[r.ID] {
			continue
		}
		seenApprovals[r.ID] = true
		if !enabled {
			continue
		}
		body := fmt.Sprintf("[%s] %s", r.Repo, r.Title())
		if r.Environment != "" {
			body += " · " + r.Environment
		}
		notifySend("Deployment approval needed", body)
	}

	// Keep the map from growing without bound over a long-lived daemon.
	for id := range seenApprovals {
		if !present[id] {
			delete(seenApprovals, id)
		}
	}
}

// failedConclusion reports whether a finished run ended in a way worth
// flagging. "cancelled" is excluded on purpose: you cancelled it, so you
// already know.
func failedConclusion(c string) bool {
	switch c {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// Dismissals live next to the discovery cache rather than in
// $XDG_RUNTIME_DIR, because a failure you have already looked at must not
// come back after a logout. The file is written only by the --open picker
// and read only by the daemon, so the two never contend for it.
func dismissedPath() string {
	return filepath.Join(filepath.Dir(repoCachePath()), "dismissed-runs.json")
}

// dismissRetention bounds the file. A run drops out of the 20-entry API
// window long before this, so anything older is dead weight.
const dismissRetention = 7 * 24 * time.Hour

func readDismissed() map[int64]bool {
	out := make(map[int64]bool)
	data, err := os.ReadFile(dismissedPath())
	if err != nil {
		return out
	}
	var raw map[string]time.Time
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for k, at := range raw {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || time.Since(at) > dismissRetention {
			continue
		}
		out[id] = true
	}
	return out
}

// dismissRun records that a failed run has been looked at, so the daemon
// stops surfacing it. Called from the --open path, which is a separate
// short-lived process, hence the read-modify-write.
func dismissRun(id int64) {
	raw := make(map[string]time.Time)
	if data, err := os.ReadFile(dismissedPath()); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	for k, at := range raw {
		if time.Since(at) > dismissRetention {
			delete(raw, k)
		}
	}
	raw[strconv.FormatInt(id, 10)] = time.Now()

	path := dismissedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("Error creating dismissal dir: %s", err)
		return
	}
	data, err := json.Marshal(raw)
	if err != nil {
		log.Printf("Error marshaling dismissals: %s", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("Error writing dismissals: %s", err)
	}
}

// ghCommand builds a plain `gh` invocation for the non-api subcommands.
// Unlike ghJSON this carries no timeout: its only caller is the short-lived
// --open process, where a hung gh blocks nothing but itself.
func ghCommand(args ...string) *exec.Cmd {
	return exec.Command("gh", args...)
}

// notifySend is the one place this module talks to the notification daemon,
// so every notification it raises is grouped under the same app and icon.
func notifySend(title, body string) {
	cmd := exec.Command("notify-send",
		"--app-name", "GitHub",
		"--icon", "github",
		"--category", "im.received",
		title, body)
	if err := cmd.Run(); err != nil {
		log.Printf("Error sending notification %q: %s", title, err)
	}
}
