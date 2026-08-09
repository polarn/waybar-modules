package bambu

import (
	"encoding/json"
	"sync"
	"time"
)

// State accumulates a printer's MQTT reports into a single view.
//
// The printer answers pushall with a full snapshot and then streams
// partial "push_status" messages carrying only the fields that changed.
// A consumer that reads the newest message on its own therefore sees most
// fields as absent, which renders as zero — so State merges each message
// over what came before, and absent means unchanged.
//
// The merge runs on decoded JSON rather than on Report values so that
// fields Report does not model yet survive too. That is the same blind
// spot that hid the chamber temperature when it moved to device.ctc.
//
// Safe for concurrent use: the stream writes while /metrics and /state read.
type State struct {
	mu      sync.RWMutex
	raw     map[string]any
	updated time.Time
}

// Apply merges one report message into the accumulated state.
func (s *State) Apply(body []byte) error {
	var incoming map[string]any
	if err := json.Unmarshal(body, &incoming); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.raw == nil {
		s.raw = make(map[string]any, len(incoming))
	}
	mergeObject(s.raw, incoming)
	s.updated = time.Now()
	return nil
}

// Report decodes the accumulated state. ok is false until the first
// message has been applied.
func (s *State) Report() (*Report, bool) {
	b, ok := s.Raw()
	if !ok {
		return nil, false
	}
	rep := new(Report)
	if json.Unmarshal(b, rep) != nil {
		return nil, false
	}
	return rep, true
}

// Raw returns the accumulated state as JSON. The waybar client decodes
// this rather than a narrowed projection, so the pill keeps working when
// the exporter is older than the field it needs.
func (s *State) Raw() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw == nil {
		return nil, false
	}
	b, err := json.Marshal(s.raw)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Age reports how long ago the last message arrived. ok is false until
// the first one does. This is the honest liveness signal: the process can
// be healthy while its subscription has silently gone deaf.
func (s *State) Age() (time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.raw == nil {
		return 0, false
	}
	return time.Since(s.updated), true
}

// mergeObject recursively merges src over dst. Nested objects merge key by
// key; scalars and arrays replace wholesale. Arrays are deliberately not
// merged element-wise — the printer resends a whole list (the AMS trays)
// when any of it changes, and index-merging would strand entries that were
// removed upstream.
func mergeObject(dst, src map[string]any) {
	for k, v := range src {
		sub, isObj := v.(map[string]any)
		if !isObj {
			dst[k] = v
			continue
		}
		if existing, ok := dst[k].(map[string]any); ok {
			mergeObject(existing, sub)
			continue
		}
		// Copy rather than alias: the caller's decoded map must not stay
		// reachable from our state, or a later Apply could mutate it.
		cp := make(map[string]any, len(sub))
		mergeObject(cp, sub)
		dst[k] = cp
	}
}
