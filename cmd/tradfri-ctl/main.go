// tradfri-ctl is a control CLI for a DIRIGERA hub. Companion to
// waybar-tradfri (read-only daemon) — this one can toggle and set
// light state.
//
// Subcommands:
//
//	tradfri-ctl list                 # emit JSONL of all lights (id, name, on, color, label, pct)
//	tradfri-ctl toggle --id <id>     # flip isOn
//	tradfri-ctl set    --id <id> --on|--off
//	tradfri-ctl set    --id <id> --brightness 0-100
//	tradfri-ctl music                # emit JSONL of speaker favorites/playlists the hub can play
//	tradfri-ctl scenes               # emit JSONL of scenes (id, name)
//	tradfri-ctl set-music    --scene-id <id> --favorite <title>   # point a scene's speaker action at other content
//	tradfri-ctl rotate-music --scene-id <id> --prefix "Isak: "    # deterministic daily pick among matching favorites
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/polarn/waybar-modules/pkg/dirigera"
)

type globalFlags struct {
	host      string
	tokenPath string
}

func addGlobal(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.host, "host", "", "DIRIGERA hostname or IP (required)")
	fs.StringVar(&g.tokenPath, "token", "", "Path to access token (default $HOME/.config/waybar-tradfri/token)")
}

func mustClient(g globalFlags) *dirigera.Client {
	if g.host == "" {
		fatal("--host is required")
	}
	path := g.tokenPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("resolve home dir: %v", err)
		}
		path = filepath.Join(home, ".config", "waybar-tradfri", "token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("read token (%s): %v\n\nRun waybar-tradfri-auth first.", path, err)
	}
	token := strings.TrimSpace(string(data))
	return dirigera.NewClient(g.host, token)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "list":
		cmdList(os.Args[2:])
	case "toggle":
		cmdToggle(os.Args[2:])
	case "set":
		cmdSet(os.Args[2:])
	case "music":
		cmdMusic(os.Args[2:])
	case "scenes":
		cmdScenes(os.Args[2:])
	case "set-music":
		cmdSetMusic(os.Args[2:])
	case "rotate-music":
		cmdRotateMusic(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
	}
}

type listEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	On         bool   `json:"on"`
	Color      string `json:"color"`
	Label      string `json:"label"`
	Brightness int    `json:"brightness"`
}

func cmdList(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	c := mustClient(g)
	devices, err := c.Devices()
	if err != nil {
		fatal("list: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	for _, d := range devices {
		if d.Type != "light" {
			continue
		}
		a := d.Attributes
		entry := listEntry{
			ID:         d.ID,
			Name:       strings.TrimSpace(a.CustomName),
			On:         a.IsOn,
			Color:      a.Hex(),
			Label:      a.Label(),
			Brightness: a.LightLevel,
		}
		_ = enc.Encode(entry)
	}
}

func cmdToggle(args []string) {
	var g globalFlags
	var id string
	fs := flag.NewFlagSet("toggle", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.StringVar(&id, "id", "", "Device ID to toggle (required)")
	fs.Parse(args)

	if id == "" {
		fatal("--id is required")
	}
	c := mustClient(g)

	devices, err := c.Devices()
	if err != nil {
		fatal("read current state: %v", err)
	}
	var current *dirigera.Device
	for i := range devices {
		if devices[i].ID == id {
			current = &devices[i]
			break
		}
	}
	if current == nil {
		fatal("device %q not found", id)
	}
	next := !current.Attributes.IsOn
	if err := c.SetLightOn(id, next); err != nil {
		fatal("set: %v", err)
	}
	fmt.Printf("%s: %v\n", strings.TrimSpace(current.Attributes.CustomName), onOff(next))
}

func cmdSet(args []string) {
	var g globalFlags
	var id string
	var on, off bool
	var brightness int
	fs := flag.NewFlagSet("set", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.StringVar(&id, "id", "", "Device ID (required)")
	fs.BoolVar(&on, "on", false, "Turn light on")
	fs.BoolVar(&off, "off", false, "Turn light off")
	fs.IntVar(&brightness, "brightness", -1, "Set brightness 0-100 (omit to leave unchanged)")
	fs.Parse(args)

	if id == "" {
		fatal("--id is required")
	}
	if on && off {
		fatal("cannot use --on and --off together")
	}
	c := mustClient(g)

	if on || off {
		if err := c.SetLightOn(id, on); err != nil {
			fatal("set isOn: %v", err)
		}
	}
	if brightness >= 0 {
		if err := c.SetLightBrightness(id, brightness); err != nil {
			fatal("set brightness: %v", err)
		}
	}
}

type musicEntry struct {
	Kind  string `json:"kind"` // "favorite" | "playlist"
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
}

func cmdMusic(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("music", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	c := mustClient(g)
	m, err := c.Music()
	if err != nil {
		fatal("music: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	for _, f := range m.Favorites {
		_ = enc.Encode(musicEntry{Kind: "favorite", ID: f.ID, Title: strings.TrimSpace(f.Title), Type: f.Type})
	}
	for _, p := range m.Playlists {
		_ = enc.Encode(musicEntry{Kind: "playlist", ID: p.ID, Title: strings.TrimSpace(p.Title), Type: p.Type})
	}
}

func cmdScenes(args []string) {
	var g globalFlags
	fs := flag.NewFlagSet("scenes", flag.ExitOnError)
	addGlobal(fs, &g)
	fs.Parse(args)

	c := mustClient(g)
	scenes, err := c.Scenes()
	if err != nil {
		fatal("scenes: %v", err)
	}

	type sceneEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	enc := json.NewEncoder(os.Stdout)
	for _, s := range scenes {
		_ = enc.Encode(sceneEntry{ID: sceneID(s), Name: sceneName(s)})
	}
}

// sceneFlags is the scene selector shared by set-music and rotate-music.
// Prefer --scene-id: scene names are freely editable in the IKEA app and
// have already changed once, ids are stable.
type sceneFlags struct {
	name string
	id   string
}

func addSceneFlags(fs *flag.FlagSet, s *sceneFlags) {
	fs.StringVar(&s.name, "scene", "", "Scene name (as shown in the IKEA app)")
	fs.StringVar(&s.id, "scene-id", "", "Scene id (stable across renames; preferred)")
}

func cmdSetMusic(args []string) {
	var g globalFlags
	var sf sceneFlags
	var favorite, playlist string
	fs := flag.NewFlagSet("set-music", flag.ExitOnError)
	addGlobal(fs, &g)
	addSceneFlags(fs, &sf)
	fs.StringVar(&favorite, "favorite", "", "Favorite title to play")
	fs.StringVar(&playlist, "playlist", "", "Playlist title to play")
	fs.Parse(args)

	if (favorite == "") == (playlist == "") {
		fatal("exactly one of --favorite or --playlist is required")
	}
	c := mustClient(g)

	item := mustMusicItem(c, favorite, playlist)
	scene := mustScene(c, sf)
	setScenePlayItem(c, scene, item)
	fmt.Printf("scene %q: now plays %q\n", sceneName(scene), item.Title)
}

func cmdRotateMusic(args []string) {
	var g globalFlags
	var sf sceneFlags
	var prefix, exclude string
	var dryRun bool
	fs := flag.NewFlagSet("rotate-music", flag.ExitOnError)
	addGlobal(fs, &g)
	addSceneFlags(fs, &sf)
	fs.StringVar(&prefix, "prefix", "", "Favorite-title prefix defining the rotation pool (required)")
	fs.StringVar(&exclude, "exclude", "", "Comma-separated favorite titles to leave out of the pool")
	fs.BoolVar(&dryRun, "dry-run", false, "Print today's pick without updating the scene")
	fs.Parse(args)

	if prefix == "" {
		fatal("--prefix is required")
	}
	excluded := map[string]bool{}
	for _, t := range strings.Split(exclude, ",") {
		if t = strings.TrimSpace(t); t != "" {
			excluded[t] = true
		}
	}
	c := mustClient(g)

	m, err := c.Music()
	if err != nil {
		fatal("music: %v", err)
	}
	var pool []dirigera.MusicItem
	for _, f := range m.Favorites {
		title := strings.TrimSpace(f.Title)
		if strings.HasPrefix(title, prefix) && !excluded[title] {
			f.Title = title
			pool = append(pool, f)
		}
	}
	if len(pool) == 0 {
		fatal("no favorites match prefix %q (add them in the Sonos app)", prefix)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Title < pool[j].Title })

	// Deterministic and stateless: same UTC day -> same pick, so re-runs
	// are idempotent and no rotation state needs storing anywhere.
	day := int(time.Now().UTC().Unix() / 86400)
	pick := pool[day%len(pool)]

	titles := make([]string, len(pool))
	for i, p := range pool {
		titles[i] = p.Title
	}
	fmt.Printf("pool (%d): %s\n", len(pool), strings.Join(titles, " | "))
	fmt.Printf("today (day %d %% %d = %d): %q\n", day, len(pool), day%len(pool), pick.Title)
	if dryRun {
		return
	}

	scene := mustScene(c, sf)
	setScenePlayItem(c, scene, pick)
	fmt.Printf("scene %q: now plays %q\n", sceneName(scene), pick.Title)
}

// mustMusicItem resolves a favorite or playlist by (trimmed,
// case-insensitive) title. Resolution happens at call time because the
// hub's music ids encode list positions, not stable identities.
func mustMusicItem(c *dirigera.Client, favorite, playlist string) dirigera.MusicItem {
	m, err := c.Music()
	if err != nil {
		fatal("music: %v", err)
	}
	list, want, kind := m.Favorites, favorite, "favorite"
	if playlist != "" {
		list, want, kind = m.Playlists, playlist, "playlist"
	}
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item.Title), strings.TrimSpace(want)) {
			item.Title = strings.TrimSpace(item.Title)
			return item
		}
	}
	fatal("%s %q not found on the hub (check `tradfri-ctl music`)", kind, want)
	panic("unreachable")
}

func mustScene(c *dirigera.Client, sf sceneFlags) map[string]any {
	if (sf.name == "") == (sf.id == "") {
		fatal("exactly one of --scene or --scene-id is required")
	}
	scenes, err := c.Scenes()
	if err != nil {
		fatal("scenes: %v", err)
	}
	for _, s := range scenes {
		if sf.id != "" && sceneID(s) == sf.id {
			return s
		}
		if sf.name != "" && sceneName(s) == sf.name {
			return s
		}
	}
	fatal("scene %q not found (check `tradfri-ctl scenes`)", sf.name+sf.id)
	panic("unreachable")
}

// setScenePlayItem rewrites every speaker action in the scene that already
// carries a playbackAudio block to play the given item, PUTs the scene
// back, and waits for the hub to apply it (PUT is async, 202).
func setScenePlayItem(c *dirigera.Client, scene map[string]any, item dirigera.MusicItem) {
	actions, _ := scene["actions"].([]any)
	touched := 0
	for _, a := range actions {
		action, _ := a.(map[string]any)
		attrs, _ := action["attributes"].(map[string]any)
		if attrs == nil {
			continue
		}
		if _, ok := attrs["playbackAudio"]; !ok {
			continue
		}
		attrs["playbackAudio"] = map[string]any{
			"playItem": map[string]any{"id": item.ID, "title": item.Title},
		}
		touched++
	}
	if touched == 0 {
		fatal("scene %q has no speaker (playbackAudio) action to update", sceneName(scene))
	}

	id := sceneID(scene)
	if err := c.PutScene(id, scene); err != nil {
		fatal("update scene: %v", err)
	}
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		got, err := c.Scene(id)
		if err != nil {
			continue
		}
		if scenePlaysItem(got, item.ID) {
			return
		}
	}
	fatal("scene %q: hub accepted the update but it never applied", sceneName(scene))
}

func scenePlaysItem(scene map[string]any, itemID string) bool {
	actions, _ := scene["actions"].([]any)
	for _, a := range actions {
		action, _ := a.(map[string]any)
		attrs, _ := action["attributes"].(map[string]any)
		pa, _ := attrs["playbackAudio"].(map[string]any)
		pi, _ := pa["playItem"].(map[string]any)
		if id, _ := pi["id"].(string); id == itemID {
			return true
		}
	}
	return false
}

func sceneID(scene map[string]any) string {
	id, _ := scene["id"].(string)
	return id
}

func sceneName(scene map[string]any) string {
	info, _ := scene["info"].(map[string]any)
	name, _ := info["name"].(string)
	return name
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: tradfri-ctl <subcommand> [flags]

Subcommands:
  list                 Emit one JSON per light to stdout
  toggle --id <id>     Flip a light's on/off state
  set    --id <id> --on|--off [--brightness 0-100]
  music                Emit one JSON per speaker favorite/playlist to stdout
  scenes               Emit one JSON per scene (id, name) to stdout
  set-music    --scene-id <id>|--scene <name> --favorite <title>|--playlist <title>
                       Point a scene's speaker action at other content
  rotate-music --scene-id <id>|--scene <name> --prefix <p> [--exclude <t,t>] [--dry-run]
                       Deterministic daily pick among favorites matching prefix

Global flags (all subcommands):
  --host <host>        DIRIGERA hostname/IP (required)
  --token <path>       Token file (default $HOME/.config/waybar-tradfri/token)`)
	os.Exit(2)
}
