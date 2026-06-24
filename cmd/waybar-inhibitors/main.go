// waybar-inhibitors reports how many clients currently hold a screensaver
// idle-inhibitor, with a tooltip listing which apps (and why).
//
// hypridle owns org.freedesktop.ScreenSaver and counts Inhibit/UnInhibit calls
// internally ("inhibit locks: N" in its log) but exposes no way to enumerate
// them. So we eavesdrop the session bus (BecomeMonitor) and track the same
// Inhibit/UnInhibit traffic ourselves; the resulting count matches hypridle's.
//
// It does not print to stdout. On every change it writes a single waybar JSON
// line to a state file and pokes waybar with SIGRTMIN+N so the custom module
// re-reads it. Running as a long-lived user service (rather than being exec'd
// by waybar) means a waybar restart never desyncs the count.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/polarn/waybar-modules/pkg/waybar"
)

const screenSaverIface = "org.freedesktop.ScreenSaver"

// inhibitor is one held screensaver lock, keyed in state by its cookie.
type inhibitor struct {
	app    string // the application name passed to Inhibit()
	reason string // the reason passed to Inhibit()
	sender string // the caller's unique bus name (":1.42")
	pid    uint32
	comm   string // /proc/<pid>/comm, used when app is empty/generic
}

// pendingCall is an Inhibit method-call awaiting its reply (the reply carries
// the cookie). Keyed by the call's serial.
type pendingCall struct {
	app    string
	reason string
	sender string
}

type monitor struct {
	caller    *dbus.Conn // normal connection, for PID lookups
	signum    int
	stateFile string
	barName   string

	ssOwner string                 // unique name currently owning ScreenSaver (hypridle)
	cookies map[uint32]inhibitor   // live inhibitors by cookie
	pending map[uint32]pendingCall // Inhibit calls awaiting reply, by serial
	last    string                 // last published JSON line (dedupe)
}

func main() {
	signum := flag.Int("signal", 5, "waybar real-time signal (SIGRTMIN+N) to poke on change")
	stateFile := flag.String("state-file", defaultStateFile(), "path to write the waybar JSON line")
	barName := flag.String("waybar-process", "waybar", "exact process name to signal")
	flag.Parse()

	// A monitor connection is receive-only, so PID lookups need a separate
	// normal connection.
	caller, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatalf("connect session bus: %v", err)
	}
	defer caller.Close()

	mon, err := dbus.SessionBusPrivate()
	if err != nil {
		log.Fatalf("open monitor connection: %v", err)
	}
	if err := mon.Auth(nil); err != nil {
		log.Fatalf("monitor auth: %v", err)
	}
	if err := mon.Hello(); err != nil {
		log.Fatalf("monitor hello: %v", err)
	}

	// BecomeMonitor must happen before Eavesdrop: the call needs a normal
	// reply, after which the connection is monitor-only and every matching
	// message is delivered to our channel.
	rules := []string{
		"type='method_call',interface='org.freedesktop.ScreenSaver',member='Inhibit'",
		"type='method_call',interface='org.freedesktop.ScreenSaver',member='UnInhibit'",
		"type='method_return'", // carries Inhibit cookies; filtered by sender below
		"type='signal',interface='org.freedesktop.DBus',member='NameOwnerChanged'",
	}
	if call := mon.BusObject().Call("org.freedesktop.DBus.Monitoring.BecomeMonitor", 0, rules, uint32(0)); call.Err != nil {
		log.Fatalf("BecomeMonitor: %v", call.Err)
	}

	ch := make(chan *dbus.Message, 64)
	mon.Eavesdrop(ch)

	m := &monitor{
		caller:    caller,
		signum:    *signum,
		stateFile: *stateFile,
		barName:   *barName,
		cookies:   map[uint32]inhibitor{},
		pending:   map[uint32]pendingCall{},
	}
	m.ssOwner = m.nameOwner(screenSaverIface)
	log.Printf("monitoring %s (owner %q); state=%s; poke SIGRTMIN+%d -> %s",
		screenSaverIface, m.ssOwner, m.stateFile, m.signum, m.barName)

	// Write the initial state so waybar's first read always finds a file.
	m.publish()

	for msg := range ch {
		m.handle(msg)
	}
	log.Fatal("monitor channel closed (bus disconnected)")
}

func (m *monitor) handle(msg *dbus.Message) {
	switch msg.Type {
	case dbus.TypeMethodCall:
		if iface, _ := msg.Headers[dbus.FieldInterface].Value().(string); iface != screenSaverIface {
			return
		}
		member, _ := msg.Headers[dbus.FieldMember].Value().(string)
		sender, _ := msg.Headers[dbus.FieldSender].Value().(string)
		switch member {
		case "Inhibit":
			app, reason := bodyString(msg.Body, 0), bodyString(msg.Body, 1)
			m.pending[msg.Serial()] = pendingCall{app: app, reason: reason, sender: sender}
		case "UnInhibit":
			if c, ok := bodyUint32(msg.Body, 0); ok {
				if _, exists := m.cookies[c]; exists {
					delete(m.cookies, c)
					m.publish()
				}
			}
		}

	case dbus.TypeMethodReply:
		// Only trust replies from the ScreenSaver owner (hypridle); a reply
		// to an Inhibit carries a single uint32 cookie.
		sender, _ := msg.Headers[dbus.FieldSender].Value().(string)
		if m.ssOwner == "" || sender != m.ssOwner {
			return
		}
		rs, ok := msg.Headers[dbus.FieldReplySerial].Value().(uint32)
		if !ok {
			return
		}
		pc, ok := m.pending[rs]
		if !ok {
			return
		}
		delete(m.pending, rs)
		cookie, ok := bodyUint32(msg.Body, 0)
		if !ok {
			return // not an Inhibit reply (UnInhibit/GetActive return other shapes)
		}
		pid := m.connPID(pc.sender)
		m.cookies[cookie] = inhibitor{
			app:    pc.app,
			reason: pc.reason,
			sender: pc.sender,
			pid:    pid,
			comm:   commForPID(pid),
		}
		m.publish()

	case dbus.TypeSignal:
		if member, _ := msg.Headers[dbus.FieldMember].Value().(string); member != "NameOwnerChanged" {
			return
		}
		name, newOwner := bodyString(msg.Body, 0), bodyString(msg.Body, 2)
		if name == screenSaverIface {
			m.ssOwner = newOwner // hypridle restarted, etc.
		}
		// A client connection that vanished releases its inhibits (hypridle
		// does the same); drop anything we tracked for it.
		if strings.HasPrefix(name, ":") && newOwner == "" {
			changed := false
			for c, inh := range m.cookies {
				if inh.sender == name {
					delete(m.cookies, c)
					changed = true
				}
			}
			for s, pc := range m.pending {
				if pc.sender == name {
					delete(m.pending, s)
				}
			}
			if changed {
				m.publish()
			}
		}
	}
}

func (m *monitor) publish() {
	w := waybar.New()
	n := len(m.cookies)
	w.Text = fmt.Sprintf("%d", n)
	if n == 0 {
		w.Class = "idle"
		w.ToolTip = "Nothing is holding the screensaver lock"
	} else {
		w.Class = "active"
		w.ToolTip = buildTooltip(m.cookies)
	}

	b, err := json.Marshal(w)
	if err != nil {
		log.Printf("marshal: %v", err)
		return
	}
	if string(b) == m.last {
		return // unchanged; don't churn waybar
	}
	m.last = string(b)

	if err := writeFileAtomic(m.stateFile, append(b, '\n')); err != nil {
		log.Printf("write state file: %v", err)
		return
	}
	m.pokeWaybar()
}

func buildTooltip(cookies map[uint32]inhibitor) string {
	list := make([]inhibitor, 0, len(cookies))
	for _, inh := range cookies {
		list = append(list, inh)
	}
	// Stable order so identical state produces an identical line (dedupe).
	sort.Slice(list, func(i, j int) bool {
		if list[i].app != list[j].app {
			return list[i].app < list[j].app
		}
		return list[i].pid < list[j].pid
	})

	var b strings.Builder
	plural := "s"
	if len(list) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "<b>%d app%s holding the screensaver lock</b>", len(list), plural)
	for _, inh := range list {
		name := inh.app
		if name == "" {
			name = inh.comm
		}
		if name == "" {
			name = "(unknown)"
		}
		b.WriteString("\n• ")
		b.WriteString(escapePango(name))
		if inh.reason != "" {
			b.WriteString(" — ")
			b.WriteString(escapePango(inh.reason))
		}
		var det []string
		if inh.comm != "" && !strings.EqualFold(inh.comm, inh.app) {
			det = append(det, inh.comm)
		}
		if inh.pid != 0 {
			det = append(det, fmt.Sprintf("pid %d", inh.pid))
		}
		if len(det) > 0 {
			fmt.Fprintf(&b, " <small>(%s)</small>", escapePango(strings.Join(det, ", ")))
		}
	}
	return b.String()
}

func (m *monitor) pokeWaybar() {
	// waybar re-runs a custom module on SIGRTMIN+N. -x exact-matches so we
	// don't signal ourselves or sibling waybar-* daemons. Best-effort:
	// pkill exits non-zero when waybar isn't up yet.
	_ = exec.Command("pkill", fmt.Sprintf("-RTMIN+%d", m.signum), "-x", m.barName).Run()
}

func (m *monitor) nameOwner(name string) string {
	var owner string
	if err := m.caller.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
		return ""
	}
	return owner
}

func (m *monitor) connPID(sender string) uint32 {
	if sender == "" {
		return 0
	}
	var pid uint32
	if err := m.caller.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, sender).Store(&pid); err != nil {
		return 0
	}
	return pid
}

func commForPID(pid uint32) string {
	if pid == 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func defaultStateFile() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "waybar-inhibitors.json")
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".waybar-inhibitors-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func bodyString(body []any, i int) string {
	if i >= len(body) {
		return ""
	}
	s, _ := body[i].(string)
	return s
}

func bodyUint32(body []any, i int) (uint32, bool) {
	if i >= len(body) {
		return 0, false
	}
	v, ok := body[i].(uint32)
	return v, ok
}

// escapePango escapes the five XML entities so app-supplied names/reasons can't
// break (or inject) waybar's Pango-markup tooltip.
func escapePango(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"'", "&#39;",
		"\"", "&#34;",
	).Replace(s)
}
