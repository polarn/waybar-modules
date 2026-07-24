package dirigera

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MusicItem is one playable entry the hub mirrors from a linked speaker
// account (Sonos). The ID is an opaque blob that encodes the item's
// *position* in the speaker's favorites/playlists list — it is NOT stable
// across reorders or deletions, so resolve by Title at call time and never
// persist IDs.
type MusicItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
}

// Music is the hub's view of what linked speakers can play.
type Music struct {
	Playlists []MusicItem `json:"playlists"`
	Favorites []MusicItem `json:"favorites"`
}

// Music returns the speaker content (favorites, playlists) known to the hub.
func (c *Client) Music() (*Music, error) {
	var out Music
	if err := c.getJSON("/v1/music", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Scenes returns all scenes as raw JSON objects. Scenes are updated with a
// full-object PUT, so fields we don't model (commands, undoAllowedDuration,
// future additions) must survive the round-trip — hence maps, not structs.
func (c *Client) Scenes() ([]map[string]any, error) {
	var out []map[string]any
	if err := c.getJSON("/v1/scenes", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Scene returns a single scene as a raw JSON object.
func (c *Client) Scene(id string) (map[string]any, error) {
	var out map[string]any
	if err := c.getJSON("/v1/scenes/"+id, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutScene replaces a scene wholesale. The hub answers 202 and applies the
// change asynchronously (observed within ~2s) — read the scene back to
// confirm when it matters.
func (c *Client) PutScene(id string, scene map[string]any) error {
	buf, err := json.Marshal(scene)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, c.baseURL()+"/v1/scenes/"+id, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("put scene %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put scene %s: %s: %s", id, resp.Status, string(body))
	}
	return nil
}

func (c *Client) getJSON(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: %s: %s", path, resp.Status, string(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
