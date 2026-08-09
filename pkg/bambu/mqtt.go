package bambu

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Broker is Bambu's cloud MQTT endpoint for the global (non-China) region.
const Broker = "us.mqtt.bambulab.com:8883"

// pushallPayload asks the printer for a full state snapshot. The printer
// rate-limits these to roughly one a minute, so callers must not send it
// per poll — see Subscribe, which sends one per connection and then lives
// off the partial pushes.
var pushallPayload = []byte(`{"pushing":{"sequence_id":"0","command":"pushall"}}`)

// Minimal MQTT 3.1.1 framing — just enough for this exchange (CONNECT,
// one SUBSCRIBE, one QoS-0 PUBLISH, then read PUBLISHes until the report
// arrives). Hand-rolled to keep the repo dependency-light; a full client
// library would be the bigger foreign codebase by far.

// dial opens a TLS connection to the broker, authenticates as the
// session user and subscribes to the printer's report topic. The
// returned conn has its deadline set to now+timeout.
func dial(s *Session, serial string, timeout time.Duration) (*tls.Conn, error) {
	user := s.MQTTUser
	if user == "" {
		var err error
		if user, err = s.MQTTUsername(); err != nil {
			if user, err = UsernameFromAPI(s.AccessToken); err != nil {
				return nil, fmt.Errorf("derive MQTT username: %w", err)
			}
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", Broker, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", Broker, err)
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	clientID := fmt.Sprintf("bambu-ctl-%d", time.Now().UnixNano())
	if err := writeConnect(conn, clientID, user, s.AccessToken); err != nil {
		return nil, err
	}
	first, payload, err := readPacket(conn)
	if err != nil {
		return nil, fmt.Errorf("read CONNACK: %w", err)
	}
	if first>>4 != 2 || len(payload) < 2 {
		return nil, fmt.Errorf("unexpected reply to CONNECT (type %d)", first>>4)
	}
	switch payload[1] {
	case 0: // accepted
	case 4, 5: // bad user name or password / not authorized
		return nil, ErrAuth
	default:
		return nil, fmt.Errorf("broker refused connection (code %d)", payload[1])
	}
	if err := writeSubscribe(conn, 1, "device/"+serial+"/report"); err != nil {
		return nil, err
	}
	ok = true
	return conn, nil
}

// FetchReport connects as the session user, asks the printer (by serial)
// for a full state push and returns the first report carrying print
// progress, plus its raw JSON. ErrAuth means the token is stale;
// ErrNoReport usually means the printer is powered off.
func FetchReport(s *Session, serial string, timeout time.Duration) (*Report, []byte, error) {
	conn, err := dial(s, serial, timeout)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	reportTopic := "device/" + serial + "/report"
	if err := writePublish(conn, "device/"+serial+"/request", pushallPayload); err != nil {
		return nil, nil, err
	}

	for {
		first, payload, err := readPacket(conn)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return nil, nil, ErrNoReport
			}
			return nil, nil, err
		}
		if first>>4 != 3 { // not a PUBLISH (SUBACK etc.)
			continue
		}
		topic, body, err := parsePublish(first, payload)
		if err != nil || topic != reportTopic {
			continue
		}
		var rep Report
		if json.Unmarshal(body, &rep) != nil {
			continue
		}
		// Partial pushes lack mc_percent; the pushall answer carries it.
		if rep.Print.McPercent == nil {
			continue
		}
		writeDisconnect(conn)
		return &rep, body, nil
	}
}

// ErrNoAck means a command was published but the printer never echoed a
// result — most often it is powered off.
var ErrNoAck = errors.New("command sent, but no acknowledgement from the printer (off?)")

// SendCommand publishes a control payload to the printer's request topic
// and waits for the echoed result on the report topic. section/command
// identify the ack to look for (e.g. "print"/"pause", "system"/"ledctrl").
func SendCommand(s *Session, serial, section, command string, payload any, timeout time.Duration) error {
	conn, err := dial(s, serial, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := writePublish(conn, "device/"+serial+"/request", body); err != nil {
		return err
	}

	reportTopic := "device/" + serial + "/report"
	for {
		first, pkt, err := readPacket(conn)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return ErrNoAck
			}
			return err
		}
		if first>>4 != 3 {
			continue
		}
		topic, msg, err := parsePublish(first, pkt)
		if err != nil || topic != reportTopic {
			continue
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(msg, &data) != nil {
			continue
		}
		var echo struct {
			Command string `json:"command"`
			Result  string `json:"result"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(data[section], &echo) != nil || echo.Command != command {
			continue
		}
		writeDisconnect(conn)
		if echo.Result != "" && !strings.EqualFold(echo.Result, "success") {
			if echo.Reason != "" {
				return fmt.Errorf("printer replied %q: %s", echo.Result, echo.Reason)
			}
			return fmt.Errorf("printer replied %q", echo.Result)
		}
		return nil
	}
}

// encodeString emits an MQTT length-prefixed string (uint16 big-endian).
// Fine for everything we send — the longest is the ~1.2 kB access token.
func encodeString(s string) []byte {
	b := make([]byte, 0, 2+len(s))
	b = append(b, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

// encodeRemLen emits the MQTT variable-length "remaining length".
func encodeRemLen(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func writePacket(w io.Writer, first byte, body []byte) error {
	pkt := append([]byte{first}, encodeRemLen(len(body))...)
	pkt = append(pkt, body...)
	_, err := w.Write(pkt)
	return err
}

func writeConnect(w io.Writer, clientID, user, pass string) error {
	var b []byte
	b = append(b, encodeString("MQTT")...)
	b = append(b, 4)    // protocol level: 3.1.1
	b = append(b, 0xC2) // flags: username + password + clean session
	// Keepalive 30 s. The one-shot calls finish well inside it; Subscribe
	// holds the connection open and pings under it (see writePing).
	b = append(b, 0, 30)
	b = append(b, encodeString(clientID)...)
	b = append(b, encodeString(user)...)
	b = append(b, encodeString(pass)...)
	return writePacket(w, 0x10, b)
}

func writeSubscribe(w io.Writer, packetID uint16, topic string) error {
	b := []byte{byte(packetID >> 8), byte(packetID)}
	b = append(b, encodeString(topic)...)
	b = append(b, 0) // QoS 0 — broker then downgrades all deliveries to us
	return writePacket(w, 0x82, b)
}

func writePublish(w io.Writer, topic string, payload []byte) error {
	b := encodeString(topic)
	b = append(b, payload...)
	return writePacket(w, 0x30, b)
}

// writePing sends PINGREQ; the broker answers PINGRESP (packet type 13).
// Only a long-lived subscription needs this.
func writePing(w io.Writer) error {
	return writePacket(w, 0xC0, nil)
}

func writeDisconnect(w io.Writer) {
	_ = writePacket(w, 0xE0, nil) // best effort; we're leaving anyway
}

func readPacket(r io.Reader) (byte, []byte, error) {
	hdr := make([]byte, 1)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	n, err := readRemLen(r)
	if err != nil {
		return 0, nil, err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func readRemLen(r io.Reader) (int, error) {
	mult, val := 1, 0
	buf := make([]byte, 1)
	for i := 0; i < 4; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		val += int(buf[0]&0x7F) * mult
		if buf[0]&0x80 == 0 {
			return val, nil
		}
		mult *= 128
	}
	return 0, errors.New("malformed remaining length")
}

// parsePublish splits a PUBLISH packet into topic and payload. QoS > 0
// deliveries carry a packet ID between the two (shouldn't happen — we
// subscribe at QoS 0 — but handle it rather than corrupt the payload).
func parsePublish(first byte, p []byte) (string, []byte, error) {
	if len(p) < 2 {
		return "", nil, errors.New("short PUBLISH")
	}
	tl := int(p[0])<<8 | int(p[1])
	if len(p) < 2+tl {
		return "", nil, errors.New("short PUBLISH topic")
	}
	topic := string(p[2 : 2+tl])
	rest := p[2+tl:]
	if qos := (first >> 1) & 3; qos > 0 {
		if len(rest) < 2 {
			return "", nil, errors.New("short PUBLISH packet ID")
		}
		rest = rest[2:]
	}
	return topic, rest, nil
}
