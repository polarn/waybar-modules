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
// (http://localhost:<port>/callback) must be registered verbatim on the
// developer-portal application.
func Authenticate(cfg *Config, openBrowser bool) (*Tokens, error) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generate verifier: %w", err)
	}
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	port := cfg.RedirectPort
	if port == 0 {
		port = DefaultRedirectPort
	}
	redirect := fmt.Sprintf("http://localhost:%d/callback", port)

	// Bind before opening the browser so a taken port fails fast.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
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
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- result{err: errors.New("callback state mismatch")}
		case q.Get("error") != "":
			http.Error(w, q.Get("error"), http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("authorization denied: %s (%s)", q.Get("error"), q.Get("error_description"))}
		case q.Get("code") == "":
			http.Error(w, "missing code", http.StatusBadRequest)
			resCh <- result{err: errors.New("callback missing code")}
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><p>Authenticated — you can close this tab.</p></body></html>")
			resCh <- result{code: q.Get("code")}
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	authURL := authorizeURL + "?" + q.Encode()

	if openBrowser {
		_ = exec.Command("xdg-open", authURL).Start()
	}
	fmt.Fprintf(os.Stderr, "Complete the Volvo ID login in your browser. If none opened, visit:\n\n%s\n\n", authURL)

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		return exchangeCode(cfg, res.code, verifier, redirect)
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for browser login")
	}
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
