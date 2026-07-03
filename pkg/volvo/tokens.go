package volvo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrNoTokens means no tokens.json exists yet — run `volvo-ctl auth`.
var ErrNoTokens = errors.New("volvo: no tokens — run 'volvo-ctl auth'")

// ErrReauthNeeded means the refresh token was rejected. Personal
// developer-portal apps have a limited consent grant (~a week), after
// which refresh stops working and the user must run `volvo-ctl auth`
// again in a browser.
var ErrReauthNeeded = errors.New("volvo: refresh token rejected — run 'volvo-ctl auth' again")

// Tokens is the persisted OAuth state. Expiry is absolute, computed from
// the token response's expires_in at save time.
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// Valid reports whether the access token is still usable, with a 60s
// skew margin so we never send a token that expires mid-request.
func (t *Tokens) Valid(now time.Time) bool {
	return t.AccessToken != "" && now.Add(60*time.Second).Before(t.Expiry)
}

// TokenStore persists tokens at Path and serialises every
// read→refresh→write cycle across processes. Volvo rotates refresh
// tokens (each is single-use), so two concurrent refreshes — e.g. the
// waybar poll racing a manual CLI run — would invalidate the grant;
// WithLock makes that impossible.
type TokenStore struct {
	Path string
}

// WithLock runs fn while holding an exclusive flock on Path+".lock".
// The lock lives in a separate file on purpose: Save replaces the data
// file via rename(2), so a lock taken on tokens.json itself would be
// attached to a dead inode after the first rotation.
func (s *TokenStore) WithLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// Load reads the persisted tokens. A missing file maps to ErrNoTokens.
func (s *TokenStore) Load() (*Tokens, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoTokens
		}
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if t.RefreshToken == "" {
		return nil, ErrNoTokens
	}
	return &t, nil
}

// Save atomically replaces the token file (temp file + rename) with
// 0600 perms, so a crash mid-write can never lose the current tokens.
func (s *TokenStore) Save(t *Tokens) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".tokens-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after successful rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}
