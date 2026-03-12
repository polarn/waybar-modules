package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
	"github.com/xanzy/go-gitlab"
)

func main() {
	accessToken := os.Getenv("GITLAB_TOKEN")
	if len(accessToken) == 0 {
		log.Printf("GITLAB_TOKEN is not set, exiting...")
		os.Exit(1)
	}

	var interval int
	flag.IntVar(&interval, "interval", 60, "Interval of polling")
	flag.Parse()

	client, err := gitlab.NewClient(accessToken)
	if err != nil {
		log.Printf("Error: %s", err)
		os.Exit(1)
	}

	user, _, err := client.Users.CurrentUser()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	log.Printf("Found user ID: %d, name: %s", user.ID, user.Name)

	for {
		mergeRequests, _, err := client.MergeRequests.ListMergeRequests(&gitlab.ListMergeRequestsOptions{
			ReviewerID: gitlab.ReviewerID(user.ID),
			Scope:      gitlab.String("all"),
			State:      gitlab.String("opened"),
		})
		if err != nil {
			log.Println(err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		var tooltips []string
		for i, mr := range mergeRequests {
			log.Printf("%d: %s - %s\n", i, mr.Title, mr.WebURL)
			tooltips = append(tooltips, mr.Title)
		}

		status := "none"
		if len(mergeRequests) > 0 {
			status = "found"
		}

		w := waybar.New()
		w.Text = fmt.Sprintf("%d", len(mergeRequests))
		w.ToolTip = strings.Join(tooltips, "\n")
		w.Class = status
		w.Alt = status

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}
}
