# Agents Guide for waybar-modules

## Project overview

Personal waybar custom modules written in Go. Each module is a long-running process that outputs JSON to stdout on a polling interval, consumed by waybar's `custom` module type.

A subset of modules also support a `--swiftbar` flag for use as macOS [SwiftBar](https://swiftbar.app) plugins. In SwiftBar mode the binary is one-shot — emits a single SwiftBar-format frame on stdout and exits — because SwiftBar re-runs polled plugins on the interval encoded in the plugin's filename (e.g. `wiim.5s.sh` → every 5 seconds).

## Repository structure

- `cmd/waybar-gitlab-mr/` - Displays count of GitLab merge requests awaiting review
- `cmd/waybar-github-pr/` - Displays count of approved GitHub PRs ready to merge, with rofi-based click-to-open. SwiftBar-enabled.
- `cmd/waybar-wiim-nowplaying/` - Displays now-playing info from a WiiM device (amp/mini/pro). SwiftBar-enabled.
- `cmd/bambu-ctl/` - Bambu Lab cloud CLI (`login`/`status`/`waybar`); feeds the custom/p2s printer pill
- `pkg/bambu/` - Bambu cloud client: HTTP login flow, session cache, and a hand-rolled minimal MQTT 3.1.1 fetch (no client library — the exchange is one subscribe + one publish, QoS 0)
- `pkg/waybar/` - Shared `Waybar` struct with JSON output and `Print()` method

## Building

- `make build` - Compiles all binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances so waybar restarts them
- `make build-swiftbar` / `make install-swiftbar` - Same flow but only for the SwiftBar-enabled subset (currently `waybar-github-pr`, `waybar-wiim-nowplaying`). Used on macOS where the Linux-only modules (hwmon temps, tradfri, etc.) wouldn't compile or be useful.
- `make clean` - Removes `./build/`

## Conventions

- Each module lives in its own `cmd/<name>/main.go` as a single file
- All modules use `pkg/waybar.Waybar` for JSON output (text, tooltip, class, alt)
- Modules are polling loops: fetch data, print one JSON line, sleep, repeat
- Flags use the standard `flag` package: `--host`, `--interval`, etc.
- Modules that opt into SwiftBar add a `--swiftbar` bool flag; when set, the loop is short-circuited to one iteration that prints a SwiftBar frame and the process exits (no JSON, no sleep)
- No external dependencies beyond what's in `go.mod` (except `go-gitlab` for the MR module)
- Keep modules simple and self-contained; avoid unnecessary abstractions

## SwiftBar plugin format

SwiftBar plugins emit plain text on stdout: the first line is the menu bar title, then `---` introduces the dropdown. Inline SF Symbols use the `:symbol.name:` syntax (e.g. `:music.note:`, `:arrow.triangle.pull:`). Dropdown rows can have `| href=URL` to open a link, or `| shell=/path param1=… param2=…` to invoke a command — modules use the latter to wire menu actions back to themselves (e.g. WiiM Volume Up/Down re-invokes the binary with `--volume-up` via `os.Executable()` so the path is always self-consistent).

Plugin filename intervals (`5s`, `5m`, etc.) are interpreted by SwiftBar — the binary itself doesn't know or care about the interval in `--swiftbar` mode.

Streamable plugins (`<swiftbar.type>streamable</swiftbar.type>`) were tried first but cause NSStatusItem visibility issues: the menu bar item gets created with empty content during the leading-separator `~~~` priming step, and macOS persists `VisibleCC=0` for it. Polled mode (the current approach) sidesteps that entirely.

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

### SwiftBar mode

`--swiftbar` emits one SwiftBar frame and exits. Title is the now-playing text plus an SF Symbol picked from the player class (`:music.note:` / `:speaker.slash.fill:` / `:stop.fill:` / `:cable.connector:` / `:wave.3.right:`). Dropdown rows: tooltip lines as info entries, then Volume Up / Volume Down actions wired back to this binary via `os.Executable()` + `param1=--host param2=<host> param3=--volume-(up|down)`. Scroll-for-volume isn't a thing on SwiftBar so volume is dropdown-only there.

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

### SwiftBar mode

`--swiftbar` implies `--notify=false` (no `notify-send` on macOS) and skips the `$XDG_RUNTIME_DIR` cache write (the `--open` flow isn't used). Title is `approved·total :arrow.triangle.pull:`, with a `:bell.badge:` variant when there are unread notifications. Dropdown is a single `Open PRs | href=https://github.com/pulls` row — no per-PR list (intentionally simple; can be extended later by adding a list subcommand).

## bambu-ctl module details

### Why cloud, not local

Bambu's P2S-generation printers only serve local MQTT when LAN-only mode + Developer Mode are enabled (which kills the Handy app / MakerWorld cloud features). `bambu-ctl` therefore reads state from Bambu's cloud broker (`us.mqtt.bambulab.com:8883`) with the account's access token — the same channel Handy uses — so the printer stays in Cloud mode.

### Auth flow

`bambu-ctl login`: POST `/v1/user-service/user/login` on `api.bambulab.com` with OrcaSlicer-mimicking headers (Bambu's risk control rejects unknown clients). Response may demand an emailed code (`loginType: verifyCode` → `/user/sendemail/code`) or 2FA (`loginType: tfa` → `/user/tfa/login`, token in the `token` cookie). The JWT's `username` claim (`u_<uid>`) is the MQTT username; the token itself is the password. Session cached in `~/.config/bambu-cloud.json` (0600), expires ~3 months → the pill's `reauth` class.

Google/Apple SSO accounts have no API-usable password and the API login rejects them entirely; `login --token` covers those by pasting the `token` cookie from a browser session on makerworld.com / bambulab.com (it's the same cloud access token). Alternative: set a password on the account via Handy/web, then normal login works.

### Status fetch

Subscribe `device/<serial>/report`, publish `{"pushing":{"command":"pushall"}}` to `device/<serial>/request`, take the first report containing `mc_percent` (partial pushes lack it). The printer rate-limits pushall (~1/min) — the waybar module polls at 120 s for headroom. Report fields mix numbers and numeric strings across firmwares; `pkg/bambu.Num` tolerates both.

Chamber temperature moved between generations: P2S-gen firmware has no flat `print.chamber_temper` (X1-era) and reports it at `print.device.ctc.info.temp` instead. `Report.ChamberTemp()` prefers the ctc path, falls back to the flat field, and returns false when neither exists so the tooltip omits the line rather than showing a fake 0 °C — don't "simplify" either branch away. Sibling `device.*` temps pack `(target << 16) | current` (`device.bed.info.temp` 0x4b004b = 75/75), hence the low-16-bit mask; the chamber has no target today so it reads as a bare int.

### Pill states

`printing` (green, `% + min left`) / `paused` / `failed` / `idle` (nozzle °C) / `offline` (dim, printer off) / `setup`+`reauth` (mauve, login needed) / `error`. CSS classes live in the chezmoi waybar style.css.

### Control commands

`pause`/`resume`/`stop`/`speed`/`light` publish to `device/<serial>/request` (payloads per OpenBambuAPI mqtt.md: `print.command` = pause/resume/stop/print_speed with param "1"-"4"; `system.command` = ledctrl for chamber_light) and wait for the echoed `result` on the report topic — `SendCommand` in pkg/bambu matches on section+command, treats a non-"success" result as an error, and returns `ErrNoAck` on timeout (printer off). `stop` prompts for confirmation unless `--yes`. AMS drying has no documented MQTT command (start it from the touchscreen/Handy).

## bambu-exporter module details

### One-shot fetch vs held-open subscription

`FetchReport` asks for a `pushall` per call, and the printer rate-limits those to ~1/min — that is the only reason the waybar module polls at 120 s. `Subscribe` (pkg/bambu/stream.go) holds one connection open instead and consumes the unsolicited `push_status` deltas, which arrive every 1–2 s. Anything that needs data more often than once a minute must go through `Subscribe`, not `FetchReport`.

Those deltas carry only changed fields, so `State` (pkg/bambu/state.go) merges each message over the accumulated object; keeping just the newest message would make most fields absent, which renders as zero. The merge runs on decoded JSON rather than `Report` values so unmodelled fields survive — the same blind spot that hid `chamber_temper` moving to `device.ctc`. Nested objects merge key-by-key; arrays replace wholesale, because the printer resends a whole list (the AMS trays) when any of it changes.

Connection details that are easy to get wrong: `dial` leaves a whole-connection deadline set for its short exchange, so a persistent session must clear it or the first keepalive write fails; CONNECT advertises a 30 s keepalive, so `Subscribe` sends PINGREQ every 20 s with a rolling read deadline (90 s of silence means the session went deaf while the socket still looks open); and the reconnect backoff resets only after a connection survives 5 minutes.

### Metrics

Prometheus text is hand-rolled in `cmd/bambu-exporter/metrics.go` — client_golang would pull protobuf and a half-dozen transitive modules into a repo whose `go.sum` is eight lines. Validate changes with `podman run --rm -i --entrypoint promtool docker.io/prom/prometheus:latest check metrics < metrics.txt`; it catches reserved suffixes (`_total` is counters-only — hence `bambulab_print_layers`) and non-base units (hence nozzle diameter living on `bambulab_nozzle_info` as a label rather than being named in millimetres).

Naming follows `bambulab_*` with base-unit suffixes and a `printer_name` label, close to Grafana dashboard 25033 so it is a usable starting point — but not identical, see the layers note above. **A metric is emitted only when the printer reported the field**: absent must not become a confident zero. Keep the printer serial out of labels; it is credential-adjacent and would end up in dashboard screenshots.

### Token expiry

Renewal is interactive, so an unattended deployment breaks about quarterly. `bambulab_cloud_auth_ok` goes to 0 when the cloud rejects the token and the process keeps serving rather than crash-looping (a crash loop would hide the reason, and retrying in-process cannot help — the token is baked into the environment until the pod restarts). `TokenExpiry()` reads the JWT `exp` claim, but **not every token is a JWT**: the emailed-code login flow mints opaque ones, and this printer's account has one, so `bambulab_cloud_token_age_seconds` is the portable early warning.

## Adding a new module

1. Create `cmd/<name>/main.go`
2. Use `pkg/waybar.New()` and set `.Text`, `.ToolTip`, `.Class`, `.Alt`
3. Call `.Print()` each iteration to emit JSON
4. Add the build line to `Makefile`

### Adding SwiftBar support to an existing module

1. Add a `--swiftbar` bool flag.
2. After computing the `Waybar` struct (or whatever data the loop normally prints), branch: if swiftbar, call a `printSwiftBar*` helper that emits the SwiftBar frame and `return` from `main` (one-shot; no sleep).
3. The helper writes plaintext lines: title (with optional `:sf.symbol:`), then `---`, then dropdown rows.
4. Add the binary name to `SWIFTBAR_BINS` in the Makefile so `make install-swiftbar` picks it up.
5. Add a corresponding plugin script under `~/.config/swiftbar/plugins/<name>.<interval>.sh` (tracked in chezmoi under the `darwin` guard) that just `exec`s the binary with `--swiftbar` and any host-specific flags.
