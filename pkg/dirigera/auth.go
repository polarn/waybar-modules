// Package dirigera provides a minimal client for the IKEA DIRIGERA smart
// home hub. The hub speaks HTTPS on port 8443 with a self-signed
// certificate, and auth uses OAuth 2.0 with PKCE plus a physical
// confirmation (pressing the Action button on the hub).
package dirigera

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultPort is the HTTPS port DIRIGERA listens on.
const DefaultPort = 8443

// AuthClient drives the initial pairing flow that produces a long-lived
// access token.
type AuthClient struct {
	Host   string // hostname or IP of the hub
	Port   int    // optional; defaults to DefaultPort
	HTTP   *http.Client
	client string // lazily-initialised user-agent / client name
}

// NewAuthClient returns an AuthClient preconfigured with a TLS-skipping
// HTTP client (DIRIGERA ships a self-signed cert).
func NewAuthClient(host string) *AuthClient {
	return &AuthClient{
		Host: host,
		Port: DefaultPort,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// baseURL assembles the scheme+host+port prefix for API calls.
func (a *AuthClient) baseURL() string {
	p := a.Port
	if p == 0 {
		p = DefaultPort
	}
	return fmt.Sprintf("https://%s:%d", a.Host, p)
}

// RequestCode generates a PKCE code_verifier/code_challenge pair and
// asks the hub for an authorization code. The verifier must be returned
// to ExchangeCode to complete the flow.
func (a *AuthClient) RequestCode() (code, verifier string, err error) {
	verifier, err = generateCodeVerifier()
	if err != nil {
		return "", "", fmt.Errorf("generate verifier: %w", err)
	}
	challenge := codeChallenge(verifier)

	q := url.Values{}
	q.Set("audience", "homesmart.local")
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	u := a.baseURL() + "/v1/oauth/authorize?" + q.Encode()
	resp, err := a.HTTP.Get(u)
	if err != nil {
		return "", "", fmt.Errorf("authorize request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("authorize: %s: %s", resp.Status, string(body))
	}

	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("parse authorize response: %w", err)
	}
	if out.Code == "" {
		return "", "", fmt.Errorf("authorize returned empty code: %s", string(body))
	}
	return out.Code, verifier, nil
}

// ExchangeCode swaps the authorization code for a long-lived access
// token. Must be called AFTER the user has pressed the Action button on
// the hub (within ~60 seconds).
func (a *AuthClient) ExchangeCode(code, verifier, clientName string) (string, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("name", clientName)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost,
		a.baseURL()+"/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange: %s: %s", resp.Status, string(body))
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token: %s", string(body))
	}
	return out.AccessToken, nil
}

// generateCodeVerifier produces an RFC 7636 PKCE code verifier —
// base64url-encoded random bytes, 43–128 chars after encoding.
func generateCodeVerifier() (string, error) {
	b := make([]byte, 64) // 64 bytes → ~86 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallenge is the S256 transformation of a PKCE verifier.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
