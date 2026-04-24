package dirigera

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Client is an authenticated DIRIGERA REST client.
type Client struct {
	Host  string
	Port  int
	Token string
	HTTP  *http.Client
}

// NewClient returns a client preconfigured to skip TLS verification
// (DIRIGERA ships a self-signed cert) and use a short request timeout.
func NewClient(host, token string) *Client {
	return &Client{
		Host:  host,
		Port:  DefaultPort,
		Token: token,
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func (c *Client) baseURL() string {
	p := c.Port
	if p == 0 {
		p = DefaultPort
	}
	return fmt.Sprintf("https://%s:%d", c.Host, p)
}

// Device is a minimal projection of DIRIGERA's device JSON — we only
// care about lights and the attributes that affect display.
type Device struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`       // "light", "outlet", ...
	DeviceType string     `json:"deviceType"` // same as Type, usually
	Attributes Attributes `json:"attributes"`
}

// Attributes holds the light state fields we care about. DIRIGERA
// exposes more (battery, permitJoin, firmware, …) — we ignore them.
type Attributes struct {
	CustomName       string  `json:"customName"`
	IsOn             bool    `json:"isOn"`
	LightLevel       int     `json:"lightLevel"` // 0..100
	ColorMode        string  `json:"colorMode"`  // "color" | "colorTemperature"
	ColorHue         float64 `json:"colorHue"`
	ColorSaturation  float64 `json:"colorSaturation"`
	ColorTemperature int     `json:"colorTemperature"`
}

// SetLightOn flips the given device's on/off state via PATCH. Works
// for any light or outlet that exposes the isOn attribute.
func (c *Client) SetLightOn(id string, on bool) error {
	type body struct {
		Attributes struct {
			IsOn bool `json:"isOn"`
		} `json:"attributes"`
	}
	var b [1]body
	b[0].Attributes.IsOn = on
	return c.patchDevice(id, b[:])
}

// SetLightBrightness sets the brightness to 0..100. No-op if the light
// is off (DIRIGERA accepts the write but doesn't visibly change anything
// until isOn=true).
func (c *Client) SetLightBrightness(id string, pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	type body struct {
		Attributes struct {
			LightLevel int `json:"lightLevel"`
		} `json:"attributes"`
	}
	var b [1]body
	b[0].Attributes.LightLevel = pct
	return c.patchDevice(id, b[:])
}

func (c *Client) patchDevice(id string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPatch, c.baseURL()+"/v1/devices/"+id, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("patch %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch %s: %s: %s", id, resp.Status, string(body))
	}
	return nil
}

// Devices returns all devices known to the hub.
func (c *Client) Devices() ([]Device, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL()+"/v1/devices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devices request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devices: %s: %s", resp.Status, string(body))
	}

	var out []Device
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse devices: %w", err)
	}
	return out, nil
}

// Hex returns a `#rrggbb` string approximating the light's current
// emitted color. Off lights return an empty string (caller decides what
// to show for "off").
func (a Attributes) Hex() string {
	if !a.IsOn {
		return ""
	}
	var r, g, b uint8
	switch a.ColorMode {
	case "color":
		r, g, b = hsvToRGB(a.ColorHue, a.ColorSaturation, 1.0)
	default: // "colorTemperature" or missing
		k := a.ColorTemperature
		if k == 0 {
			k = 2700 // neutral fallback
		}
		r, g, b = kelvinToRGB(k)
	}
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// Label returns a short human-readable color description — e.g.
// "warm white", "cool white", "red", "teal". For off lights, "off".
func (a Attributes) Label() string {
	if !a.IsOn {
		return "off"
	}
	if a.ColorMode == "color" {
		return hueLabel(a.ColorHue)
	}
	return tempLabel(a.ColorTemperature)
}

// ---------------- color math ----------------

// hsvToRGB converts HSV (hue 0–360, s/v 0–1) to sRGB bytes.
// DIRIGERA's "ColorSaturation" maps onto S; the lamp is always at full V
// when on — brightness is decoupled and carried in LightLevel.
func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	if s <= 0 {
		c := uint8(clamp01(v) * 255)
		return c, c, c
	}
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	hh := h / 60
	i := int(hh)
	ff := hh - float64(i)
	p := v * (1 - s)
	q := v * (1 - s*ff)
	t := v * (1 - s*(1-ff))

	var r, g, b float64
	switch i {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default: // 5
		r, g, b = v, p, q
	}
	return uint8(clamp01(r) * 255), uint8(clamp01(g) * 255), uint8(clamp01(b) * 255)
}

// kelvinToRGB approximates a black-body color temperature in Kelvin to
// sRGB using Neil Bartlett's piecewise formula. Good enough for the
// 1500–6500 K range IKEA bulbs emit.
func kelvinToRGB(k int) (uint8, uint8, uint8) {
	t := float64(k) / 100.0
	var r, g, b float64

	// Red
	if t <= 66 {
		r = 255
	} else {
		r = 329.698727446 * math.Pow(t-60, -0.1332047592)
	}
	// Green
	if t <= 66 {
		g = 99.4708025861*math.Log(t) - 161.1195681661
	} else {
		g = 288.1221695283 * math.Pow(t-60, -0.0755148492)
	}
	// Blue
	switch {
	case t >= 66:
		b = 255
	case t <= 19:
		b = 0
	default:
		b = 138.5177312231*math.Log(t-10) - 305.0447927307
	}

	return clamp255(r), clamp255(g), clamp255(b)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp255(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// hueLabel bins HSV hue (0–360) into a coarse color name.
func hueLabel(h float64) string {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	switch {
	case h < 15 || h >= 345:
		return "red"
	case h < 45:
		return "orange"
	case h < 65:
		return "yellow"
	case h < 150:
		return "green"
	case h < 195:
		return "teal"
	case h < 255:
		return "blue"
	case h < 285:
		return "purple"
	case h < 345:
		return "pink"
	}
	return "red"
}

// tempLabel bins Kelvin values into warm/neutral/cool white.
func tempLabel(k int) string {
	switch {
	case k == 0:
		return "white"
	case k < 2500:
		return "warm white"
	case k < 3500:
		return "soft white"
	case k < 5000:
		return "neutral white"
	}
	return "cool white"
}
