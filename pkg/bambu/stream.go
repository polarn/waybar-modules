package bambu

import (
	"context"
	"errors"
	"time"
)

// Subscribe keeps a persistent subscription to the printer's report topic,
// merging every message into st until ctx is cancelled.
//
// This exists because FetchReport cannot be used on a timer: it asks for a
// pushall each call, and the printer rate-limits those to roughly one a
// minute. Holding the subscription open instead means the continuous
// partial pushes — the ones FetchReport discards — become the data source,
// so consumers can read as often as they like at no cost to the printer.
//
// Reconnects with backoff on transient failures. ErrAuth is fatal and
// returned to the caller, since retrying an expired token is pointless.
// logf receives connection-level events; pass nil to discard them.
func Subscribe(ctx context.Context, s *Session, serial string, st *State, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	const (
		// The floor is 30s, not a second or two, because the broker limits
		// new MQTT connections to roughly 5 per 5 minutes per account. From
		// 30s the doubling gives 4 attempts in the first five minutes of an
		// outage (0s, 30s, 90s, 210s); from 2s it gives 8, which would trip
		// the limit exactly when the network is already unhappy. Nothing
		// here needs to recover faster than that — a dropped stream is
		// noticed within readTimeout anyway.
		minBackoff = 30 * time.Second
		maxBackoff = 2 * time.Minute
		// A connection that lasted this long was healthy, so the next
		// failure starts over from minBackoff rather than inheriting a
		// long delay from an unrelated outage hours earlier.
		healthy = 5 * time.Minute
	)

	backoff := minBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := time.Now()
		err := stream(ctx, s, serial, st, logf)
		if errors.Is(err, ErrAuth) {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if time.Since(start) >= healthy {
			backoff = minBackoff
		}
		logf("subscription dropped (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// stream runs one connection to exhaustion and returns why it ended.
func stream(ctx context.Context, s *Session, serial string, st *State, logf func(string, ...any)) error {
	const (
		dialTimeout = 20 * time.Second
		// Under the 30 s keepalive advertised in CONNECT, so the broker
		// always hears from us in time.
		pingEvery = 20 * time.Second
		// Well clear of the printer's ~1/min pushall limit. Deltas keep
		// the state current; this only repairs drift from a missed message.
		resyncEvery = 10 * time.Minute
		// We ping every 20 s and the broker answers, so this much silence
		// means the session is dead even though the socket looks open.
		readTimeout = 90 * time.Second
	)

	conn, err := dial(s, serial, dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	// dial leaves a whole-connection deadline set for its short exchange.
	// Clear it or the first keepalive write fails once that instant passes.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	requestTopic := "device/" + serial + "/request"
	reportTopic := "device/" + serial + "/report"

	// Writer goroutine: baseline snapshot, keepalive, periodic resync.
	// crypto/tls permits one concurrent reader and writer, and every write
	// happens here, so no lock is needed.
	writeErr := make(chan error, 1)
	go func() {
		send := func(f func() error) bool {
			if e := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil {
				writeErr <- e
				return false
			}
			if e := f(); e != nil {
				writeErr <- e
				return false
			}
			return true
		}
		pushall := func() bool {
			return send(func() error { return writePublish(conn, requestTopic, pushallPayload) })
		}
		if !pushall() {
			return
		}
		ping := time.NewTicker(pingEvery)
		defer ping.Stop()
		resync := time.NewTicker(resyncEvery)
		defer resync.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				if !send(func() error { return writePing(conn) }) {
					return
				}
			case <-resync.C:
				if !pushall() {
					return
				}
			}
		}
	}()

	// Unblock the read when the caller cancels.
	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()

	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}
		first, payload, err := readPacket(conn)
		if err != nil {
			// A failed write kills the connection too; prefer its error,
			// which says what actually went wrong.
			select {
			case werr := <-writeErr:
				return werr
			default:
			}
			return err
		}
		switch first >> 4 {
		case 3: // PUBLISH
			topic, body, err := parsePublish(first, payload)
			if err != nil || topic != reportTopic {
				continue
			}
			if err := st.Apply(body); err != nil {
				logf("discarding unparsable report: %v", err)
			}
		case 13: // PINGRESP — the keepalive landed
		}
	}
}
