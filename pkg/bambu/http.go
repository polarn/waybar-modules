package bambu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const apiBase = "https://api.bambulab.com"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Bambu's API applies risk control to unknown clients; these headers
// mimic OrcaSlicer, same as ha-bambulab does.
var apiHeaders = map[string]string{
	"User-Agent":            "bambu_network_agent/01.09.05.01",
	"X-BBL-Client-Name":     "OrcaSlicer",
	"X-BBL-Client-Type":     "slicer",
	"X-BBL-Client-Version":  "01.09.05.51",
	"X-BBL-Language":        "en-US",
	"X-BBL-OS-Type":         "linux",
	"X-BBL-Agent-Version":   "01.09.05.01",
}

// LoginResult is the /user/login response. AccessToken is set on direct
// success; otherwise LoginType says which second factor is needed
// ("verifyCode" = emailed code, "tfa" = authenticator app + TFAKey).
type LoginResult struct {
	AccessToken string `json:"accessToken"`
	LoginType   string `json:"loginType"`
	TFAKey      string `json:"tfaKey"`
}

func doJSON(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, apiBase+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range apiHeaders {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, snippet)
	}
	return resp, nil
}

func decodeInto(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// LoginPassword starts a login. Check the result: an empty AccessToken
// with LoginType set means a second step is required.
func LoginPassword(email, password string) (*LoginResult, error) {
	resp, err := doJSON("POST", "/v1/user-service/user/login",
		map[string]string{"account": email, "password": password, "apiError": ""})
	if err != nil {
		return nil, err
	}
	var res LoginResult
	if err := decodeInto(resp, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RequestEmailCode asks Bambu to email a one-time login code.
func RequestEmailCode(email string) error {
	resp, err := doJSON("POST", "/v1/user-service/user/sendemail/code",
		map[string]string{"email": email, "type": "codeLogin"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// LoginCode completes a "verifyCode" login with the emailed code.
func LoginCode(email, code string) (*LoginResult, error) {
	resp, err := doJSON("POST", "/v1/user-service/user/login",
		map[string]string{"account": email, "code": code})
	if err != nil {
		return nil, err
	}
	var res LoginResult
	if err := decodeInto(resp, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// LoginTFA completes a "tfa" login; the token arrives in a cookie.
func LoginTFA(tfaKey, code string) (string, error) {
	resp, err := doJSON("POST", "/v1/user-service/user/tfa/login",
		map[string]string{"tfaKey": tfaKey, "tfaCode": code})
	if err != nil {
		return "", err
	}
	var res LoginResult
	var cookieToken string
	for _, c := range resp.Cookies() {
		if c.Name == "token" {
			cookieToken = c.Value
		}
	}
	if err := decodeInto(resp, &res); err == nil && res.AccessToken != "" {
		return res.AccessToken, nil
	}
	if cookieToken != "" {
		return cookieToken, nil
	}
	return "", fmt.Errorf("tfa login returned no token")
}

// Device is one printer bound to the account.
type Device struct {
	DevID          string `json:"dev_id"`
	Name           string `json:"name"`
	Online         bool   `json:"online"`
	DevProductName string `json:"dev_product_name"`
}

// Devices lists printers bound to the account of the given token.
func Devices(token string) ([]Device, error) {
	req, err := http.NewRequest("GET", apiBase+"/v1/iot-service/api/user/bind", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range apiHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("user/bind: HTTP %d: %s", resp.StatusCode, snippet)
	}
	var body struct {
		Devices []Device `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Devices, nil
}
