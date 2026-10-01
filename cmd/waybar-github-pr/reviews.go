package main

import (
	"fmt"
	"log"
	"strings"
)

const glyphReview = "\uf4af"

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

func (r ReviewRequest) Suffix() string {
	s := ""
	if r.Author != "" {
		s += " · " + r.Author
	}
	if len(r.Teams) > 0 {
		s += " · for " + strings.Join(r.Teams, ", ")
	}
	return s
}

type reviewsResult struct {
	Requests []ReviewRequest
	Complete bool
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
