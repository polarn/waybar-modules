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
		prs, err := fetchApprovedPRs()
		if err != nil {
			log.Printf("Error fetching PRs: %s", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		var tooltips []string
		for _, pr := range prs {
			log.Printf("%s: %s - %s", pr.Repository.NameWithOwner, pr.Title, pr.URL)
			tooltips = append(tooltips, fmt.Sprintf("[%s] %s", pr.Repository.NameWithOwner, pr.Title))
		}

		status := "none"
		if len(prs) > 0 {
			status = "found"
		}

		writePRCache(prs)

		w := waybar.New()
		w.Text = fmt.Sprintf("%d", len(prs))
		w.ToolTip = strings.Join(tooltips, "\n")
		w.Class = status
		w.Alt = status

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func fetchApprovedPRs() ([]PR, error) {
	cmd := exec.Command("gh", "search", "prs",
		"--review=approved",
		"--state=open",
		"--author=@me",
		"--json=title,url,repository",
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh search: %w", err)
	}

	var prs []PR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	return prs, nil
}

func cacheFilePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "waybar-github-prs.json")
}

func writePRCache(prs []PR) {
	data, err := json.Marshal(prs)
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

	var prs []PR
	if err := json.Unmarshal(data, &prs); err != nil {
		log.Printf("Error reading PR cache: %s", err)
		return
	}

	if len(prs) == 0 {
		return
	}

	if len(prs) == 1 {
		exec.Command("xdg-open", prs[0].URL).Start()
		return
	}

	// Build rofi menu: "label\0info\0url"
	var entries []string
	for _, pr := range prs {
		entries = append(entries, fmt.Sprintf("[%s] %s", pr.Repository.NameWithOwner, pr.Title))
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
			exec.Command("xdg-open", prs[i].URL).Start()
			return
		}
	}
}
