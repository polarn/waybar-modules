// volvo-exporter polls Volvo's cloud API for one car and is the only
// holder of its rotating refresh token. It serves:
//
//	$VOLVO_METRICS_ADDR/metrics  (:9090) Prometheus text
//	$VOLVO_HTTP_ADDR/state       (:8080) volvo.ExporterState JSON for volvo-ctl
//	$VOLVO_HTTP_ADDR/healthz     process liveness
//	127.0.0.1:20999/auth         browser login, via kubectl port-forward
//
// Env: VOLVO_CONFIG_JSON (config.json contents, vin required),
// VOLVO_DATA_DIR (holds tokens.json, default /data), VOLVO_INTERVAL
// (default 5m).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/polarn/waybar-modules/pkg/volvo"
)

var leaves = []string{"doors", "windows", "engine-status", "tyres", "warnings", "diagnostics", "odometer", "statistics"}

func main() {
	log.SetFlags(0)

	cfg, err := volvo.ParseConfig([]byte(os.Getenv("VOLVO_CONFIG_JSON")), "$VOLVO_CONFIG_JSON")
	if err != nil {
		log.Fatalf("volvo-exporter: %v", err)
	}
	if cfg.VIN == "" {
		log.Fatal("volvo-exporter: config has no vin")
	}
	if cfg.RedirectURI == "" {
		log.Fatal("volvo-exporter: config has no redirect_uri")
	}
	interval := 5 * time.Minute
	if v := os.Getenv("VOLVO_INTERVAL"); v != "" {
		if interval, err = time.ParseDuration(v); err != nil || interval < time.Minute {
			log.Fatalf("volvo-exporter: VOLVO_INTERVAL %q: want a duration of at least 1m", v)
		}
	}
	store := &volvo.TokenStore{Path: filepath.Join(envOr("VOLVO_DATA_DIR", "/data"), "tokens.json")}

	p := newPoller(volvo.NewClient(cfg, store), cfg.VIN, interval)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		p.run(ctx)
		close(done)
	}()

	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, collect(p.view()))
	})

	appMux := http.NewServeMux()
	appMux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		v := p.view()
		st := volvo.ExporterState{
			AuthOK:      v.authOK,
			LastPoll:    v.lastPoll,
			Snapshot:    v.snap,
			IntervalSec: int(interval.Seconds()),
		}
		if v.lastErr != nil {
			st.LastError = v.lastErr.Error()
		}
		if !v.retryUntil.IsZero() {
			st.RetryAfter = v.retryUntil.UTC().Format(time.RFC3339)
		}
		w.Header().Set("Content-Type", "application/json")
		if st.Snapshot == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	servers := []*http.Server{
		newServer(envOr("VOLVO_METRICS_ADDR", ":9090"), metricsMux),
		newServer(envOr("VOLVO_HTTP_ADDR", ":8080"), appMux),
		newServer(fmt.Sprintf("127.0.0.1:%d", cfg.CallbackPort()), newAuthMux(cfg, store, p.kick)),
	}
	for _, s := range servers {
		go func(s *http.Server) {
			log.Printf("volvo-exporter: listening on %s", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("volvo-exporter: %s: %v", s.Addr, err)
			}
		}(s)
	}

	<-ctx.Done()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		log.Print("volvo-exporter: poll still running at shutdown; a token refresh may be lost")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
}

type view struct {
	snap       *volvo.Snapshot
	authOK     bool
	lastPoll   time.Time
	lastOK     time.Time
	lastErr    error
	retryUntil time.Time
	polls      map[string]float64
	softErrors float64
}

type poller struct {
	client   *volvo.Client
	vin      string
	interval time.Duration
	kick     chan struct{}

	mu       sync.Mutex
	v        view
	failures int
}

func newPoller(c *volvo.Client, vin string, interval time.Duration) *poller {
	return &poller{
		client:   c,
		vin:      vin,
		interval: interval,
		kick:     make(chan struct{}, 1),
		v:        view{authOK: true, polls: map[string]float64{}},
	}
}

func (p *poller) view() view {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.v
	v.polls = make(map[string]float64, len(p.v.polls))
	for k, n := range p.v.polls {
		v.polls[k] = n
	}
	return v
}

func (p *poller) run(ctx context.Context) {
	for {
		wait := p.poll()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-p.kick:
			t.Stop()
		case <-t.C:
		}
	}
}

func (p *poller) poll() time.Duration {
	var softLimit *volvo.RateLimitError
	snap, err := p.client.FetchSnapshot(p.vin, leaves, func(err error) {
		log.Printf("volvo-exporter: %v", err)
		var rl *volvo.RateLimitError
		if errors.As(err, &rl) {
			softLimit = rl
		}
		p.mu.Lock()
		p.v.softErrors++
		p.mu.Unlock()
	})

	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.v.lastPoll = now
	p.v.lastErr = err
	p.v.retryUntil = time.Time{}

	var rl *volvo.RateLimitError
	result := "error"
	switch {
	case err == nil:
		result = "ok"
		p.v.snap, p.v.authOK, p.v.lastOK = snap, true, now
		p.failures = 0
		rl = softLimit
	case errors.Is(err, volvo.ErrReauthNeeded), errors.Is(err, volvo.ErrNoTokens):
		result = "reauth"
		p.v.authOK = false
	case errors.As(err, &rl):
		result = "rate_limited"
	}
	p.v.polls[result]++
	if err != nil {
		log.Printf("volvo-exporter: poll: %v", err)
	}

	wait := p.interval
	if result == "error" || result == "rate_limited" {
		p.failures++
		wait = p.interval << min(p.failures, 4)
		if wait > time.Hour {
			wait = time.Hour
		}
	}
	if rl != nil && rl.RetryAfter > wait {
		wait = rl.RetryAfter
	}
	if wait != p.interval {
		p.v.retryUntil = now.Add(wait)
	}
	return wait
}

func newAuthMux(cfg *volvo.Config, store *volvo.TokenStore, kick chan<- struct{}) *http.ServeMux {
	var (
		mu      sync.Mutex
		pending *volvo.AuthRequest
		started time.Time
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		ar, err := volvo.NewAuthRequest(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		pending, started = ar, time.Now()
		mu.Unlock()
		http.Redirect(w, r, ar.URL, http.StatusFound)
	})
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ar := pending
		if time.Since(started) > 10*time.Minute {
			ar = nil
		}
		mu.Unlock()
		if ar == nil {
			http.Error(w, "no login in progress — open /auth first", http.StatusBadRequest)
			return
		}
		code, err := ar.CallbackCode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tok, err := ar.Exchange(cfg, code)
		if err != nil {
			log.Printf("volvo-exporter: login: %v", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := store.WithLock(func() error { return store.Save(tok) }); err != nil {
			log.Printf("volvo-exporter: save tokens: %v", err)
			http.Error(w, "save tokens: "+err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		pending = nil
		mu.Unlock()
		select {
		case kick <- struct{}{}:
		default:
		}
		log.Print("volvo-exporter: login complete, new tokens saved")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><p>volvo-exporter is logged in. You can close this tab and stop the port-forward.</p></body></html>")
	})
	return mux
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
