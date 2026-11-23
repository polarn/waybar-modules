# Agents Guide for waybar-modules

## Project overview

Waybar custom modules in Go, plus the DIRIGERA CLI that shares a package with one of them. Two shapes of program, and the conventions differ:

| Shape | What it does |
|---|---|
| **Bar module** | Long-running daemon. Polls something, prints one JSON line to stdout per tick, forever. Consumed by waybar's `custom` module type. |
| **CLI** | One-shot. Does a thing and exits. |

A subset of bar modules also support a `--swiftbar` flag for use as macOS [SwiftBar](https://swiftbar.app) plugins. In SwiftBar mode the binary is one-shot — emits a single SwiftBar-format frame on stdout and exits — because SwiftBar re-runs polled plugins on the interval encoded in the plugin's filename (e.g. `wiim.5s.sh` → every 5 seconds).

The Bambu Lab and Volvo tooling (a CLI with a `waybar` subcommand and a Prometheus exporter each) lived here until October 2026 and now have their own repos; see "What moved out" at the end.

## Repository structure

Bar modules (daemon, prints waybar JSON on an interval):

- `cmd/waybar-gitlab-mr/` - Count of GitLab merge requests awaiting review
- `cmd/waybar-wiim-nowplaying/` - Now-playing info from a WiiM device (amp/mini/pro), with volume control. SwiftBar-enabled.
- `cmd/waybar-cpu-temp/` - CPU temperature, tooltip lists every CPU hwmon sensor
- `cmd/waybar-gpu-temp/` - GPU temperature; picks the card with the most sensors (the discrete one), tooltip lists loaded llama-swap models
- `cmd/waybar-tradfri/` - State of a chosen set of DIRIGERA lights
- `cmd/waybar-inhibitors/` - How many clients hold a screensaver idle-inhibitor, and which
- `cmd/waybar-allsvenskan/` - Allsvenskan fixtures and live scores for a configured team
- `cmd/waybar-batteries/` - Aggregated battery level across Bluetooth (UPower) and SteelSeries (headsetcontrol) peripherals
- `cmd/waybar-camera/` - Whether a webcam is in use, and by whom: direct `/dev/video*` holders plus the clients linked to the camera's PipeWire node. Event-driven (inotify on the device nodes), not polled; Linux-only (`//go:build linux`)

CLIs (one-shot):

- `cmd/tradfri-ctl/` - DIRIGERA control CLI (toggle/set lights, rotate Sonos scenes). Containerised for the flux `tradfri` CronJob.
- `cmd/waybar-tradfri-auth/` - One-time DIRIGERA pairing; writes the token the other two read. Named `waybar-*` for historical reasons — it is a helper, not a bar module.

Packages:

- `pkg/dirigera/` - DIRIGERA REST client: pairing, devices, scenes
- `pkg/hwmon/` - Shared hwmon sensor discovery and reading
- `pkg/waybar/` - Shared `Waybar` struct with JSON output and `Print()` method

## Where it is wired up

Every consumer lives in chezmoi (`~/.local/share/chezmoi`). Its `CLAUDE.md` has a table mapping each pill to its module file and binary.

- `dot_config/waybar/modules/<name>.jsonc`: each pill's `exec` line, flags, clicks and `restart-interval`. `config.jsonc` places it on a bar.
- `dot_config/waybar/style.css`: colours keyed `#custom-<name>.<class>`, so a new `class` needs a rule there.
- `run_onchange_install-waybar-modules.sh`: the `go install …@latest` list, which also installs `bambu-ctl` and `volvo-ctl` from their own repos. For macOS, `run_onchange_install-swiftbar-modules.sh` and `dot_config/swiftbar/plugins/`.
- `dot_config/systemd/user/waybar-inhibitors.service`: `waybar-inhibitors` runs as a user unit, and the pill reads its output file.

## Building

- `make build` - Compiles the 11 desktop binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances. Waybar respawns only the modules whose config sets `restart-interval`, so run `systemctl --user restart waybar` afterwards
- `make build-swiftbar` / `make install-swiftbar` - Same flow but only for the SwiftBar-enabled subset (currently `waybar-wiim-nowplaying`). Used on macOS where the Linux-only modules (hwmon temps, tradfri, etc.) wouldn't compile or be useful.
- `make clean` - Removes `./build/`

Note `make install` does a blind `pkill -x` on every binary name it installs.

## Conventions

Everywhere:

- **No external dependencies beyond what's in `go.mod`**, which is deliberately tiny: `github.com/godbus/dbus/v5` and nothing else direct. This is a real constraint, not an aspiration — every HTTP API here is talked to with `net/http` and `encoding/json` by hand, and the bambu repo hand-rolls MQTT for the same reason.
- Flags use the standard `flag` package: `--host`, `--interval`, etc.
- Keep things simple and self-contained; avoid unnecessary abstractions.
- One file per command is the norm and the right default.

Bar modules specifically:

- Polling loop: fetch data, print one JSON line, sleep, repeat.
- Use `pkg/waybar.Waybar` for JSON output (text, tooltip, class, alt). `waybar-allsvenskan` and `waybar-batteries` predate it and marshal their own structs — fine, but new ones should use the shared type.
- Opting into SwiftBar means adding a `--swiftbar` bool flag; when set, the loop is short-circuited to one iteration that prints a SwiftBar frame and the process exits (no JSON, no sleep).

CLIs specifically:

- Subcommand dispatch on `os.Args[1]` with a `flag.NewFlagSet` per subcommand; see `tradfri-ctl`.
- A CLI that feeds a pill does it through a `waybar` subcommand that **always exits 0 and prints exactly one JSON line**, even on failure — a bad exit or stray output blanks or breaks the pill. Errors go to stderr (waybar forwards them to the journal) and surface as a pill `class` instead. `bambu-ctl` and `volvo-ctl` in their own repos are the references.
- Long-lived credentials live under `~/.config/`, mode 0600, and are never committed.

## SwiftBar plugin format

SwiftBar plugins emit plain text on stdout: the first line is the menu bar title, then `---` introduces the dropdown. Inline SF Symbols use the `:symbol.name:` syntax (e.g. `:music.note:`, `:arrow.triangle.pull:`). Dropdown rows can have `| href=URL` to open a link, or `| shell=/path param1=… param2=…` to invoke a command — modules use the latter to wire menu actions back to themselves (e.g. WiiM Volume Up/Down re-invokes the binary with `--volume-up` via `os.Executable()` so the path is always self-consistent).

Plugin filename intervals (`5s`, `5m`, etc.) are interpreted by SwiftBar — the binary itself doesn't know or care about the interval in `--swiftbar` mode.

Three things SwiftBar's parser (`MenuLineParameters.swift`) does that a module must allow for:

- **A line splits at its first `|`**, and everything after it is parsed as parameters. Text from outside (a track name) must have `|` and newlines replaced before it is printed, or it can cut the row short and add its own `shell=`.
- **`shell=` runs in a Terminal window unless the row says `terminal=false`.**
- **A click action that names the binary bypasses the plugin script**, and with it the `PATH` the script sets. Point `shell=` at `$SWIFTBAR_PLUGIN_PATH` (the plugin script, which passes `"$@"` on to the binary) instead.

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

## Adding a new command

Pick the shape first — the steps differ, and following the bar-module path for a CLI or a service produces the wrong thing.

**A bar module** (reference: `cmd/waybar-cpu-temp`)

1. Create `cmd/<name>/main.go` with an `--interval` flag.
2. Use `pkg/waybar.New()`, set `.Text`, `.ToolTip`, `.Class`, `.Alt`.
3. Call `.Print()` each iteration to emit JSON, then sleep.
4. Add the build line to `Makefile`.
5. Add the module config in chezmoi under `dot_config/waybar/modules/`, with `restart-interval` so waybar respawns it after `make install`, and place it in `config.jsonc`.
6. Add the `go install …@latest` line to chezmoi's `run_onchange_install-waybar-modules.sh`.

**A CLI** (reference: `cmd/tradfri-ctl`, or `bambu-ctl` in `polarn/bambu` if it needs a pill)

1. Create `cmd/<name>/main.go` with subcommand dispatch and a per-subcommand `FlagSet`.
2. If it feeds a pill, add a `waybar` subcommand that prints one JSON line and always exits 0.
3. Add the build line to `Makefile`.
4. Add the `go install …@latest` line to chezmoi's `run_onchange_install-waybar-modules.sh`.

**A service** does not go here. A new cloud domain with a CLI and an exporter gets its own repo shaped like `polarn/bambu`: `cmd/<x>-ctl`, `cmd/<x>-exporter`, `pkg/<x>`, and a copy of `pkg/waybar` for the pill subcommand.

Whichever shape: a field is emitted only when the upstream actually reported it. Publishing a zero for an absent value is how the bambu exporter silently graphed a 0 °C chamber for months.

### Adding SwiftBar support to an existing module

1. Add a `--swiftbar` bool flag.
2. After computing the `Waybar` struct (or whatever data the loop normally prints), branch: if swiftbar, call a `printSwiftBar*` helper that emits the SwiftBar frame and `return` from `main` (one-shot; no sleep).
3. The helper writes plaintext lines: title (with optional `:sf.symbol:`), then `---`, then dropdown rows.
4. Add the binary name to `SWIFTBAR_BINS` in the Makefile so `make install-swiftbar` picks it up.
5. Add a corresponding plugin script under `~/.config/swiftbar/plugins/<name>.<interval>.sh` (tracked in chezmoi under the `darwin` guard) that just `exec`s the binary with `--swiftbar` and any host-specific flags.

## What moved out, and how

On 2026-10-31 the Bambu Lab and Volvo domains were split into `polarn/bambu` (`bambu-ctl`, `bambu-exporter`, `pkg/bambu`) and `polarn/volvo` (`volvo-ctl`, `volvo-exporter`, `pkg/volvo`), each with its history filtered out of this one. The cut is per domain, not "waybar vs not": a CLI and its exporter share the client package, so they travel together. tradfri stayed because two of its three commands are bar modules and the whole domain is 1.3k lines.

On 2026-11-23 `waybar-github-pr` moved to `polarn/ghpill`. It starts there from a single commit and is installed as a package (`ghpill-bin` on the AUR, a Homebrew cask) instead of with `go install`.

How, for the next domain that outgrows this repo:

- `git clone --no-local` a fresh copy, then `git filter-repo --path cmd/<x>-ctl --path cmd/<x>-exporter --path pkg/<x> --path pkg/waybar --path LICENSE`. It refuses a clone that is not fresh and removes `origin`; add the new remote afterwards. Rewrite the module path in `go.mod` and every import, and pin `go.mod` to the same `go` line and dependency versions as here: a bare `go mod tidy` takes the newest of everything, including a `go` line ahead of the `golang:1.25` the Dockerfiles build with.
- Each repo carries its own copy of `pkg/waybar` (30 lines) rather than importing this module: a cross-module dependency on `@latest` would multiply the stale-proxy footgun documented in chezmoi's CLAUDE.md.
- The new repos are public. chezmoi installs with a plain `go install …@latest` through proxy.golang.org, which cannot read private repos, and every commit was already public here, so private would have cost `GOPRIVATE` plus git credentials on every host and a cross-repo token in the image build, for nothing.
- Their exporter images are built in those repos (root `Dockerfile`, `.github/workflows/build.yml` with `paths:` filters, which GitHub evaluates over the whole push), not in `polarn/container-images`; flux is repinned from the digest in the run summary. A ghcr package created by one repo refuses another repo's `GITHUB_TOKEN` (`denied: permission_denied: write_package`) until the package's *Manage Actions access* grants it, which has no API. The package stays linked to the repo that created it; that is cosmetic and cannot be changed.
- `tradfri-ctl` is the one image still built from this repo, by `polarn/container-images` cloning a pinned `SRC_REF`: this repo has no image workflow and that CLI changes about once a year. If that stops being true, copy the workflow shape from `polarn/bambu`.

**`docs/volvo-callback/` stays here even though the Volvo code left.** `https://polarn.github.io/waybar-modules/volvo-callback/` is the registered OAuth redirect URI of a published Volvo app, fixed at publication, and GitHub Pages does not redirect after a repo rename. Renaming this repo, or moving that directory, breaks `volvo-ctl auth` and the exporter's login until a new Volvo app is published.

Nothing imports this module as a library and it has never been tagged — chezmoi uses `@latest` and `polarn/container-images` pins a raw commit SHA for `tradfri-ctl`.

## Repo settings

`polarn` repos are squash-merge only with delete-branch-on-merge, and the public ones carry a `linear-history` ruleset on `main` (`required_linear_history` plus `non_fast_forward`, no bypass actors); GitHub Free refuses rulesets on private repos, so there the merge button is the only guard. A squash leaves the branch tip unreachable from `main`, so `git branch --merged` never lists a merged branch: delete it with `-D` once the PR is in.
