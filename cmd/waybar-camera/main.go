//go:build linux

// waybar-camera prints a waybar JSON line whenever webcam use changes: idle,
// active (the tooltip names who holds it, directly or through PipeWire) or
// absent. It watches /dev/video* with inotify rather than polling.
package main

import (
	"cmp"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/polarn/waybar-modules/pkg/waybar"
)

const (
	iconCamera    = "\U000F05A0"
	iconCameraOff = "\U000F1737"
	deviceMask    = syscall.IN_OPEN | syscall.IN_CLOSE_WRITE | syscall.IN_CLOSE_NOWRITE
)

var pipewireProcs = []string{"pipewire", "wireplumber"}

type holder struct {
	pid  int
	comm string
	dev  string
}

func main() {
	once := flag.Bool("once", false, "Print the current state once and exit")
	settle := flag.Duration("settle", 250*time.Millisecond, "Wait this long after an open or close before rescanning")
	poll := flag.Duration("poll", 2*time.Second, "Re-read the PipeWire graph this often while PipeWire holds a camera")
	flag.Parse()

	if *once {
		devs := cameras()
		w, _ := render(devs, scanHolders(devs))
		w.Print()
		return
	}

	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		log.Fatalf("inotify: %v", err)
	}
	if _, err := syscall.InotifyAddWatch(fd, "/dev", syscall.IN_CREATE|syscall.IN_DELETE); err != nil {
		log.Fatalf("watch /dev: %v", err)
	}
	changed := make(chan struct{}, 1)
	go readEvents(fd, changed)

	var (
		holders []holder
		wds     map[int]bool
		last    waybar.Waybar
		pollC   <-chan time.Time
	)
	settleT := time.NewTimer(0)
	for {
		rescan := false
		select {
		case <-changed:
			settleT.Reset(*settle)
			continue
		case <-settleT.C:
			rescan = true
		case <-pollC:
		}

		devs := cameras()
		newWds, failed := syncWatches(fd, devs)
		if rescan || !maps.Equal(newWds, wds) {
			holders = scanHolders(devs)
		}
		wds = newWds

		w, viaPipewire := render(devs, holders)
		if w != last {
			if err := w.Print(); err != nil {
				log.Printf("print: %v", err)
			}
			last = w
		}

		pollC = nil
		if viaPipewire || failed {
			pollC = time.After(*poll)
		}
	}
}

func cameras() map[string]string {
	paths, _ := filepath.Glob("/dev/video*")
	devs := map[string]string{}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			continue
		}
		devs[p] = deviceName(filepath.Base(p))
	}
	return devs
}

func deviceName(base string) string {
	b, err := os.ReadFile("/sys/class/video4linux/" + base + "/name")
	if err != nil {
		return base
	}
	name := strings.TrimSpace(string(b))
	if a, c, ok := strings.Cut(name, ": "); ok && a == c {
		return a
	}
	return name
}

func syncWatches(fd int, devs map[string]string) (map[int]bool, bool) {
	wds := map[int]bool{}
	failed := false
	for p := range devs {
		wd, err := syscall.InotifyAddWatch(fd, p, deviceMask)
		if err != nil {
			log.Printf("watch %s: %v", p, err)
			failed = true
			continue
		}
		wds[wd] = true
	}
	return wds, failed
}

func readEvents(fd int, changed chan<- struct{}) {
	buf := make([]byte, 64*(syscall.SizeofInotifyEvent+syscall.NAME_MAX+1))
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			log.Fatalf("inotify read: %v", err)
		}
		relevant := false
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			start := off + syscall.SizeofInotifyEvent
			name := strings.TrimRight(string(buf[start:start+nameLen]), "\x00")
			off = start + nameLen
			if name == "" || strings.HasPrefix(name, "video") {
				relevant = true
			}
		}
		if relevant {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	}
}

func scanHolders(devs map[string]string) []holder {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		log.Printf("read /proc: %v", err)
		return nil
	}
	var holders []holder
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + p.Name() + "/fd/"
		f, err := os.Open(dir)
		if err != nil {
			continue
		}
		fds, _ := f.Readdirnames(-1)
		f.Close()
		seen := map[string]bool{}
		for _, n := range fds {
			target, err := os.Readlink(dir + n)
			if err != nil || seen[target] {
				continue
			}
			if _, ok := devs[target]; ok {
				seen[target] = true
				comm, _ := os.ReadFile("/proc/" + p.Name() + "/comm")
				holders = append(holders, holder{pid: pid, comm: strings.TrimSpace(string(comm)), dev: target})
			}
		}
	}
	return holders
}

func render(devs map[string]string, holders []holder) (waybar.Waybar, bool) {
	w := waybar.New()
	if len(devs) == 0 {
		w.Text, w.Class, w.Alt, w.ToolTip = iconCameraOff, "absent", "absent", "No camera"
		return w, false
	}

	users := map[string][]string{}
	viaPipewire, pwLoaded := false, false
	var pw map[string][]string
	var pwErr error
	for _, h := range holders {
		name := devs[h.dev]
		if slices.Contains(pipewireProcs, h.comm) {
			viaPipewire = true
			if !pwLoaded {
				if pw, pwErr = pipewireUsers(devs); pwErr != nil {
					log.Printf("pw-dump: %v", pwErr)
				}
				pwLoaded = true
			}
			if pwErr == nil {
				users[name] = append(users[name], pw[h.dev]...)
				continue
			}
		}
		users[name] = append(users[name], fmt.Sprintf("%s (pid %d)", h.comm, h.pid))
	}

	names := slices.Sorted(maps.Values(devs))
	names = slices.Compact(names)
	active := false
	var tip strings.Builder
	for _, name := range names {
		who := users[name]
		slices.Sort(who)
		who = slices.Compact(who)
		if tip.Len() > 0 {
			tip.WriteString("\n\n")
		}
		fmt.Fprintf(&tip, "<b>%s</b>", html.EscapeString(name))
		if len(who) == 0 {
			tip.WriteString("\nidle")
		}
		for _, u := range who {
			active = true
			tip.WriteString("\n" + html.EscapeString(u))
		}
	}

	w.Text, w.ToolTip = iconCamera, tip.String()
	w.Class, w.Alt = "idle", "idle"
	if active {
		w.Class, w.Alt = "active", "active"
	}
	return w, viaPipewire
}

type pwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info *struct {
		State  string         `json:"state"`
		Props  map[string]any `json:"props"`
		Output int            `json:"output-node-id"`
		Input  int            `json:"input-node-id"`
	} `json:"info"`
}

func pipewireUsers(devs map[string]string) (map[string][]string, error) {
	out, err := exec.Command("pw-dump").Output()
	if err != nil {
		return nil, err
	}
	return parsePipewire(out, devs)
}

func parsePipewire(data []byte, devs map[string]string) (map[string][]string, error) {
	var objs []pwObject
	if err := json.Unmarshal(data, &objs); err != nil {
		return nil, err
	}
	byID := map[int]pwObject{}
	for _, o := range objs {
		if o.Info != nil {
			byID[o.ID] = o
		}
	}

	users := map[string][]string{}
	for _, cam := range byID {
		if cam.Type != "PipeWire:Interface:Node" || cam.Info.State != "running" ||
			prop(cam.Info.Props, "media.class") != "Video/Source" {
			continue
		}
		path := prop(cam.Info.Props, "api.v4l2.path")
		if _, ok := devs[path]; !ok {
			continue
		}
		var who []string
		for _, l := range byID {
			if l.Type != "PipeWire:Interface:Link" || l.Info.Output != cam.ID {
				continue
			}
			if n, ok := byID[l.Info.Input]; ok {
				who = append(who, describe(n, byID))
			}
		}
		if len(who) == 0 {
			who = []string{"PipeWire"}
		}
		users[path] = who
	}
	return users, nil
}

func describe(node pwObject, byID map[int]pwObject) string {
	np := node.Info.Props
	var cp map[string]any
	if id, err := strconv.Atoi(prop(np, "client.id")); err == nil {
		if c, ok := byID[id]; ok {
			cp = c.Info.Props
		}
	}
	name := cmp.Or(prop(cp, "application.name"), prop(np, "application.name"), prop(np, "node.name"), "?")
	bin, pid := prop(cp, "application.process.binary"), prop(cp, "application.process.id")
	if bin != "" && pid != "" {
		return fmt.Sprintf("%s (%s, pid %s)", name, bin, pid)
	}
	return name
}

func prop(p map[string]any, key string) string {
	switch v := p[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
