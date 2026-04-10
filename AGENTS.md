# Agents Guide for waybar-modules

## Project overview

Personal waybar custom modules written in Go. Each module is a long-running process that outputs JSON to stdout on a polling interval, consumed by waybar's `custom` module type.

## Repository structure

- `cmd/waybar-gitlab-mr/` - Displays count of GitLab merge requests awaiting review
- `cmd/waybar-github-pr/` - Displays count of approved GitHub PRs ready to merge, with rofi-based click-to-open
- `cmd/waybar-wiim-nowplaying/` - Displays now-playing info from a WiiM device (amp/mini/pro)
- `pkg/waybar/` - Shared `Waybar` struct with JSON output and `Print()` method

## Building

- `make build` - Compiles all binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances so waybar restarts them
- `make clean` - Removes `./build/`

## Conventions

- Each module lives in its own `cmd/<name>/main.go` as a single file
- All modules use `pkg/waybar.Waybar` for JSON output (text, tooltip, class, alt)
- Modules are polling loops: fetch data, print one JSON line, sleep, repeat
- Flags use the standard `flag` package: `--host`, `--interval`, etc.
- No external dependencies beyond what's in `go.mod` (except `go-gitlab` for the MR module)
- Keep modules simple and self-contained; avoid unnecessary abstractions

## WiiM now-playing module details

### WiiM API

The module talks to the WiiM device's LinkPlay HTTP API at `https://<host>/httpapi.asp?command=<cmd>`. TLS verification is disabled (self-signed cert). Key commands:

- `getPlayerStatus` - Returns playback state (`status`), source (`mode`), volume (`vol`), and hex-encoded title/artist/album
- `getMetaInfo` - Returns plain-text metadata (preferred over hex-decoded getPlayerStatus)
- `setPlayerCmd:vol:<0-100>` - Sets volume

### Mode values

| Mode | Source |
|------|--------|
| 1 | AirPlay |
| 2 | DLNA |
| 10 | Network |
| 31 | Spotify |
| 32 | Tidal |
| 40 | Line-In |
| 41 | Bluetooth |
| 43 | Optical |

### Metadata resolution strategy (layered)

1. **Always fetch `getPlayerStatus`** for playback state, mode, and volume
2. **`getMetaInfo`** as primary metadata source (plain text, no hex decoding)
3. **TuneIn radio detection** - If title is a `opml.radiotime.com/Tune.ashx?id=sXXXXX` URL, resolve station name via TuneIn Describe API. Results are cached in-memory by station ID.
4. **Hex-decoded `getPlayerStatus`** fields as fallback if `getMetaInfo` returns all "unknow"
5. **Physical inputs** (Optical, Line-In, Bluetooth) try local audio detection:
   - First: MPRIS via `playerctl` (catches Firefox, Spotify, VLC, etc.)
   - Then: PipeWire via `pw-dump` (catches Wine/Proton games and other non-MPRIS apps)
   - Fallback: just the source name (e.g. "Optical")

### Volume control

The `--volume-up` and `--volume-down` flags perform a one-shot volume adjustment and exit. Used by waybar's `on-scroll-up` / `on-scroll-down`. Step size configurable with `--volume-step` (default 5).

### Junk value filtering

The `isUseful()` helper rejects empty strings, URLs (`http://`/`https://`), and `"unknow"`/`"unknown"` values that the WiiM API frequently returns.

## GitHub PR module details

### Data source

Uses the `gh` CLI (`gh search prs`) rather than a Go library — no token env var needed, relies on existing `gh auth` session.

### Query

`gh search prs --review=approved --state=open --author=@me` — finds all open PRs authored by the current user that have at least one approved review.

### Click-to-open (`--open` flag)

The polling loop writes the current PR list to `$XDG_RUNTIME_DIR/waybar-github-prs.json` as a cache. The `--open` flag reads this cache and:

- **0 PRs**: does nothing
- **1 PR**: opens directly via `xdg-open`
- **Multiple PRs**: presents a rofi dmenu for selection, then opens the chosen PR

Waybar config wires this up via `"on-click": "waybar-github-pr --open"`.

## Adding a new module

1. Create `cmd/<name>/main.go`
2. Use `pkg/waybar.New()` and set `.Text`, `.ToolTip`, `.Class`, `.Alt`
3. Call `.Print()` each iteration to emit JSON
4. Add the build line to `Makefile`
