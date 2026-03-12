package main

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
)

type PlayerStatus struct {
	Status string `json:"status"`
	Mode   string `json:"mode"`
	Title  string `json:"Title"`
	Artist string `json:"Artist"`
	Album  string `json:"Album"`
}

type MetaInfo struct {
	MetaData MetaData `json:"metaData"`
}

type MetaData struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtURI string `json:"albumArtURI"`
}

type TuneInResponse struct {
	Body []TuneInStation `json:"body"`
}

type TuneInStation struct {
	Name     string `json:"name"`
	Slogan   string `json:"slogan"`
	Genre    string `json:"genre_name"`
	Location string `json:"location"`
}

var modeNames = map[string]string{
	"1":  "AirPlay",
	"2":  "DLNA",
	"10": "Network",
	"31": "Spotify",
	"32": "Tidal",
	"40": "Line-In",
	"41": "Bluetooth",
	"43": "Optical",
}

var tuneInIDRegex = regexp.MustCompile(`opml\.radiotime\.com/Tune\.ashx\?id=(s\d+)`)

func hexDecode(s string) string {
	b, err := hex.DecodeString(s)
	if err != nil {
		return s
	}
	return string(b)
}

func isUseful(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return false
	}
	if strings.EqualFold(s, "unknow") || strings.EqualFold(s, "unknown") {
		return false
	}
	return true
}

func modeName(mode string) string {
	if name, ok := modeNames[mode]; ok {
		return name
	}
	return "Source " + mode
}

func extractTuneInID(s string) string {
	m := tuneInIDRegex.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func resolveRadioStation(client *http.Client, stationID string) *TuneInStation {
	url := fmt.Sprintf("http://opml.radiotime.com/Describe.ashx?id=%s&render=json", stationID)
	resp, err := fetchJSON[TuneInResponse](client, url)
	if err != nil || len(resp.Body) == 0 {
		return nil
	}
	return &resp.Body[0]
}

func main() {
	host := flag.String("host", "", "WiiM device IP address or hostname")
	interval := flag.Int("interval", 5, "Polling interval in seconds")
	flag.Parse()

	if *host == "" {
		log.Fatal("--host is required")
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	baseURL := fmt.Sprintf("https://%s/httpapi.asp?command=", *host)
	stationCache := make(map[string]*TuneInStation)

	for {
		ps, err := fetchJSON[PlayerStatus](client, baseURL+"getPlayerStatus")
		if err != nil {
			log.Println(err)
			time.Sleep(time.Duration(*interval) * time.Second)
			continue
		}

		w := waybar.New()

		isPhysicalInput := ps.Mode == "40" || ps.Mode == "41" || ps.Mode == "43"

		if ps.Status != "play" && !isPhysicalInput {
			w.Class = "stopped"
			w.Alt = "stopped"
		} else if isPhysicalInput {
			w.Text = modeName(ps.Mode)
			w.Class = strings.ToLower(modeName(ps.Mode))
			w.Alt = strings.ToLower(modeName(ps.Mode))
		} else {
			// Try getMetaInfo first — it returns plain text.
			meta, metaErr := fetchJSON[MetaInfo](client, baseURL+"getMetaInfo")

			var title, artist, album string

			if metaErr == nil {
				title = meta.MetaData.Title
				artist = meta.MetaData.Artist
				album = meta.MetaData.Album
			}

			// Check if this is a TuneIn radio stream.
			if stationID := extractTuneInID(title); stationID != "" {
				station, cached := stationCache[stationID]
				if !cached {
					station = resolveRadioStation(client, stationID)
					stationCache[stationID] = station
				}
				if station != nil {
					w.Text = station.Name
					var tip []string
					for _, s := range []string{station.Name, station.Slogan, station.Genre, station.Location} {
						if s != "" {
							tip = append(tip, s)
						}
					}
					w.ToolTip = strings.Join(tip, "\n")
				} else {
					w.Text = "Radio"
				}
				w.Class = "playing"
				w.Alt = "playing"
			} else {
				// Filter out junk values.
				if !isUseful(title) {
					title = ""
				}
				if !isUseful(artist) {
					artist = ""
				}
				if !isUseful(album) {
					album = ""
				}

				// If getMetaInfo gave us nothing useful, try hex-decoded getPlayerStatus fields.
				if title == "" && artist == "" {
					t := hexDecode(ps.Title)
					a := hexDecode(ps.Artist)
					al := hexDecode(ps.Album)
					if isUseful(t) {
						title = t
					}
					if isUseful(a) {
						artist = a
					}
					if isUseful(al) {
						album = al
					}
				}

				if title != "" || artist != "" {
					switch {
					case artist != "" && title != "":
						w.Text = fmt.Sprintf("%s - %s", artist, title)
					case title != "":
						w.Text = title
					default:
						w.Text = artist
					}

					var tip []string
					for _, s := range []string{title, artist, album} {
						if s != "" {
							tip = append(tip, s)
						}
					}
					w.ToolTip = strings.Join(tip, "\n")
					w.Class = "playing"
					w.Alt = "playing"
				} else {
					// No metadata — show source name for non-streaming inputs.
					w.Text = modeName(ps.Mode)
					w.Class = "stopped"
					w.Alt = "stopped"
				}
			}
		}

		if err := w.Print(); err != nil {
			log.Printf("Error printing waybar output: %s", err)
		}

		time.Sleep(time.Duration(*interval) * time.Second)
	}
}

func fetchJSON[T any](client *http.Client, url string) (*T, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &result, nil
}
