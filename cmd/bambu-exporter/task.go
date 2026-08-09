package main

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/polarn/waybar-modules/pkg/bambu"
)

// taskCache holds the printer's latest job from the cloud REST API, plus
// the bytes of its plate-preview render.
//
// This is a separate poll from the MQTT stream because it is a different
// service: the MQTT report says what the printer is doing, the tasks API
// says what was sliced. It refreshes slowly on purpose — a job changes at
// most every few hours, and this is the only thing in the exporter that
// costs Bambu an HTTP request.
type taskCache struct {
	token  string
	serial string

	mu    sync.RWMutex
	task  *bambu.Task
	cover []byte
	ctype string
}

func newTaskCache(token, serial string) *taskCache {
	return &taskCache{token: token, serial: serial}
}

// run refreshes until ctx is done. Failures are logged and retried at the
// normal interval: the plate preview is a nicety, and losing it must not
// affect metrics or /state.
func (c *taskCache) run(done <-chan struct{}, logf func(string, ...any)) {
	const every = 5 * time.Minute
	for {
		if err := c.refresh(); err != nil && !errors.Is(err, bambu.ErrNoTask) {
			logf("plate preview unavailable: %v", err)
		}
		select {
		case <-done:
			return
		case <-time.After(every):
		}
	}
}

func (c *taskCache) refresh() error {
	task, err := bambu.LatestTask(c.token, c.serial)
	if err != nil {
		return err
	}

	c.mu.RLock()
	sameCover := c.task != nil && c.task.Cover == task.Cover && len(c.cover) > 0
	c.mu.RUnlock()
	if sameCover {
		c.mu.Lock()
		c.task = task
		c.mu.Unlock()
		return nil
	}

	// Fetch the render once and serve it ourselves, so the status page has
	// no third-party requests and keeps working on a browser without
	// internet access.
	img, ctype, err := fetchImage(task.Cover)
	if err != nil {
		c.mu.Lock()
		c.task = task
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.task, c.cover, c.ctype = task, img, ctype
	c.mu.Unlock()
	return nil
}

func (c *taskCache) get() (*bambu.Task, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.task == nil {
		return nil, false
	}
	t := *c.task
	return &t, true
}

func (c *taskCache) coverBytes() ([]byte, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.cover) == 0 {
		return nil, "", false
	}
	return c.cover, c.ctype, true
}

func fetchImage(rawURL string) ([]byte, string, error) {
	if rawURL == "" {
		return nil, "", errors.New("task has no cover image")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", errors.New("cover image: " + resp.Status)
	}
	// Bounded read: this is remote input rendered into our own page.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	ctype := resp.Header.Get("Content-Type")
	// Bambu's CDN serves these as binary/octet-stream, which a browser
	// will not render as an image.
	if ctype == "" || ctype == "binary/octet-stream" || ctype == "application/octet-stream" {
		ctype = http.DetectContentType(b)
	}
	return b, ctype, nil
}
