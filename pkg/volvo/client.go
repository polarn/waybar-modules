package volvo

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrForbidden is a 403 from the API — in practice the app or the
// user's consent grant lacks a scope for the endpoint (e.g.
// location:read), not a broken login, so it is distinct from
// ErrReauthNeeded.
var ErrForbidden = errors.New("volvo: access denied (missing API scope?)")

// Client is an authenticated Volvo cloud API client. BaseURL and
// TokenURL default to the production endpoints; tests override them
// with httptest servers.
type Client struct {
	Config   *Config
	Store    *TokenStore
	BaseURL  string
	TokenURL string
	HTTP     *http.Client
}

// NewClient wires a Client from config and token store.
func NewClient(cfg *Config, store *TokenStore) *Client {
	return &Client{
		Config:   cfg,
		Store:    store,
		BaseURL:  apiBase,
		TokenURL: tokenURL,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// FieldValue tolerates the API sending a value as either a JSON number
// or a string (observed shapes vary by field and API revision).
type FieldValue string

func (v *FieldValue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*v = FieldValue(s)
		return nil
	}
	*v = FieldValue(strings.TrimSpace(string(b)))
	return nil
}

// Field is the per-datum envelope of Energy API v2: supported fields
// carry {status:"OK", value, unit, updatedAt}; unsupported ones come as
// {status:"ERROR", code:"NOT_SUPPORTED"} with nothing else.
type Field struct {
	Status    string     `json:"status"`
	Value     FieldValue `json:"value"`
	Unit      string     `json:"unit"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Code      string     `json:"code"`
}

// OK reports whether the field carries a usable value.
func (f Field) OK() bool { return f.Status == "OK" }

// Int parses the value as an integer (percent, minutes, watts, ...).
func (f Field) Int() (int, error) {
	// Some numeric values may arrive as floats; parse generously.
	s := strings.TrimSpace(string(f.Value))
	if i, err := strconv.Atoi(s); err == nil {
		return i, nil
	}
	fl, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int(fl), nil
}

// Age is how stale the datum is — the car pushes state to Volvo's cloud
// opportunistically, so this can be hours when it is parked and asleep.
func (f Field) Age(now time.Time) time.Duration { return now.Sub(f.UpdatedAt) }

// EnergyState is the Energy API v2 /state projection — only the fields
// the pill and CLI present; the API returns more.
type EnergyState struct {
	BatteryChargeLevel       Field `json:"batteryChargeLevel"`
	ElectricRange            Field `json:"electricRange"`
	ChargingStatus           Field `json:"chargingStatus"`
	ChargerConnectionStatus  Field `json:"chargerConnectionStatus"`
	ChargingPower            Field `json:"chargingPower"`
	TargetBatteryChargeLevel Field `json:"targetBatteryChargeLevel"`
	ChargingTimeToTarget     Field `json:"estimatedChargingTimeToTargetBatteryChargeLevel"`
}

// EnergyState fetches the vehicle's last-reported energy state.
func (c *Client) EnergyState(vin string) (*EnergyState, error) {
	var st EnergyState
	if err := c.get("/energy/v2/vehicles/"+url.PathEscape(vin)+"/state", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Location is the Location API v1 GPS fix.
type Location struct {
	Lat       float64
	Lon       float64
	Heading   string // degrees as sent by the API; may be empty
	Timestamp time.Time
}

// Age is how stale the fix is — like energy data, it only moves when
// the car reports opportunistically.
func (l Location) Age(now time.Time) time.Duration { return now.Sub(l.Timestamp) }

// Location fetches the vehicle's last-reported GPS position. Requires
// the location:read scope on both the portal app and the current
// consent grant; without it the API answers 403 (ErrForbidden).
func (c *Client) Location(vin string) (*Location, error) {
	// The response is a GeoJSON Feature wrapped in {"data": ...};
	// coordinates arrive [lon, lat, alt].
	var out struct {
		Data struct {
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				Heading   string    `json:"heading"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"properties"`
		} `json:"data"`
	}
	if err := c.get("/location/v1/vehicles/"+url.PathEscape(vin)+"/location", &out); err != nil {
		return nil, err
	}
	coords := out.Data.Geometry.Coordinates
	if len(coords) < 2 {
		return nil, fmt.Errorf("location: malformed coordinates (%d values)", len(coords))
	}
	return &Location{
		Lon:       coords[0],
		Lat:       coords[1],
		Heading:   out.Data.Properties.Heading,
		Timestamp: out.Data.Properties.Timestamp,
	}, nil
}

// CVField is the Connected Vehicle API v2 per-datum envelope. Unlike
// Energy v2's Field there is no status member — unsupported data is
// either absent or carries an UNSPECIFIED value.
type CVField struct {
	Value     FieldValue `json:"value"`
	Unit      string     `json:"unit"`
	Timestamp time.Time  `json:"timestamp"`
}

// CVMap is one Connected Vehicle v2 resource: field name → datum.
type CVMap map[string]CVField

// Connected fetches one Connected Vehicle v2 resource leaf. Known
// leaves and their scopes: "doors" (conve:doors_status +
// conve:lock_status), "windows" (conve:windows_status),
// "engine-status" (conve:engine_status), "odometer"
// (conve:odometer_status), "tyres" (conve:tyre_status), "warnings"
// (conve:warnings), "diagnostics" (conve:diagnostics_workshop),
// "statistics" (conve:trip_statistics).
func (c *Client) Connected(vin, leaf string) (CVMap, error) {
	var out struct {
		Data CVMap `json:"data"`
	}
	if err := c.get("/connected-vehicle/v2/vehicles/"+url.PathEscape(vin)+"/"+leaf, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// CommandResult is the response to a Connected Vehicle v2 command
// invocation. InvokeStatus values like COMPLETED / RUNNING / DELIVERED
// mean the car took (or is taking) the command; REJECTED, TIMEOUT,
// CONNECTION_FAILURE etc. mean it did not.
type CommandResult struct {
	VIN          string `json:"vin"`
	InvokeStatus string `json:"invokeStatus"`
	Message      string `json:"message"`
}

// Command invokes a Connected Vehicle v2 command (for this tool only
// "climatization-start" / "climatization-stop" — the sole action scope
// we request). Requires the matching conve:* scope on both the portal
// app and the consent grant.
func (c *Client) Command(vin, command string) (*CommandResult, error) {
	var out struct {
		Data CommandResult `json:"data"`
	}
	if err := c.post("/connected-vehicle/v2/vehicles/"+url.PathEscape(vin)+"/commands/"+command, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Vehicles lists the VINs registered to the authenticated Volvo ID.
func (c *Client) Vehicles() ([]string, error) {
	var out struct {
		Data []struct {
			VIN string `json:"vin"`
		} `json:"data"`
	}
	if err := c.get("/connected-vehicle/v2/vehicles", &out); err != nil {
		return nil, err
	}
	vins := make([]string, 0, len(out.Data))
	for _, v := range out.Data {
		vins = append(vins, v.VIN)
	}
	return vins, nil
}

// get issues an authenticated GET; post an authenticated body-less
// POST (the command endpoints take no payload for our uses).
func (c *Client) get(path string, out any) error  { return c.call(http.MethodGet, path, out) }
func (c *Client) post(path string, out any) error { return c.call(http.MethodPost, path, out) }

// call issues an authenticated request. On a 401 (token revoked
// server-side despite a fresh-looking expiry) it forces one refresh
// and retries.
func (c *Client) call(method, path string, out any) error {
	tok, err := c.accessToken(false)
	if err != nil {
		return err
	}
	status, body, err := c.doReq(method, path, tok)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		if tok, err = c.accessToken(true); err != nil {
			return err
		}
		if status, body, err = c.doReq(method, path, tok); err != nil {
			return err
		}
	}
	if status == http.StatusForbidden {
		return fmt.Errorf("%s %s: %w", method, path, ErrForbidden)
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, status, truncate(body, 200))
	}
	return json.Unmarshal(body, out)
}

func (c *Client) doReq(method, path, token string) (int, []byte, error) {
	req, err := http.NewRequest(method, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("vcc-api-key", c.Config.VccAPIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// accessToken returns a valid bearer token, refreshing (and rotating
// the persisted refresh token) when needed. The whole cycle runs under
// the store lock so concurrent invocations can't double-spend a
// single-use refresh token.
func (c *Client) accessToken(force bool) (string, error) {
	var tok string
	err := c.Store.WithLock(func() error {
		t, err := c.Store.Load()
		if err != nil {
			return err
		}
		if !force && t.Valid(time.Now()) {
			tok = t.AccessToken
			return nil
		}
		nt, err := c.refreshGrant(t.RefreshToken)
		if err != nil {
			return err
		}
		if err := c.Store.Save(nt); err != nil {
			return err
		}
		tok = nt.AccessToken
		return nil
	})
	return tok, err
}

// refreshGrant redeems the refresh token. Volvo answers 400 (typically
// error=invalid_grant) once the personal-app consent grant lapses or a
// rotated token is replayed — both mean the user must re-auth, so any
// 4xx maps to ErrReauthNeeded.
func (c *Client) refreshGrant(refreshToken string) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequest(http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+basicCredentials(c.Config))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return nil, fmt.Errorf("%w (HTTP %d: %s)", ErrReauthNeeded, resp.StatusCode, truncate(body, 200))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh: HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return parseTokenResponse(body)
}

// parseTokenResponse converts a token-endpoint response into Tokens
// with an absolute expiry.
func parseTokenResponse(body []byte) (*Tokens, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("token response missing tokens: %s", truncate(body, 200))
	}
	return &Tokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}

func basicCredentials(cfg *Config) string {
	return base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
