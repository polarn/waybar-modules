package volvo

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Authenticate runs the browser-interactive authorization-code + PKCE
// flow and returns fresh tokens. Volvo ID logins include an email 2FA
// step, so this can never be headless: we open the system browser and
// catch the redirect on a localhost callback server. The redirect URI
// must be registered verbatim on the developer-portal application; the
// portal bans localhost URIs, so cfg.RedirectURI normally points at a
// public forwarding page (docs/volvo-callback) that bounces the browser
// on to the localhost listener.
func Authenticate(cfg *Config, openBrowser bool) (*Tokens, error) {
	ar, err := NewAuthRequest(cfg)
	if err != nil {
		return nil, err
	}

	// Bind before opening the browser so a taken port fails fast.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.CallbackPort()))
	if err != nil {
		return nil, fmt.Errorf("listen on callback port: %w", err)
	}

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code, err := ar.CallbackCode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			resCh <- result{err: err}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><p>Authenticated — you can close this tab.</p></body></html>")
		resCh <- result{code: code}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	if openBrowser {
		_ = exec.Command("xdg-open", ar.URL).Start()
	}
	fmt.Fprintf(os.Stderr, "Complete the Volvo ID login in your browser. If none opened, visit:\n\n%s\n\n", ar.URL)

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		return ar.Exchange(cfg, res.code)
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for browser login")
	}
}

// AuthRequest is one pending authorization-code + PKCE login: send the
// browser to URL, then pass the callback's code to Exchange.
type AuthRequest struct {
	URL      string
	State    string
	verifier string
	redirect string
}

// NewAuthRequest starts a login against the redirect URI registered on
// the portal app (cfg.RedirectURI, else the legacy localhost callback).
func NewAuthRequest(cfg *Config) (*AuthRequest, error) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generate verifier: %w", err)
	}
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	redirect := fmt.Sprintf("http://localhost:%d/callback", cfg.CallbackPort())
	if cfg.RedirectURI != "" {
		redirect = cfg.RedirectURI
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	return &AuthRequest{
		URL:      authorizeURL + "?" + q.Encode(),
		State:    state,
		verifier: verifier,
		redirect: redirect,
	}, nil
}

// CallbackCode validates a /callback request against this login and
// returns its authorization code.
func (a *AuthRequest) CallbackCode(r *http.Request) (string, error) {
	q := r.URL.Query()
	switch {
	case q.Get("state") != a.State:
		return "", errors.New("callback state mismatch")
	case q.Get("error") != "":
		return "", fmt.Errorf("authorization denied: %s (%s)", q.Get("error"), q.Get("error_description"))
	case q.Get("code") == "":
		return "", errors.New("callback missing code")
	}
	return q.Get("code"), nil
}

// Exchange swaps the callback's authorization code for tokens.
func (a *AuthRequest) Exchange(cfg *Config, code string) (*Tokens, error) {
	return exchangeCode(cfg, code, a.verifier, a.redirect)
}

// exchangeCode swaps the authorization code for tokens. Like refresh,
// the token endpoint authenticates the app with Basic client
// credentials (confidential client) on top of the PKCE verifier.
func exchangeCode(cfg *Config, code, verifier, redirect string) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+basicCredentials(cfg))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
	return parseTokenResponse(body)
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

// randomState produces the CSRF state parameter for the auth request.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
