// Package bambu talks to Bambu Lab's cloud (HTTP login + MQTT status) so
// a printer can stay in Cloud mode while local tools read its state. The
// P2S generation only serves local MQTT in LAN-only + Developer Mode, so
// the cloud broker is the practical channel for a cloud-bound printer.
// Endpoints per OpenBambuAPI docs and ha-bambulab's pybambu client.
package bambu

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Session is the cached login state. The file layout is shared with the
// Python prototype this package replaced, so either tool can mint or
// consume the token.
type Session struct {
	AccessToken string `json:"access_token"`
	Serial      string `json:"serial"`
	Name        string `json:"name"`
	Saved       int64  `json:"saved"`
	// MQTTUser caches the derived broker username ("u_<uid>"): tokens from
	// some login flows (e.g. emailed-code) are opaque, not JWTs, and then
	// the uid must be fetched from the API instead — see UsernameFromAPI.
	MQTTUser string `json:"mqtt_user,omitempty"`
}

var (
	// ErrNoSession means login was never run (or the cache was removed).
	ErrNoSession = errors.New("no cached token — run: bambu-ctl login")
	// ErrAuth means the cloud rejected the token (expired ~3 months in,
	// or revoked by a password change).
	ErrAuth = errors.New("cloud rejected credentials — run: bambu-ctl login")
	// ErrNoReport means the broker accepted us but the printer never
	// answered — powered off, or its pushall rate-limit swallowed the ask.
	ErrNoReport = errors.New("no report from printer (off, or pushall rate-limited — retry in a minute)")
)

// DefaultPath is the session cache location: ~/.config/bambu-cloud.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "bambu-cloud.json"), nil
}

func LoadSession(path string) (*Session, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.AccessToken == "" {
		return nil, ErrNoSession
	}
	return &s, nil
}

// Save writes the session with 0600 (it is a bearer credential).
func (s *Session) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// MQTTUsername derives the broker username from the access token: the
// JWT payload carries a "username" claim of the form "u_<uid>".
func (s *Session) MQTTUsername() (string, error) {
	parts := strings.Split(s.AccessToken, ".")
	if len(parts) < 2 {
		return "", errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse JWT payload: %w", err)
	}
	if claims.Username == "" {
		return "", errors.New("JWT payload has no username claim")
	}
	return claims.Username, nil
}
