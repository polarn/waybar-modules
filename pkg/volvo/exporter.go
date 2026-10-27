package volvo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrExporterReauth means volvo-exporter's own grant lapsed; the login
// has to happen through the exporter's /auth, not `volvo-ctl auth`.
var ErrExporterReauth = errors.New("volvo-exporter: refresh token rejected — log in again through its /auth")

// ExporterState is volvo-exporter's /state response.
type ExporterState struct {
	AuthOK      bool      `json:"auth_ok"`
	LastPoll    time.Time `json:"last_poll"`
	LastError   string    `json:"last_error,omitempty"`
	Snapshot    *Snapshot `json:"snapshot,omitempty"`
	RetryAfter  string    `json:"retry_after,omitempty"`
	IntervalSec int       `json:"interval_seconds"`
}

// FetchExporterState reads baseURL/state from a volvo-exporter.
func FetchExporterState(baseURL string) (*ExporterState, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/state")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var st ExporterState
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("volvo-exporter /state: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return &st, nil
}

// Current returns the exporter's snapshot, or why there is none.
func (s *ExporterState) Current() (*Snapshot, error) {
	if !s.AuthOK {
		return nil, ErrExporterReauth
	}
	if s.Snapshot == nil {
		if s.LastError != "" {
			return nil, fmt.Errorf("volvo-exporter has no data yet: %s", s.LastError)
		}
		return nil, errors.New("volvo-exporter has no data yet")
	}
	return s.Snapshot, nil
}
