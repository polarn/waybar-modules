package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polarn/waybar-modules/pkg/volvo"
)

const energyBody = `{
  "batteryChargeLevel": {"status": "OK", "value": "36.0", "unit": "percentage", "updatedAt": "2026-10-26T20:00:00Z"},
  "electricRange": {"status": "OK", "value": "170.0", "unit": "km", "updatedAt": "2026-10-26T20:00:00Z"},
  "chargingStatus": {"status": "OK", "value": "IDLE", "updatedAt": "2026-10-26T20:00:00Z"},
  "chargerConnectionStatus": {"status": "OK", "value": "DISCONNECTED", "updatedAt": "2026-10-26T20:00:00Z"},
  "chargingPower": {"status": "ERROR", "code": "PROPERTY_NOT_FOUND"},
  "targetBatteryChargeLevel": {"status": "OK", "value": "100", "unit": "percentage", "updatedAt": "2026-10-26T20:00:00Z"},
  "estimatedChargingTimeToTargetBatteryChargeLevel": {"status": "OK", "value": "0", "unit": "minutes", "updatedAt": "2026-10-26T20:00:00Z"}
}`

func api(t *testing.T, energy http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/energy/"):
			energy(w, r)
		case strings.HasSuffix(r.URL.Path, "/doors"):
			fmt.Fprint(w, `{"data": {"centralLock": {"value": "LOCKED"}, "tailgate": {"value": "OPEN"}, "hood": {"value": "CLOSED"}}}`)
		case strings.HasSuffix(r.URL.Path, "/odometer"):
			fmt.Fprint(w, `{"data": {"odometer": {"value": 19717, "unit": "km"}}}`)
		case strings.HasSuffix(r.URL.Path, "/warnings"):
			fmt.Fprint(w, `{"data": {"brakeLightLeftWarning": {"value": "FAILURE"}, "hazardLightsWarning": {"value": "UNSPECIFIED"}, "fogLightRearWarning": {"value": "NO_WARNING"}}}`)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testPoller(t *testing.T, srv *httptest.Server, refresh http.HandlerFunc) *poller {
	t.Helper()
	store := &volvo.TokenStore{Path: filepath.Join(t.TempDir(), "tokens.json")}
	if err := store.Save(&volvo.Tokens{AccessToken: "AT", RefreshToken: "R1", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := volvo.NewClient(&volvo.Config{ClientID: "id", ClientSecret: "s", VccAPIKey: "k"}, store)
	c.BaseURL = srv.URL
	tok := httptest.NewServer(refresh)
	t.Cleanup(tok.Close)
	c.TokenURL = tok.URL
	return newPoller(c, "VIN1", 5*time.Minute)
}

func noRefresh(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected token refresh") }
}

func TestPollAndCollect(t *testing.T) {
	srv := api(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, energyBody) })
	p := testPoller(t, srv, noRefresh(t))
	if wait := p.poll(); wait != 5*time.Minute {
		t.Errorf("wait = %s, want the interval", wait)
	}
	out := collect(p.view())
	for _, want := range []string{
		`volvo_up 1`,
		`volvo_auth_ok 1`,
		`volvo_polls_total{result="ok"} 1`,
		`volvo_battery_charge_level_percent{vin="VIN1"} 36`,
		`volvo_electric_range_meters{vin="VIN1"} 170000`,
		`volvo_charging{vin="VIN1"} 0`,
		`volvo_charging_status_info{vin="VIN1",status="IDLE"} 1`,
		`volvo_charger_connected{vin="VIN1"} 0`,
		`volvo_energy_updated_timestamp_seconds{vin="VIN1"} 1.7930448e+09`,
		`volvo_odometer_meters{vin="VIN1"} 1.9717e+07`,
		`volvo_locked{vin="VIN1"} 1`,
		`volvo_open{vin="VIN1",part="tailgate"} 1`,
		`volvo_open{vin="VIN1",part="hood"} 0`,
		`volvo_warning{vin="VIN1",source="warnings",name="brakeLightLeftWarning"} 1`,
		`volvo_warning{vin="VIN1",source="warnings",name="fogLightRearWarning"} 0`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, absent := range []string{"volvo_charging_power_watts", "hazardLightsWarning", "volvo_engine_running"} {
		if strings.Contains(out, absent) {
			t.Errorf("%s should be absent", absent)
		}
	}
	if strings.Count(out, "# TYPE volvo_open gauge") != 1 {
		t.Error("volvo_open family split")
	}
}

func TestPollReauth(t *testing.T) {
	srv := api(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	p := testPoller(t, srv, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	})
	if wait := p.poll(); wait != 5*time.Minute {
		t.Errorf("wait = %s, want the interval: a lapsed grant must keep checking for new tokens", wait)
	}
	out := collect(p.view())
	for _, want := range []string{`volvo_auth_ok 0`, `volvo_up 0`, `volvo_polls_total{result="reauth"} 1`} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q", want)
		}
	}
}

func TestPollRateLimitBacksOff(t *testing.T) {
	srv := api(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	p := testPoller(t, srv, noRefresh(t))
	if wait := p.poll(); wait != 2*time.Hour {
		t.Errorf("wait = %s, want Retry-After's 2h", wait)
	}
	if v := p.view(); v.polls["rate_limited"] != 1 || v.retryUntil.IsZero() {
		t.Errorf("view = %+v", v)
	}
}

func TestPollErrorBacksOffAndRecovers(t *testing.T) {
	fail := true
	srv := api(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, energyBody)
	})
	p := testPoller(t, srv, noRefresh(t))
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
		if wait := p.poll(); wait != want {
			t.Errorf("failure %d: wait = %s, want %s", i+1, wait, want)
		}
	}
	fail = false
	if wait := p.poll(); wait != 5*time.Minute {
		t.Errorf("after recovery wait = %s, want the interval", wait)
	}
	if v := p.view(); v.lastErr != nil || v.snap == nil || !v.retryUntil.IsZero() {
		t.Errorf("view after recovery = %+v", v)
	}
}

func TestAuthMux(t *testing.T) {
	cfg := &volvo.Config{ClientID: "id", ClientSecret: "s", VccAPIKey: "k", RedirectURI: "https://example.invalid/cb/"}
	store := &volvo.TokenStore{Path: filepath.Join(t.TempDir(), "tokens.json")}
	mux := newAuthMux(cfg, store, make(chan struct{}, 1))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?code=C&state=S", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("callback without /auth: %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth", nil))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.Contains(loc, "volvoid.eu.volvocars.com") || !strings.Contains(loc, "code_challenge=") {
		t.Fatalf("/auth: %d %s", rec.Code, loc)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?code=C&state=forged", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("forged state: %d, want 400", rec.Code)
	}
	if _, err := store.Load(); err != volvo.ErrNoTokens {
		t.Errorf("tokens written on a rejected callback: %v", err)
	}
}
