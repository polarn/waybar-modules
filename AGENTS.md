# Agents Guide for waybar-modules

## Project overview

Personal homelab tools in Go. It began as waybar custom modules and still mostly is, but it now holds three different shapes of program. Knowing which shape you are looking at matters, because the conventions differ:

| Shape | What it does |
|---|---|
| **Bar module** | Long-running daemon. Polls something, prints one JSON line to stdout per tick, forever. Consumed by waybar's `custom` module type. |
| **CLI** | One-shot. Does a thing and exits. Some carry a `waybar` subcommand that prints a single pill line — waybar runs those with `"interval"`, *not* as a daemon. |
| **Service** | Long-running, serves HTTP, emits no waybar JSON. Currently just `bambu-exporter`, which is deployed to Kubernetes. |

A subset of bar modules also support a `--swiftbar` flag for use as macOS [SwiftBar](https://swiftbar.app) plugins. In SwiftBar mode the binary is one-shot — emits a single SwiftBar-format frame on stdout and exits — because SwiftBar re-runs polled plugins on the interval encoded in the plugin's filename (e.g. `wiim.5s.sh` → every 5 seconds).

The name is now narrower than the contents; see "Why it is still called waybar-modules" at the end.

## Repository structure

Bar modules (daemon, prints waybar JSON on an interval):

- `cmd/waybar-gitlab-mr/` - Count of GitLab merge requests awaiting review
- `cmd/waybar-github-pr/` - Count of approved GitHub PRs ready to merge, with a fuzzel picker on click. SwiftBar-enabled.
- `cmd/waybar-wiim-nowplaying/` - Now-playing info from a WiiM device (amp/mini/pro), with volume control. SwiftBar-enabled.
- `cmd/waybar-cpu-temp/` - CPU temperature, tooltip lists every CPU hwmon sensor
- `cmd/waybar-gpu-temp/` - GPU temperature; picks the card with the most sensors (the discrete one), tooltip lists loaded llama-swap models
- `cmd/waybar-tradfri/` - State of a chosen set of DIRIGERA lights
- `cmd/waybar-inhibitors/` - How many clients hold a screensaver idle-inhibitor, and which
- `cmd/waybar-allsvenskan/` - Allsvenskan fixtures and live scores for a configured team
- `cmd/waybar-batteries/` - Aggregated battery level across Bluetooth (UPower) and SteelSeries (headsetcontrol) peripherals

CLIs (one-shot):

- `cmd/bambu-ctl/` - Bambu Lab cloud CLI (`login`/`status`/`waybar`/`pause`/`resume`/`toggle`/`stop`/`speed`/`light`); feeds the custom/p2s printer pill
- `cmd/volvo-ctl/` - Volvo cloud CLI (`auth`/`vehicles`/`status`/`location`/`climate`/`waybar`); feeds the custom/volvo pill
- `cmd/tradfri-ctl/` - DIRIGERA control CLI (toggle/set lights, rotate Sonos scenes). Containerised for the flux `tradfri` CronJob.
- `cmd/waybar-tradfri-auth/` - One-time DIRIGERA pairing; writes the token the other two read. Named `waybar-*` for historical reasons — it is a helper, not a bar module.

Service:

- `cmd/bambu-exporter/` - Holds one cloud MQTT subscription open; serves Prometheus metrics, a JSON state endpoint and a status page. Deployed via the flux `bambu-exporter` app.

Packages:

- `pkg/bambu/` - Bambu cloud client: HTTP login flow, session cache, hand-rolled minimal MQTT 3.1.1 (no client library), plus the persistent subscription and report accumulator the exporter needs
- `pkg/volvo/` - Volvo cloud client: OAuth with PKCE + rotating refresh token, Energy/Connected Vehicle/Location APIs
- `pkg/dirigera/` - DIRIGERA REST client: pairing, devices, scenes
- `pkg/hwmon/` - Shared hwmon sensor discovery and reading
- `pkg/waybar/` - Shared `Waybar` struct with JSON output and `Print()` method

## Building

- `make build` - Compiles the 13 desktop binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances so waybar restarts them
- `make build-swiftbar` / `make install-swiftbar` - Same flow but only for the SwiftBar-enabled subset (currently `waybar-github-pr`, `waybar-wiim-nowplaying`). Used on macOS where the Linux-only modules (hwmon temps, tradfri, etc.) wouldn't compile or be useful.
- `make clean` - Removes `./build/`

**`bambu-exporter` is deliberately not in `make build`.** It is a server, and `make install` copies everything in `./build/*` into `~/.local/bin` — not where a server belongs. It is built by `container-images/bambu-exporter/Dockerfile` from a pinned `SRC_REF`, so shipping a change to it means bumping that SHA and re-pinning the resulting digest in flux. Build it directly with `go build ./cmd/bambu-exporter` when working on it locally.

Note `make install` does a blind `pkill -x` on every binary name it installs. Harmless for the bar modules (waybar respawns them), but it will also kill a locally-running exporter.

## Conventions

Everywhere:

- **No external dependencies beyond what's in `go.mod`**, which is deliberately tiny: `github.com/godbus/dbus/v5` and nothing else direct. This is a real constraint, not an aspiration — it is why the MQTT framing in `pkg/bambu` and the Prometheus exposition format in `cmd/bambu-exporter` are both hand-rolled rather than pulling in a client library. Both formats are small and stable; the libraries would each be larger than the code using them.
- Flags use the standard `flag` package: `--host`, `--interval`, etc.
- Keep things simple and self-contained; avoid unnecessary abstractions.
- One file per command is the norm and the right default. `cmd/bambu-exporter` is the only one that splits (main / metrics / task, plus an embedded `index.html`), because it is three separable concerns rather than one loop.

Bar modules specifically:

- Polling loop: fetch data, print one JSON line, sleep, repeat.
- Use `pkg/waybar.Waybar` for JSON output (text, tooltip, class, alt). `waybar-allsvenskan` and `waybar-batteries` predate it and marshal their own structs — fine, but new ones should use the shared type.
- Opting into SwiftBar means adding a `--swiftbar` bool flag; when set, the loop is short-circuited to one iteration that prints a SwiftBar frame and the process exits (no JSON, no sleep).

CLIs specifically:

- Subcommand dispatch on `os.Args[1]` with a `flag.NewFlagSet` per subcommand; see `bambu-ctl` or `volvo-ctl`.
- A `waybar` subcommand must **always exit 0 and print exactly one JSON line**, even on failure — a bad exit or stray output blanks or breaks the pill. Errors go to stderr (waybar forwards them to the journal) and surface as a pill `class` instead.
- Long-lived credentials live under `~/.config/`, mode 0600, and are never committed.

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
- **Multiple PRs**: presents a fuzzel dmenu for selection, then opens the chosen PR

Waybar config wires this up via `"on-click": "waybar-github-pr --open"`.

### SwiftBar mode

`--swiftbar` implies `--notify=false` (no `notify-send` on macOS) and skips the `$XDG_RUNTIME_DIR` cache write (the `--open` flow isn't used). Title is `approved·total :arrow.triangle.pull:`, with a `:bell.badge:` variant when there are unread notifications. Dropdown is a single `Open PRs | href=https://github.com/pulls` row — no per-PR list (intentionally simple; can be extended later by adding a list subcommand).

### Workflow runs

The pill also carries GitHub Actions runs needing attention: **approval** (stopped at a deployment gate this user can release), **running** (dispatched by this user, still queued/in progress), and **failed** (dispatched by this user and finished badly).

`failed` exists because the other two states are both transient, and a run that ends in `failure` would otherwise disappear from the pill exactly like a successful one — a broken production apply going quiet is the precise thing this feature is meant to prevent. It therefore sticks until acknowledged: selecting it in the picker records its ID in `dismissed-runs.json` next to the discovery cache, which the daemon reads each tick to filter it out. That file is written only by the short-lived `--open` process and read only by the daemon, so they never contend for it; entries are pruned after 7 days, long after a run drops out of the 20-entry API window. `cancelled` is deliberately not a failure — you cancelled it, so you already know.

Note the running state is genuinely narrow: a gated `terraform apply` here took 47 s against a 60 s poll, so green is often missed entirely. That is expected and not worth chasing with a faster interval — the approval gate, the state that actually blocks, sits for minutes or hours and is always caught. Code lives in `runs.go` — the one split in this repo besides `bambu-exporter`, justified because it owns its own cache, TTL, concurrency and notify state, none of which the PR loop touches.

Four dead ends, so they are not rediscovered:

- **There is no cross-repo API for workflow runs.** GraphQL's `SearchType` enum has no run type, and `search` therefore cannot reach them. That is why the repo set has to be derived instead of queried.
- **A deployment-review request generates no `/notifications` entry.** Checked with `?all=true` — every notification on the account is `PullRequest`-typed. So the existing notifications poll cannot be reused as the discovery channel.
- **`actor=@me` is silently ignored by the REST runs endpoint** — it returns 0 rows rather than erroring. `@me` is a `gh search` extension, not a REST one. The literal login is required, so it's resolved once and stored in the discovery cache. An empty login must skip the query entirely: `actor=` with no value matches *all* actors, which is silently wrong data.
- **The environment name and the reviewer list exist only on `pending_deployments`**, never on the runs listing. That endpoint **404s for any run without a pending deployment** (the normal case for a wait-timer hold), so an error there means "not mine", not "failed". `current_user_can_approve` is the whole "is this mine to act on" signal.

Discovery sweeps every non-archived repo in every org from `/user/orgs` and keeps those with a `required_reviewers` environment: 67 repos → 2 hits on this account, ~4 s at 8-way concurrency (~30 s serial, hence the worker pool). Cached an hour under `$XDG_CACHE_HOME` — deliberately *not* `$XDG_RUNTIME_DIR` like the PR cache, since that is cleared on logout and re-running a ~70-call sweep every login is what the cache exists to prevent. A failed sweep serves the stale list and backs off 10 minutes rather than retrying ~70 doomed calls every tick.

A GraphQL variant of the sweep (`Repository.environments.protectionRules`, enum `REQUIRED_REVIEWERS` in screaming case) collapses it to ~3 calls / ~2.5 s and returns the identical answer — worth doing if the org grows, but note `gh api graphql` **exits 1 on partial errors** even with `data` populated, so it must be one query per org to contain the blast radius.

Two rules the render obeys. Runs are garnish on a PR pill, so nothing in the runs path may `continue` the tick the way the PR fetch does — a workflow-API hiccup must never cost the PR counts. And when any watched repo fails to answer, the run segment is dropped from text, tooltip *and* class together: a partial poll must not colour the pill for a state it can't also show a count for. The tradeoff is that "poll failed" and "nothing pending" currently look identical; a `runs-stale` class serving the last complete poll is the obvious upgrade if that bites.

### Terraform roots pending apply

Optional fourth state (`--apply-repo`, off by default), built for `validio-internal/infra`, where **merging deliberately applies nothing** — the README's recipe ends "Actions → Terraform apply → Run workflow → pick the root", and that step is unenforced. The pill tracks which roots have landed on `main` without being applied since. Picking one in the picker **dispatches the apply**; that only ever reaches a plan, because the workflow's apply job sits behind the `apply` environment's required reviewers.

The whole difficulty is that "files changed since the last apply" is not the same question as "does this need applying", and the naive version is badly wrong in practice. Infra PR #7 touched 25 `.tf` files across 25 roots to repoint wiki URLs at the handbook: every hunk was a comment, `index = 2` was untouched, and the correct answer was zero roots. So:

- **Only files terraform reads count.** A `README.md` or `CLAUDE.md` inside a root cannot change a plan. The list is narrow on purpose — a bare `.yml` would match the `.gitlab-ci.yml` that sat in every root before the CI migration, so only `.env-exec.yaml` qualifies among non-`.tf` files.
- **Comments are stripped and both sides compared.** `GET /repos/{repo}/commits/{sha}` returns a per-file `patch`, so this needs no clone and no terraform. The stripper must be quote-aware or every line holding a URL loses everything from the `//` of `https://` — which is exactly the kind of line being edited here.
- **Everything uncertain counts as substantive.** Absent patch, block comments — all treated as "needs applying". A false positive costs a plan; a false negative hides a real change.
- **A root with no apply history is skipped** unless `--apply-include-unapplied`. Without a baseline, "has commits in the window" says nothing about whether the root is behind. Right after the CI migration that was 37 of 39 roots, every one pointing at the migration commit itself, which buries the one root that genuinely needed applying.

Two things are read rather than reimplemented. The root list comes from parsing the dispatch workflow's `root` choice options — generated by `.github/scripts/list-roots.sh` and CI-validated against it, so it cannot silently go stale (verified: all 39 parse identical to the script's output). The root-to-file mapping mirrors `changed-roots.sh`, including its fan-out from `google/_dashboards/` to every `google/monitoring-*` root.

The cut point between applied and unapplied is the **position** of the last successful apply's `head_sha` in the newest-first commit list, not a timestamp comparison — a dispatched apply plans some time after it starts, so the clocks lie. Per-commit results are cached forever, since a commit is immutable.

This is a proxy for a plan, not a plan: it cannot see console drift, and a substantive-looking edit can still plan empty. `drift.yml`'s weekly real plan across all roots remains the authority.

`--swiftbar` skips runs entirely, which also sidesteps a trap: SwiftBar re-execs the binary every 5 minutes as a fresh process, so a run-enabled swiftbar path would need the discovery cache to persist or it would pay the full sweep on every tick.

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

**Never name a label `job`** — or `instance`, `pod`, `namespace`, `container`, `endpoint`, `service`. The kube-prometheus-stack scrape attaches its own, and Prometheus silently renames the colliding one to `exported_<name>`. `bambulab_print_job_info` shipped with a `job` label and the dashboard's `{{job}}` rendered "bambu-exporter" instead of the print name for months. It is `title` now.

Two other own-goals promtool will not always catch: two samples of one family with an *identical* label set is a scrape error, so anything derived from a repeated array (`hms`, `lights_report`) needs deduplicating; and a family whose samples carry different label *keys* is merely bad form, but an empty label value is indistinguishable from an absent label in Prometheus, so conditionally-present labels are best emitted as `""`.

`bambulab_prints_total` and `bambulab_filament_grams_total` are the only counters, and the only state the exporter holds — see `jobwatch.go`. They are in-process and unpersisted on purpose: a volume would cost the container its `readOnlyRootFilesystem`, and a counter reset is something Prometheus already models. Query them with `increase()`.

### Token expiry

Renewal is interactive, so an unattended deployment breaks about quarterly. `bambulab_cloud_auth_ok` goes to 0 when the cloud rejects the token and the process keeps serving rather than crash-looping (a crash loop would hide the reason, and retrying in-process cannot help — the token is baked into the environment until the pod restarts). `TokenExpiry()` reads the JWT `exp` claim, but **not every token is a JWT**: the emailed-code login flow mints opaque ones, and this printer's account has one, so `bambulab_cloud_token_age_seconds` is the portable early warning.

## Adding a new command

Pick the shape first — the steps differ, and following the bar-module path for a CLI or a service produces the wrong thing.

**A bar module** (reference: `cmd/waybar-cpu-temp`)

1. Create `cmd/<name>/main.go` with an `--interval` flag.
2. Use `pkg/waybar.New()`, set `.Text`, `.ToolTip`, `.Class`, `.Alt`.
3. Call `.Print()` each iteration to emit JSON, then sleep.
4. Add the build line to `Makefile`.
5. Add the module config in chezmoi under `dot_config/waybar/modules/`.

**A CLI** (reference: `cmd/tradfri-ctl`, or `cmd/bambu-ctl` if it needs a pill)

1. Create `cmd/<name>/main.go` with subcommand dispatch and a per-subcommand `FlagSet`.
2. If it feeds a pill, add a `waybar` subcommand that prints one JSON line and always exits 0.
3. Add the build line to `Makefile`.
4. Add the `go install …@latest` line to chezmoi's `run_onchange_install-waybar-modules.sh`.

**A service** (reference: `cmd/bambu-exporter`)

1. Create `cmd/<name>/`, splitting files by concern; config from the environment so it suits a Kubernetes `envFrom: secretRef`.
2. Take a `context` cancelled on SIGINT/SIGTERM and shut the listeners down on it.
3. Keep `/metrics` on a separate port from anything user-facing, so the ingress can expose one and not the other.
4. Leave it **out** of the `Makefile` (see Building), add a `Dockerfile` under `polarn/container-images` pinning `SRC_REF`, then a flux app under `apps/microk8s/`.

Whichever shape: a metric or a field is emitted only when the upstream actually reported it. Publishing a zero for an absent value is how `chamber_temper` silently graphed 0 °C for months.

### Adding SwiftBar support to an existing module

1. Add a `--swiftbar` bool flag.
2. After computing the `Waybar` struct (or whatever data the loop normally prints), branch: if swiftbar, call a `printSwiftBar*` helper that emits the SwiftBar frame and `return` from `main` (one-shot; no sleep).
3. The helper writes plaintext lines: title (with optional `:sf.symbol:`), then `---`, then dropdown rows.
4. Add the binary name to `SWIFTBAR_BINS` in the Makefile so `make install-swiftbar` picks it up.
5. Add a corresponding plugin script under `~/.config/swiftbar/plugins/<name>.<interval>.sh` (tracked in chezmoi under the `darwin` guard) that just `exec`s the binary with `--swiftbar` and any host-specific flags.

## Why it is still called waybar-modules

The name undersells the contents — roughly 57% of the Go here is not a bar module — so this comes up. It was considered and declined in August 2026; the reasoning, so it need not be rediscovered:

**Splitting on "waybar vs not" is the wrong cut.** It runs straight through three shared packages: `pkg/bambu` (used by both `bambu-ctl`'s pill and `bambu-exporter`), `pkg/dirigera` (used by `tradfri-ctl` *and* `waybar-tradfri`/`waybar-tradfri-auth`), and `pkg/waybar` itself (linked by `volvo-ctl` and `bambu-ctl` for their `waybar` subcommands). The natural seam is per-domain — bambu, volvo, tradfri, bar modules — where each domain owns its package outright. If this is ever revisited, split that way, and take `bambu` first: it is the largest and most active area, is already a deployed service with its own image and flux app, and is already outside `make build`.

**Renaming the repo is the risky operation, not extracting from it.** `https://polarn.github.io/waybar-modules/volvo-callback/` is served from `docs/volvo-callback/` here and is the registered OAuth redirect URI of a published Volvo app whose settings are fixed at publication. Changing the repo name, or moving that directory elsewhere, breaks `volvo-ctl auth` until a new Volvo app is published. Extracting a *different* domain into a new repo leaves it untouched.

Nothing outside imports this module as a library and it has never been tagged — consumers use `@latest` (chezmoi) or raw commit SHAs (`SRC_REF` in container-images). So a split would not break Go dependency resolution, but it would multiply the stale-proxy footgun already documented in chezmoi's CLAUDE.md across N repos.
