// Package volvo is a minimal client for Volvo's cloud Connected Vehicle
// and Energy APIs (api.volvocars.com). Auth is OAuth 2.0 authorization
// code with PKCE against the Volvo ID identity provider; every data call
// additionally requires a vcc-api-key header issued by the developer
// portal (developer.volvocars.com).
package volvo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultRedirectPort is the localhost port the auth flow listens on.
	// The redirect URI registered in the developer portal must match
	// exactly: http://localhost:20999/callback
	DefaultRedirectPort = 20999

	apiBase      = "https://api.volvocars.com"
	authorizeURL = "https://volvoid.eu.volvocars.com/as/authorization.oauth2"
	tokenURL     = "https://volvoid.eu.volvocars.com/as/token.oauth2"

	// scopes covers VIN discovery plus the Energy API v2 reads. Personal
	// apps must be published with (at least) these scopes selected.
	scopes = "openid conve:vehicle_relation energy:state:read energy:capability:read"
)

// ErrNoConfig means the config file is missing or still holds
// placeholders — the pill renders this as a "setup" state.
var ErrNoConfig = errors.New("volvo: config not found")

// Config holds the developer-portal application credentials. Written by
// hand to <dir>/config.json (0600); never tracked in dotfiles.
type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	VccAPIKey    string `json:"vcc_api_key"`
	VIN          string `json:"vin,omitempty"`           // pin to skip the vehicle-list call
	RedirectPort int    `json:"redirect_port,omitempty"` // 0 → DefaultRedirectPort
}

// DefaultDir returns ~/.config/volvo.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "volvo"), nil
}

// LoadConfig reads <dir>/config.json. Missing file, placeholder values,
// or empty required fields all wrap ErrNoConfig so callers can treat
// every "user hasn't finished setup" case alike.
func LoadConfig(dir string) (*Config, error) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, yerr := os.Stat(filepath.Join(dir, "config.yaml")); yerr == nil {
				return nil, fmt.Errorf("%w: found legacy %s/config.yaml — replace it with config.json (client_id, client_secret, vcc_api_key) and delete the yaml", ErrNoConfig, dir)
			}
			return nil, fmt.Errorf("%w: %s", ErrNoConfig, path)
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, v := range map[string]string{
		"client_id":     cfg.ClientID,
		"client_secret": cfg.ClientSecret,
		"vcc_api_key":   cfg.VccAPIKey,
	} {
		if v == "" || strings.HasPrefix(v, "YOUR_") {
			return nil, fmt.Errorf("%w: %s missing %s", ErrNoConfig, path, name)
		}
	}
	return &cfg, nil
}
