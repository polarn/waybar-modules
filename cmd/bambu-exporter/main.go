// bambu-exporter holds one persistent subscription to a Bambu Lab
// printer's cloud MQTT topic and serves what it hears two ways: Prometheus
// metrics for long-term history, and the merged report as JSON for the
// waybar pill.
//
// It exists because the printer rate-limits pushall to roughly one a
// minute, so nothing can poll the cloud per scrape. One held-open
// subscription feeds every consumer at no cost to the printer.
//
// Endpoints (two listeners, so /metrics stays off the LAN ingress):
//
//	$BAMBU_METRICS_ADDR/metrics   Prometheus text, cluster-internal
//	$BAMBU_HTTP_ADDR/state        merged report JSON, for bambu-ctl waybar
//	$BAMBU_HTTP_ADDR/healthz      process liveness
//
// Config is environment-only, to suit a Kubernetes envFrom: secretRef.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
)

func main() {
	log.SetFlags(0)

	sess, err := loadSession()
	if err != nil {
		log.Fatalf("bambu-exporter: %v", err)
	}
	serial := envOr("BAMBU_SERIAL", sess.Serial)
	if serial == "" {
		log.Fatal("bambu-exporter: no printer serial in the session; set BAMBU_SERIAL")
	}
	printer := envOr("BAMBU_PRINTER_NAME", sess.Name)
	if printer == "" {
		printer = "bambu"
	}

	var (
		st     bambu.State
		authOK atomic.Bool
	)
	authOK.Store(true)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		err := bambu.Subscribe(ctx, sess, serial, &st, func(f string, a ...any) {
			log.Printf("bambu-exporter: "+f, a...)
		})
		switch {
		case err == nil || errors.Is(err, context.Canceled):
			return
		case errors.Is(err, bambu.ErrAuth):
			// Keep serving rather than exiting. A crash loop would hide the
			// reason; this way bambulab_cloud_auth_ok goes to 0 and stays
			// scrapeable until someone runs bambu-ctl login and rotates the
			// secret. Retrying in-process cannot help — the token is baked
			// into the environment until the pod restarts.
			authOK.Store(false)
			log.Printf("bambu-exporter: %v — serving stale state until the token is replaced", err)
		default:
			log.Printf("bambu-exporter: subscription gave up: %v", err)
		}
	}()

	// The plate render from the cloud tasks API. Not the camera: a live
	// stream needs LAN-only mode + LAN Only Liveview, which would disable
	// Bambu Cloud entirely (the report shows ipcam.rtsp_url "disable" and
	// ipcam.tutk_server "enable" — cloud video rides ThroughTek P2P).
	//
	// It also carries the only filament-grams figure in the whole pipeline,
	// which is why /metrics reads it too and this is constructed before the
	// handler that closes over it.
	tasks := newTaskCache(sess.AccessToken, serial)
	go tasks.run(ctx.Done(), func(f string, a ...any) {
		log.Printf("bambu-exporter: "+f, a...)
	})

	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, collect(printer, &st, sess, authOK.Load(), tasks))
	})

	appMux := http.NewServeMux()
	appMux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	appMux.HandleFunc("/cover", func(w http.ResponseWriter, r *http.Request) {
		img, ctype, ok := tasks.coverBytes()
		if !ok {
			http.Error(w, "no plate preview available", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ctype)
		// Immutable per job: the URL carries a per-cover cache buster.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(img)
	})
	appMux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := st.Raw()
		if !ok {
			http.Error(w, `{"error":"no report from the printer yet"}`, http.StatusServiceUnavailable)
			return
		}
		age, _ := st.Age()
		w.Header().Set("Content-Type", "application/json")
		// The whole merged report goes out, not a projection: the pill
		// decodes it with its own Report struct, so a client newer than the
		// exporter still finds the fields it knows about. The serial stays
		// out — it is credential-adjacent and this endpoint is on the LAN.
		env := stateEnvelope{Printer: printer, AgeSeconds: age.Seconds(), Report: raw}
		// The printer's own display wording for stg_cur, resolved here so
		// the page never shows a raw stage code (it is -1 when idle).
		if rep, ok := st.Report(); ok {
			if s := rep.Print.StgCur; s != nil && !bambu.StageIdle(s.Int()) {
				env.Stage = bambu.StageName(s.Int())
			}
		}
		if t, ok := tasks.get(); ok {
			_, _, coverOK := tasks.coverBytes()
			env.Task = &taskInfo{
				Title:           t.Title,
				WeightGrams:     t.Weight,
				DurationSeconds: t.CostTime,
				StartedAt:       t.StartTime,
				HasCover:        coverOK,
				// Changes when the job does, so the page's <img> reloads
				// instead of showing the previous print's render.
				CoverID: coverID(t.Cover),
			}
		}
		_ = json.NewEncoder(w).Encode(env)
	})
	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately not tied to report freshness: a powered-off printer
		// is not an unhealthy exporter, and failing here would restart the
		// pod every time the printer sleeps. Freshness is
		// bambulab_report_age_seconds, alerted in Prometheus.
		fmt.Fprintln(w, "ok")
	})

	servers := []*http.Server{
		newServer(envOr("BAMBU_METRICS_ADDR", ":9090"), metricsMux),
		newServer(envOr("BAMBU_HTTP_ADDR", ":8080"), appMux),
	}
	for _, s := range servers {
		go func(s *http.Server) {
			log.Printf("bambu-exporter: listening on %s", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("bambu-exporter: %s: %v", s.Addr, err)
			}
		}(s)
	}

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
}

// indexHTML is the status page. Embedded because the runtime image is
// `scratch` — there is no filesystem to serve it from.
//
//go:embed index.html
var indexHTML []byte

// stateEnvelope is what /state returns. The full merged report rides along
// as raw JSON so clients decode it with their own struct and a client
// newer than the exporter still finds the fields it knows. The printer
// serial is deliberately absent: it is credential-adjacent and this
// endpoint is reachable from the LAN.
type stateEnvelope struct {
	Printer    string          `json:"printer"`
	AgeSeconds float64         `json:"age_seconds"`
	Stage      string          `json:"stage,omitempty"`
	Task       *taskInfo       `json:"task,omitempty"`
	Report     json.RawMessage `json:"report"`
}

// taskInfo is the sliced-job metadata behind the plate preview. The cover
// URL itself is not exposed — the page loads /cover instead, so it has no
// third-party requests.
type taskInfo struct {
	Title           string  `json:"title"`
	WeightGrams     float64 `json:"weight_grams,omitempty"`
	DurationSeconds int     `json:"duration_seconds,omitempty"`
	StartedAt       string  `json:"started_at,omitempty"`
	HasCover        bool    `json:"has_cover"`
	CoverID         string  `json:"cover_id,omitempty"`
}

// coverID is a short stable id for a cover URL, used only to bust the
// browser cache when the job changes.
func coverID(coverURL string) string {
	if coverURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(coverURL))
	return hex.EncodeToString(sum[:6])
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// loadSession prefers the token in the environment (how it arrives in
// Kubernetes, via envFrom: secretRef) and falls back to the file on disk
// so the binary can be run locally against the same credentials as
// bambu-ctl.
func loadSession() (*bambu.Session, error) {
	if raw := os.Getenv("BAMBU_SESSION_JSON"); raw != "" {
		var s bambu.Session
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("parse BAMBU_SESSION_JSON: %w", err)
		}
		if s.AccessToken == "" {
			return nil, errors.New("BAMBU_SESSION_JSON has no access_token")
		}
		return &s, nil
	}
	path := os.Getenv("BAMBU_SESSION_PATH")
	if path == "" {
		var err error
		if path, err = bambu.DefaultPath(); err != nil {
			return nil, err
		}
	}
	return bambu.LoadSession(path)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
