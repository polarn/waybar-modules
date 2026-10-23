package main

import (
	"cmp"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
)

const (
	glyphReview     = "\uf4af"
	glyphDependabot = "\uf4be"
	glyphRenovate   = "\uee72"
)

type ReviewRequest struct {
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Number    int      `json:"number"`
	Repo      string   `json:"repo"`
	Author    string   `json:"author"`
	UpdatedAt string   `json:"updated_at"`
	Teams     []string `json:"teams,omitempty"`
}

func (r ReviewRequest) Head() string {
	return fmt.Sprintf("[%s#%d] ", r.Repo, r.Number)
}

func (r ReviewRequest) Dependabot() bool {
	return r.Author == "dependabot"
}

func (r ReviewRequest) Renovate() bool {
	return r.Author == "renovate" || strings.HasSuffix(r.Author, "-renovate")
}

func (r ReviewRequest) Glyph() string {
	switch {
	case r.Dependabot():
		return glyphDependabot
	case r.Renovate():
		return glyphRenovate
	}
	return glyphReview
}

func (r ReviewRequest) Suffix() string {
	s := ""
	if r.Author != "" && !r.Dependabot() && !r.Renovate() {
		s += " · " + r.Author
	}
	if len(r.Teams) > 0 {
		s += " · for " + strings.Join(r.Teams, ", ")
	}
	return s
}

func splitBots(reqs []ReviewRequest) (people, dependabot, renovate []ReviewRequest) {
	for _, r := range reqs {
		switch {
		case r.Dependabot():
			dependabot = append(dependabot, r)
		case r.Renovate():
			renovate = append(renovate, r)
		default:
			people = append(people, r)
		}
	}
	return people, dependabot, renovate
}

const groupMin = 3

var ticketKey = regexp.MustCompile(`\[?\b[A-Z][A-Z0-9]+-\d+\b\]?`)

func splitTickets(title string) (rest, tickets string) {
	keys := ticketKey.FindAllString(title, -1)
	for i, k := range keys {
		keys[i] = strings.Trim(k, "[]")
	}
	rest = strings.Join(strings.Fields(ticketKey.ReplaceAllString(title, " ")), " ")
	return rest, strings.Join(keys, " ")
}

type reviewGroup struct {
	title  string
	reqs   []ReviewRequest
	fanOut bool
}

func (g reviewGroup) row(r ReviewRequest) (head, middle string) {
	if !g.fanOut {
		return fmt.Sprintf("#%d ", r.Number), r.Title
	}
	if _, tickets := splitTickets(r.Title); tickets != "" {
		middle = " " + tickets
	}
	return strings.TrimSuffix(r.Head(), " "), middle
}

func byRepoAndNumber(a, b ReviewRequest) int {
	return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Number, b.Number))
}

func groupReviews(reqs []ReviewRequest) (loose []ReviewRequest, groups []reviewGroup) {
	taken := make(map[string]bool)
	take := func(rs []ReviewRequest) {
		for _, r := range rs {
			taken[r.URL] = true
		}
	}

	byTitle := make(map[string][]ReviewRequest)
	for _, r := range reqs {
		if t, _ := splitTickets(r.Title); t != "" {
			byTitle[t] = append(byTitle[t], r)
		}
	}
	var fanOuts []reviewGroup
	for t, rs := range byTitle {
		repos := make(map[string]bool)
		for _, r := range rs {
			repos[r.Repo] = true
		}
		if len(repos) < groupMin {
			continue
		}
		slices.SortFunc(rs, byRepoAndNumber)
		fanOuts = append(fanOuts, reviewGroup{title: fmt.Sprintf("%s · %d repos", t, len(repos)), reqs: rs, fanOut: true})
		take(rs)
	}

	byRepo := make(map[string][]ReviewRequest)
	for _, r := range reqs {
		if !taken[r.URL] {
			byRepo[r.Repo] = append(byRepo[r.Repo], r)
		}
	}
	var repos []reviewGroup
	for repo, rs := range byRepo {
		if len(rs) < groupMin {
			continue
		}
		slices.SortFunc(rs, byRepoAndNumber)
		name := repo[strings.LastIndex(repo, "/")+1:]
		repos = append(repos, reviewGroup{title: fmt.Sprintf("%s · %d", name, len(rs)), reqs: rs})
		take(rs)
	}

	byTitleOf := func(a, b reviewGroup) int { return cmp.Compare(a.title, b.title) }
	slices.SortFunc(repos, byTitleOf)
	slices.SortFunc(fanOuts, byTitleOf)
	for _, r := range reqs {
		if !taken[r.URL] {
			loose = append(loose, r)
		}
	}
	return loose, append(repos, fanOuts...)
}

type reviewsResult struct {
	Requests []ReviewRequest
	Complete bool
}

func (r reviewsResult) stale(n Notification) bool {
	if n.Reason != "review_requested" || !r.Complete {
		return false
	}
	url := subjectWebURL(n)
	for _, req := range r.Requests {
		if req.URL == url {
			return false
		}
	}
	return true
}

const reviewsQuery = `
{
  viewer { login }
  search(query: "is:open is:pr archived:false review-requested:@me -author:@me sort:updated-desc", type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        url
        number
        title
        updatedAt
        author { login }
        repository { nameWithOwner }
        reviewRequests(first: 20) {
          nodes {
            requestedReviewer {
              __typename
              ... on User { login }
              ... on Team { slug }
            }
          }
        }
      }
    }
  }
}`

type reviewsResponse struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Search struct {
			Nodes []struct {
				URL       string `json:"url"`
				Number    int    `json:"number"`
				Title     string `json:"title"`
				UpdatedAt string `json:"updatedAt"`
				Author    *struct {
					Login string `json:"login"`
				} `json:"author"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
				ReviewRequests struct {
					Nodes []struct {
						RequestedReviewer *struct {
							Typename string `json:"__typename"`
							Login    string `json:"login"`
							Slug     string `json:"slug"`
						} `json:"requestedReviewer"`
					} `json:"nodes"`
				} `json:"reviewRequests"`
			} `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func fetchReviews(sc scope) reviewsResult {
	var resp reviewsResponse
	if err := ghJSON(&resp, "graphql", "-f", "query="+reviewsQuery); err != nil {
		log.Printf("Error fetching review requests: %s", err)
		return reviewsResult{}
	}
	if len(resp.Errors) > 0 {
		log.Printf("Review request query returned errors: %s", resp.Errors[0].Message)
		return reviewsResult{}
	}
	return reviewsResult{Requests: parseReviews(resp, sc), Complete: true}
}

func parseReviews(resp reviewsResponse, sc scope) []ReviewRequest {
	me := resp.Data.Viewer.Login
	var out []ReviewRequest
	for _, n := range resp.Data.Search.Nodes {
		if n.URL == "" || !sc.keeps(n.Repository.NameWithOwner) {
			continue
		}
		r := ReviewRequest{
			Title:     n.Title,
			URL:       n.URL,
			Number:    n.Number,
			Repo:      n.Repository.NameWithOwner,
			UpdatedAt: n.UpdatedAt,
		}
		if n.Author != nil {
			r.Author = n.Author.Login
		}
		direct := false
		for _, rr := range n.ReviewRequests.Nodes {
			switch v := rr.RequestedReviewer; {
			case v == nil:
			case v.Typename == "User" && strings.EqualFold(v.Login, me):
				direct = true
			case v.Typename == "Team":
				r.Teams = append(r.Teams, v.Slug)
			}
		}
		if direct {
			r.Teams = nil
		}
		out = append(out, r)
	}
	return out
}
