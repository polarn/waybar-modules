package volvo

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// xc40Fixture is the Energy API v2 /state response for a 2024 XC40
// electric, taken verbatim from Home Assistant core's test fixtures
// (tests/components/volvo/fixtures/xc40_electric_2024/energy_state.json),
// with two modifications for tolerance coverage: chargingCurrentLimit is
// replaced by the NOT_SUPPORTED error shape, and targetBatteryChargeLevel's
// value is a string instead of a number.
const xc40Fixture = `{
  "batteryChargeLevel": {
    "status": "OK",
    "value": 53,
    "unit": "percentage",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "electricRange": {
    "status": "OK",
    "value": 150,
    "unit": "mi",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "chargerConnectionStatus": {
    "status": "OK",
    "value": "CONNECTED",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "chargingStatus": {
    "status": "OK",
    "value": "CHARGING",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "chargingType": {
    "status": "OK",
    "value": "AC",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "chargerPowerStatus": {
    "status": "OK",
    "value": "PROVIDING_POWER",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "estimatedChargingTimeToTargetBatteryChargeLevel": {
    "status": "OK",
    "value": 1440,
    "unit": "minutes",
    "updatedAt": "2025-07-02T08:51:23Z"
  },
  "chargingCurrentLimit": {
    "status": "ERROR",
    "code": "NOT_SUPPORTED"
  },
  "targetBatteryChargeLevel": {
    "status": "OK",
    "value": "90",
    "unit": "percentage",
    "updatedAt": "2024-09-22T09:40:12Z"
  },
  "chargingPower": {
    "status": "OK",
    "value": 1386,
    "unit": "watts",
    "updatedAt": "2025-07-02T08:51:23Z"
  }
}`

// testClient returns a Client with fresh valid tokens on disk, pointed
// at the given API and token servers.
func testClient(t *testing.T, apiURL, tokURL string) *Client {
	t.Helper()
	store := &TokenStore{Path: filepath.Join(t.TempDir(), "tokens.json")}
	err := store.Save(&Tokens{
		AccessToken:  "AT-valid",
		RefreshToken: "R1",
		Expiry:       time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(&Config{ClientID: "id", ClientSecret: "secret", VccAPIKey: "key"}, store)
	c.BaseURL = apiURL
	c.TokenURL = tokURL
	return c
}

func TestEnergyStateParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/energy/v2/vehicles/VIN123/state" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("vcc-api-key") != "key" {
			t.Errorf("missing vcc-api-key header")
		}
		if r.Header.Get("Authorization") != "Bearer AT-valid" {
			t.Errorf("bad Authorization header %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, xc40Fixture)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL, srv.URL+"/token")
	st, err := c.EnergyState("VIN123")
	if err != nil {
		t.Fatal(err)
	}

	if !st.BatteryChargeLevel.OK() {
		t.Error("batteryChargeLevel should be OK")
	}
	if pct, err := st.BatteryChargeLevel.Int(); err != nil || pct != 53 {
		t.Errorf("charge level = %d, %v; want 53", pct, err)
	}
	if st.ElectricRange.Unit != "mi" {
		t.Errorf("range unit = %q, want mi", st.ElectricRange.Unit)
	}
	// String-valued number must parse too.
	if target, err := st.TargetBatteryChargeLevel.Int(); err != nil || target != 90 {
		t.Errorf("target = %d, %v; want 90", target, err)
	}
	if string(st.ChargingStatus.Value) != "CHARGING" {
		t.Errorf("chargingStatus = %q", st.ChargingStatus.Value)
	}
	if w, err := st.ChargingPower.Int(); err != nil || w != 1386 {
		t.Errorf("chargingPower = %d, %v; want 1386", w, err)
	}
	want := time.Date(2025, 7, 2, 8, 51, 23, 0, time.UTC)
	if !st.BatteryChargeLevel.UpdatedAt.Equal(want) {
		t.Errorf("updatedAt = %v, want %v", st.BatteryChargeLevel.UpdatedAt, want)
	}
}

func TestNotSupportedField(t *testing.T) {
	// A projected field arriving in the unsupported-error shape must
	// decode without error and report !OK().
	body := `{
	  "batteryChargeLevel": {"status": "OK", "value": 53, "unit": "percentage", "updatedAt": "2025-07-02T08:51:23Z"},
	  "chargerConnectionStatus": {"status": "ERROR", "code": "NOT_SUPPORTED"}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL, srv.URL+"/token")
	st, err := c.EnergyState("VIN123")
	if err != nil {
		t.Fatal(err)
	}
	if st.ChargerConnectionStatus.OK() {
		t.Error("NOT_SUPPORTED field must not be OK")
	}
	if st.ChargerConnectionStatus.Code != "NOT_SUPPORTED" {
		t.Errorf("code = %q", st.ChargerConnectionStatus.Code)
	}
	// Absent fields (zero value) must not be OK either.
	if st.ElectricRange.OK() {
		t.Error("absent field must not be OK")
	}
	if !st.BatteryChargeLevel.OK() {
		t.Error("batteryChargeLevel should be OK")
	}
}

func TestRefreshRotation(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	current := "R1"

	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		rt := r.PostForm.Get("refresh_token")
		mu.Lock()
		seen[rt]++
		ok := rt == current
		if ok {
			current = "R" + fmt.Sprint(len(seen)+1)
		}
		next := current
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		fmt.Fprintf(w, `{"access_token":"AT-%s","refresh_token":"%s","expires_in":1800}`, next, next)
	}))
	defer tok.Close()

	c := testClient(t, "http://unused.invalid", tok.URL)

	if _, err := c.accessToken(true); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if _, err := c.accessToken(true); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for rt, n := range seen {
		if n > 1 {
			t.Errorf("refresh token %s was spent %d times", rt, n)
		}
	}
	if seen["R1"] != 1 || seen["R2"] != 1 {
		t.Errorf("expected R1 and R2 each used once, got %v", seen)
	}

	// Newest rotation must be what's on disk, with 0600 perms.
	saved, err := c.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "R3" {
		t.Errorf("persisted refresh token = %s, want R3", saved.RefreshToken)
	}
	info, err := os.Stat(c.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("tokens.json mode = %o, want 600", perm)
	}
}

func TestInvalidGrantMapsToReauth(t *testing.T) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer tok.Close()

	c := testClient(t, "http://unused.invalid", tok.URL)
	_, err := c.accessToken(true)
	if !errors.Is(err, ErrReauthNeeded) {
		t.Errorf("err = %v, want ErrReauthNeeded", err)
	}
}

func TestConcurrentRefreshNoDoubleSpend(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	counter := 1

	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		rt := r.PostForm.Get("refresh_token")
		mu.Lock()
		seen[rt]++
		if seen[rt] > 1 {
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		counter++
		next := "R" + fmt.Sprint(counter)
		mu.Unlock()
		fmt.Fprintf(w, `{"access_token":"AT-%s","refresh_token":"%s","expires_in":1800}`, next, next)
	}))
	defer tok.Close()

	c := testClient(t, "http://unused.invalid", tok.URL)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.accessToken(true)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for rt, n := range seen {
		if n > 1 {
			t.Errorf("refresh token %s was spent %d times concurrently", rt, n)
		}
	}
}

func TestMissingTokensFile(t *testing.T) {
	store := &TokenStore{Path: filepath.Join(t.TempDir(), "tokens.json")}
	c := NewClient(&Config{ClientID: "id", ClientSecret: "s", VccAPIKey: "k"}, store)
	_, err := c.EnergyState("VIN123")
	if !errors.Is(err, ErrNoTokens) {
		t.Errorf("err = %v, want ErrNoTokens", err)
	}
}

func TestLoadConfigStates(t *testing.T) {
	dir := t.TempDir()

	// Missing entirely.
	if _, err := LoadConfig(dir); !errors.Is(err, ErrNoConfig) {
		t.Errorf("missing: err = %v, want ErrNoConfig", err)
	}

	// Legacy yaml placeholder present.
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("api_key: YOUR_API_KEY\n"), 0o600)
	if _, err := LoadConfig(dir); !errors.Is(err, ErrNoConfig) {
		t.Errorf("yaml-only: err = %v, want ErrNoConfig", err)
	}

	// Placeholder json.
	os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"client_id":"YOUR_CLIENT_ID","client_secret":"s","vcc_api_key":"k"}`), 0o600)
	if _, err := LoadConfig(dir); !errors.Is(err, ErrNoConfig) {
		t.Errorf("placeholder: err = %v, want ErrNoConfig", err)
	}

	// Valid.
	os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"client_id":"id","client_secret":"s","vcc_api_key":"k","vin":"V1"}`), 0o600)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if cfg.VIN != "V1" {
		t.Errorf("vin = %q", cfg.VIN)
	}
}
