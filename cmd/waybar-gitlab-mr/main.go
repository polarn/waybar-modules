package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
)

type MR struct {
	Title      string `json:"title"`
	WebURL     string `json:"web_url"`
	IID        int    `json:"iid"`
	ProjectID  int    `json:"project_id"`
	References struct {
		Full string `json:"full"`
	} `json:"references"`
}

// Approvals subset of /projects/:id/merge_requests/:iid/approvals — we just
// need to know whether anyone has approved.
type Approvals struct {
	ApprovedBy []struct{} `json:"approved_by"`
}

// Todo mirrors GitLab's /todos response (the equivalent of GitHub's
// notifications). action_name examples: "mentioned", "review_requested",
// "assigned", "approval_required", "build_failed", "directly_addressed".
type Todo struct {
	ID         int    `json:"id"`
	ActionName string `json:"action_name"`
	State      string `json:"state"`
	Project    struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	TargetType string `json:"target_type"`
	Target     struct {
		Title string `json:"title"`
		// State is "opened"/"closed"/"merged"/"locked" for MRs,
		// "opened"/"closed" for issues. Empty for stateless target types.
		State string `json:"state"`
	} `json:"target"`
	TargetURL string `json:"target_url"`
}

type User struct {
	Username string `json:"username"`
}

// Tracks todo IDs we've already notify-send'd for. Lives for the daemon's
// lifetime; restart loses state. First poll baselines (marks all current as
// seen) so the user doesn't get a flood for pre-existing pending todos.
var (
	seenTodos      = make(map[int]bool)
	todosBaselined = false
)

func main() {
	var interval int
	var open bool
	var notify bool
	var notifyReasonsCSV string
	flag.IntVar(&interval, "interval", 120, "Interval of polling in seconds")
	flag.BoolVar(&open, "open", false, "Open MRs interactively and exit")
	flag.BoolVar(&notify, "notify", true, "Fire notify-send for new GitLab todos")
	flag.StringVar(&notifyReasonsCSV, "notify-reasons",
		"mentioned,directly_addressed,review_requested,assigned,approval_required,build_failed",
		"Comma-separated action_names that produce notify-send + count toward the pill")
	flag.Parse()

	if open {
		openMRs()
		return
	}

	notifyReasons := map[string]bool{}
	for _, r := range strings.Split(notifyReasonsCSV, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			notifyReasons[r] = true
		}
	}

	username, err := currentUsername()
	if err != nil {
		log.Fatalf("Could not get current user: %s", err)
	}
	log.Printf("Authenticated as: %s", username)

	for {
		all, err := fetchAuthoredMRs(username)
		if err != nil {
			log.Printf("Error fetching MRs: %s", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		var approved []MR
		for _, mr := range all {
			if mrApproved(mr) {
				approved = append(approved, mr)
			}
		}

		var tooltips []string
		for _, mr := range all {
			log.Printf("%s: %s - %s", mr.References.Full, mr.Title, mr.WebURL)
			prefix := "  "
			if isApproved(mr, approved) {
				prefix = "✓ "
			}
			line := fmt.Sprintf("%s[%s] %s", prefix, mr.References.Full, mr.Title)
			if len(line) > 70 {
				line = line[:67] + "..."
			}
			tooltips = append(tooltips, line)
		}

		status := "none"
		if len(approved) > 0 {
			status = "found"
		}

		todos := processTodos(all, notifyReasons, notify)
		writeCache(all, approved, todos)

		text := fmt.Sprintf("%d·%d", len(approved), len(all))
		if len(todos) > 0 {
			text += fmt.Sprintf(" 󰂜 %d", len(todos))
		}

		if len(todos) > 0 {
			tooltips = append(tooltips, "")
			tooltips = append(tooltips, "<b>Todos</b>")
			for _, t := range todos {
				line := fmt.Sprintf("  [%s] %s · %s", t.ActionName, t.Project.PathWithNamespace, t.Target.Title)
				if len(line) > 80 {
					line = line[:77] + "..."
				}
				tooltips = append(tooltips, line)
			}
		}

		w := waybar.New()
		w.Text = text
		w.ToolTip = strings.Join(tooltips, "\n")
		w.Class = status
		w.Alt = status

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

// glabAPI invokes `glab api <path>` and decodes the JSON response into v.
// Uses the user's existing glab auth (no separate GITLAB_TOKEN env needed).
func glabAPI(path string, v interface{}) error {
	out, err := exec.Command("glab", "api", path).Output()
	if err != nil {
		return fmt.Errorf("glab api %s: %w", path, err)
	}
	return json.Unmarshal(out, v)
}

func currentUsername() (string, error) {
	var u User
	if err := glabAPI("/user", &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

func fetchAuthoredMRs(username string) ([]MR, error) {
	params := url.Values{}
	params.Set("author_username", username)
	params.Set("state", "opened")
	params.Set("scope", "all")
	params.Set("non_archived", "true")
	params.Set("per_page", "100")
	var mrs []MR
	if err := glabAPI("/merge_requests?"+params.Encode(), &mrs); err != nil {
		return nil, err
	}
	return mrs, nil
}

// mrApproved returns true if at least one user has approved the MR
// (analogous to GitHub's "approved" review state).
func mrApproved(mr MR) bool {
	var a Approvals
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/approvals", mr.ProjectID, mr.IID)
	if err := glabAPI(path, &a); err != nil {
		log.Printf("approvals %s: %s", path, err)
		return false
	}
	return len(a.ApprovedBy) > 0
}

func isApproved(mr MR, approved []MR) bool {
	for _, a := range approved {
		if a.WebURL == mr.WebURL {
			return true
		}
	}
	return false
}

func fetchTodos() ([]Todo, error) {
	var todos []Todo
	if err := glabAPI("/todos?state=pending&per_page=100", &todos); err != nil {
		return nil, err
	}
	return todos, nil
}

func processTodos(authored []MR, reasons map[string]bool, notify bool) []Todo {
	all, err := fetchTodos()
	if err != nil {
		log.Printf("todos: %s", err)
		return nil
	}
	authoredURLs := make(map[string]bool, len(authored))
	for _, mr := range authored {
		authoredURLs[mr.WebURL] = true
	}
	var filtered []Todo
	for _, t := range all {
		if !reasons[t.ActionName] {
			continue
		}
		// Drop "assigned" todos for my own open MRs — self-assigning on MR
		// creation spawns one per MR, duplicating the authored list above.
		if t.ActionName == "assigned" && authoredURLs[t.TargetURL] {
			continue
		}
		// Drop todos whose target is already merged/closed — GitLab leaves
		// "review_requested" pending forever otherwise. Empty target.State
		// means a stateless target type (Epic, Design, etc.); let those pass.
		if t.Target.State != "" && t.Target.State != "opened" {
			continue
		}
		filtered = append(filtered, t)
	}
	if !notify {
		return filtered
	}
	if !todosBaselined {
		for _, t := range filtered {
			seenTodos[t.ID] = true
		}
		todosBaselined = true
		return filtered
	}
	for _, t := range filtered {
		if seenTodos[t.ID] {
			continue
		}
		seenTodos[t.ID] = true
		notifySendForGitLab(t)
	}
	return filtered
}

func notifySendForGitLab(t Todo) {
	title := titleForActionName(t.ActionName)
	body := fmt.Sprintf("[%s] %s", t.Project.PathWithNamespace, t.Target.Title)
	cmd := exec.Command("notify-send",
		"--app-name", "GitLab",
		"--icon", "gitlab",
		"--category", "im.received",
		title, body)
	if err := cmd.Run(); err != nil {
		log.Printf("notify-send failed: %s", err)
	}
}

func titleForActionName(action string) string {
	switch action {
	case "mentioned":
		return "GitLab: you were mentioned"
	case "directly_addressed":
		return "GitLab: directly addressed"
	case "review_requested":
		return "GitLab: review requested"
	case "assigned":
		return "GitLab: assigned"
	case "approval_required":
		return "GitLab: approval required"
	case "build_failed":
		return "GitLab: build failed"
	case "unmergeable":
		return "GitLab: unmergeable"
	case "marked":
		return "GitLab: marked todo"
	default:
		return "GitLab: " + action
	}
}

func cacheFilePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "waybar-gitlab-mrs.json")
}

type MRCache struct {
	All      []MR   `json:"all"`
	Approved []MR   `json:"approved"`
	Todos    []Todo `json:"todos,omitempty"`
}

func writeCache(all, approved []MR, todos []Todo) {
	data, err := json.Marshal(MRCache{All: all, Approved: approved, Todos: todos})
	if err != nil {
		log.Printf("Error marshaling cache: %s", err)
		return
	}
	if err := os.WriteFile(cacheFilePath(), data, 0600); err != nil {
		log.Printf("Error writing cache: %s", err)
	}
}

// openMRs opens a fuzzel picker that merges the cached open MRs with any
// pending todos. Selecting an entry opens the URL in the default browser.
func openMRs() {
	data, err := os.ReadFile(cacheFilePath())
	if err != nil {
		log.Printf("No cached items: %s", err)
		return
	}

	var cache MRCache
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("Error reading cache: %s", err)
		return
	}

	type item struct {
		label string
		url   string
	}
	var items []item
	for _, mr := range cache.All {
		prefix := "○"
		if isApproved(mr, cache.Approved) {
			prefix = "✓"
		}
		items = append(items, item{
			label: fmt.Sprintf("%s [%s] %s", prefix, mr.References.Full, mr.Title),
			url:   mr.WebURL,
		})
	}
	for _, t := range cache.Todos {
		items = append(items, item{
			label: fmt.Sprintf("󰂜 [%s] [%s] %s", t.ActionName, t.Project.PathWithNamespace, t.Target.Title),
			url:   t.TargetURL,
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

	cmd := exec.Command("fuzzel", "--dmenu", "--width=75", "--prompt", "GitLab > ")
	cmd.Stdin = strings.NewReader(strings.Join(entries, "\n"))
	out, err := cmd.Output()
	if err != nil {
		return
	}

	selected := strings.TrimSpace(string(out))
	for _, it := range items {
		if it.label == selected {
			exec.Command("xdg-open", it.url).Start()
			return
		}
	}
}
