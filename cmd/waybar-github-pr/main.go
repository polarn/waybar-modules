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

func main() {
	var interval int
	var open bool
	flag.IntVar(&interval, "interval", 120, "Interval of polling in seconds")
	flag.BoolVar(&open, "open", false, "Open PRs interactively and exit")
	flag.Parse()

	if open {
		openPRs()
		return
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

		writePRCache(all, approved)

		w := waybar.New()
		w.Text = fmt.Sprintf("%d / %d", len(approved), len(all))
		w.ToolTip = strings.Join(tooltips, "\n")
		w.Class = status
		w.Alt = status

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(interval) * time.Second)
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
	All      []PR `json:"all"`
	Approved []PR `json:"approved"`
}

func writePRCache(all, approved []PR) {
	data, err := json.Marshal(PRCache{All: all, Approved: approved})
	if err != nil {
		log.Printf("Error marshaling PR cache: %s", err)
		return
	}
	if err := os.WriteFile(cacheFilePath(), data, 0600); err != nil {
		log.Printf("Error writing PR cache: %s", err)
	}
}

func openPRs() {
	data, err := os.ReadFile(cacheFilePath())
	if err != nil {
		log.Printf("No cached PRs: %s", err)
		return
	}

	var cache PRCache
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("Error reading PR cache: %s", err)
		return
	}

	if len(cache.All) == 0 {
		return
	}

	if len(cache.All) == 1 {
		exec.Command("xdg-open", cache.All[0].URL).Start()
		return
	}

	var entries []string
	for _, pr := range cache.All {
		prefix := "○"
		if isApproved(pr, cache.Approved) {
			prefix = "✓"
		}
		entries = append(entries, fmt.Sprintf("%s [%s] %s", prefix, pr.Repository.NameWithOwner, pr.Title))
	}

	cmd := exec.Command("rofi", "-dmenu", "-p", "Open PR", "-i")
	cmd.Stdin = strings.NewReader(strings.Join(entries, "\n"))
	out, err := cmd.Output()
	if err != nil {
		return // user cancelled
	}

	selected := strings.TrimSpace(string(out))
	for i, entry := range entries {
		if entry == selected {
			exec.Command("xdg-open", cache.All[i].URL).Start()
			return
		}
	}
}
