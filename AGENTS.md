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
- `cmd/waybar-github-pr/` - Your open PRs (approved·total), PRs awaiting your or your team's review, GitHub notifications, workflow runs, merge-queue state, terraform roots pending apply and repos with unreleased changes, with a fuzzel picker on click. Runs as a work and a personal pill (`--scope`). SwiftBar-enabled.
- `cmd/waybar-wiim-nowplaying/` - Now-playing info from a WiiM device (amp/mini/pro), with volume control. SwiftBar-enabled.
- `cmd/waybar-cpu-temp/` - CPU temperature, tooltip lists every CPU hwmon sensor
- `cmd/waybar-gpu-temp/` - GPU temperature; picks the card with the most sensors (the discrete one), tooltip lists loaded llama-swap models
- `cmd/waybar-tradfri/` - State of a chosen set of DIRIGERA lights
- `cmd/waybar-inhibitors/` - How many clients hold a screensaver idle-inhibitor, and which
- `cmd/waybar-allsvenskan/` - Allsvenskan fixtures and live scores for a configured team
- `cmd/waybar-batteries/` - Aggregated battery level across Bluetooth (UPower) and SteelSeries (headsetcontrol) peripherals

CLIs (one-shot):

- `cmd/bambu-ctl/` - Bambu Lab cloud CLI (`login`/`status`/`waybar`/`pause`/`resume`/`toggle`/`stop`/`speed`/`light`); feeds the custom/p2s printer pill
- `cmd/volvo-ctl/` - Volvo cloud CLI (`auth`/`vehicles`/`status`/`location`/`climate`/`waybar`); feeds the custom/volvo pill. With `VOLVO_EXPORTER_URL` set, `status`/`location`/`waybar` read volvo-exporter's `/state` instead of the API
- `cmd/tradfri-ctl/` - DIRIGERA control CLI (toggle/set lights, rotate Sonos scenes). Containerised for the flux `tradfri` CronJob.
- `cmd/waybar-tradfri-auth/` - One-time DIRIGERA pairing; writes the token the other two read. Named `waybar-*` for historical reasons — it is a helper, not a bar module.

Service:

- `cmd/bambu-exporter/` - Holds one cloud MQTT subscription open; serves Prometheus metrics, a JSON state endpoint and a status page. Deployed via the flux `bambu-exporter` app.
- `cmd/volvo-exporter/` - Polls the Volvo API every 5 min (doors, windows and engine every minute while the car is awake) as the sole holder of the refresh token; serves Prometheus metrics, `/state` for the pill, and a loopback-only login. Deployed via the flux `volvo-exporter` app.

Packages:

- `pkg/bambu/` - Bambu cloud client: HTTP login flow, session cache, hand-rolled minimal MQTT 3.1.1 (no client library), plus the persistent subscription and report accumulator the exporter needs
- `pkg/volvo/` - Volvo cloud client: OAuth with PKCE + rotating refresh token, Energy/Connected Vehicle/Location APIs
- `pkg/dirigera/` - DIRIGERA REST client: pairing, devices, scenes
- `pkg/hwmon/` - Shared hwmon sensor discovery and reading
- `pkg/waybar/` - Shared `Waybar` struct with JSON output and `Print()` method

## Where it is wired up

Every consumer lives in chezmoi (`~/.local/share/chezmoi`). Its `CLAUDE.md` has a table mapping each pill to its module file and binary.

- `dot_config/waybar/modules/<name>.jsonc`: each pill's `exec` line, flags, clicks and `restart-interval`. `config.jsonc` places it on a bar.
- `dot_config/waybar/style.css`: colours keyed `#custom-<name>.<class>`, so a new `class` needs a rule there.
- `run_onchange_install-waybar-modules.sh`: the `go install …@latest` list. For macOS, `run_onchange_install-swiftbar-modules.sh` and `dot_config/swiftbar/plugins/`.
- `dot_config/systemd/user/waybar-inhibitors.service`: `waybar-inhibitors` runs as a user unit, and the pill reads its output file.

## Building

- `make build` - Compiles the 13 desktop binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances. Waybar respawns only the modules whose config sets `restart-interval`, so run `systemctl --user restart waybar` afterwards
- `make build-swiftbar` / `make install-swiftbar` - Same flow but only for the SwiftBar-enabled subset (currently `waybar-github-pr`, `waybar-wiim-nowplaying`). Used on macOS where the Linux-only modules (hwmon temps, tradfri, etc.) wouldn't compile or be useful.
- `make clean` - Removes `./build/`

**`bambu-exporter` and `volvo-exporter` are deliberately not in `make build`.** It is a server, and `make install` copies everything in `./build/*` into `~/.local/bin` — not where a server belongs. It is built by `container-images/bambu-exporter/Dockerfile` from a pinned `SRC_REF`, so shipping a change to it means bumping that SHA and re-pinning the resulting digest in flux. Build it directly with `go build ./cmd/bambu-exporter` when working on it locally.

Note `make install` does a blind `pkill -x` on every binary name it installs, so it also kills a locally-running exporter.

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

Two `gh search prs --state=open --author=@me` calls per tick: one with `--review=approved` for the approved count, and one without for the total.

### Click-to-open (`--open` flag)

The polling loop writes the current PR list to `$XDG_RUNTIME_DIR/waybar-github-prs-<scope>.json` as a cache (`waybar-github-prs.json` without `--scope`). The `--open` flag reads this cache and:

- **0 PRs**: does nothing
- **1 PR**: opens directly via `xdg-open`
- **Multiple PRs**: presents a fuzzel dmenu for selection, then opens the chosen PR

Waybar config wires this up via `"on-click": "waybar-github-pr --open --scope=<scope>"`, in chezmoi's `dot_config/waybar/modules/github.jsonc`.

`--hide-titles=<regexp>` leaves PRs and review requests whose title matches out of the picker, along with notifications about them (otherwise a hidden PR's `author` notification would fall through to the Notifications group). The prompt says how many were hidden. Left-click passes it to skip mass-produced PRs (`Add CODEOWNERS`); right-click doesn't, and shows everything. The pill text and tooltip are not filtered.

The PR list and the notification list are independent queries that overlap: a notification with reason `author`/`assign`/`comment` names a PR that is usually already in your open-PR list, so the picker offered the same thing twice. Notifications are matched against the PR entries by resolved web URL (`subjectWebURL` turns `/repos/O/R/pulls/N` into `/O/R/pull/N`, which is exactly what `gh search prs --json url` returns) and folded onto the existing row as a trailing bell plus reason, rather than dropped — the reason is the part worth keeping. The tooltip folds the same way (`foldNotifications`), while the pill's bell count still includes every unread notification.

GitHub never updates a thread's `reason`, so a `review_requested` thread goes unread again with that reason on any later activity (a merge-queue add, a push) after the review is done. When the review-request search succeeded, a `review_requested` notification whose PR it doesn't list is dropped before notify-send and the count; if the search failed, it is kept.

A thread keeps its notification ID until it is read, so new activity on an unread thread (a second comment on your PR) only moves its `updated_at`. `seenNotifs` therefore keys on ID plus `updated_at`, and each new update pops up again.

PR comment counts (tooltip and picker, not the pill) ride on the merge-queue facts query: `totalCommentsCount` (issue comments, review-thread comments and non-empty review bodies) minus bot-authored issue comments, because `linear-code` and the `github-actions` terraform plans comment on nearly every infra PR. GTK3 tooltips wrap at 70 chars (`max-width-chars` is hard-coded in `gtktooltipwindow.ui` and unreachable from CSS), so the tooltip goes out inside one `<span allow_breaks="false">` and grows to its longest line instead.

### Review requests (`--reviews`)

One GraphQL search, `review-requested:@me -author:@me`, covers both of GitHub's dashboard sections ("Needs your review" and "Needs your teams' review"). `-author:@me` keeps out your own PRs when CODEOWNERS requests a team you're in. A row names the requested teams by slug, unless you're also requested by name, in which case it names none. Both kinds also send a `review_requested` notification, but that leaves the pill once it's read, often from email, while the request is still open. The search keeps the PR on the pill until it's reviewed, and the picker folds any unread notification onto the review row the same way `author` notifications fold onto your own PRs. The pill shows a count (nf-oct-code_review) and adds the `review` class. A failed query drops the segment rather than showing 0. PRs authored by `dependabot` (GraphQL drops the `[bot]` suffix) get their own count (nf-oct-dependabot), and their own "Dependabot" group after "Review requested" in the tooltip and picker. Renovate gets the same after that (nf-fa-paint_roller, "Renovate"), matching the hosted app `renovate` and self-hosted apps named `*-renovate` (Validio's is `validio-renovate`). Bot rows omit the author but keep the teams. Every kind adds the `review` class. A `review_requested` notification on a Dependabot or Renovate PR fires no notify-send (Renovate opens a dozen at once); it still counts and shows in the picker. The match is by author, not title, so a person's `chore:` PR still pops up. `--quiet-review-repos=owner/repo,…` does the same for every review request in those repos; other reasons there (a mention, activity on your own PR) still pop up.

In the picker only, human review requests are grouped further, each group under its own divider between "Review requested" and "Dependabot". A title sent to 3+ repos, ignoring ticket keys like `[PL-4798]`, forms one group whose rows show the repo and ticket rather than the title. Then a repo with 3+ of the remaining requests forms a group whose rows show `#N` and the title. Rows in a group are sorted by repo and number rather than by last update, so the order stays put between polls; the rest stay in "Review requested" by update time. The tooltip doesn't group.

### Scopes (`--scope`, `--work-owners`)

One binary runs as two pills: `--scope=work` keeps repos whose owner is in `--work-owners`, `--scope=personal` keeps the rest (upstream projects included). The filter is client-side and applies to PRs, notifications and runs alike, so each notify-send fires from exactly one instance. The scope names the cache file (`waybar-github-prs-<scope>.json`), so `--open` must pass the same `--scope`. Run discovery sweeps `/user/orgs` only and never finds user-owned repos, which is why the personal pill runs with `--runs=false` and skips the ~70-call sweep. `--hide-empty` blanks the text (waybar hides the pill) when nothing is open or pending.

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

Optional fourth state (`--apply-repo`, off by default), built for `validio-internal/infra`, where **merging deliberately applies nothing** — the README's recipe ends "Actions → Terraform apply → Run workflow → pick the root", and that step is unenforced. The pill tracks which roots have landed on `main` without being applied since. Picking one in the picker **dispatches the apply**; that only ever reaches a plan, because the workflow's apply job sits behind the `apply` environment's required reviewers. With more than one root pending, the section ends with an apply-all row that dispatches every listed root and sends one notification for the lot. It goes last so it can never be the row fuzzel opens on.

The whole difficulty is that "files changed since the last apply" is not the same question as "does this need applying", and the naive version is badly wrong in practice. Infra PR #7 touched 25 `.tf` files across 25 roots to repoint wiki URLs at the handbook: every hunk was a comment, `index = 2` was untouched, and the correct answer was zero roots. So:

- **Only files terraform reads count.** A `README.md` or `CLAUDE.md` inside a root cannot change a plan. The list is narrow on purpose — a bare `.yml` would match the `.gitlab-ci.yml` that sat in every root before the CI migration, so only `.env-exec.yaml` qualifies among non-`.tf` files.
- **Comments are stripped and both sides compared.** `GET /repos/{repo}/commits/{sha}` returns a per-file `patch`, so this needs no clone and no terraform. The stripper must be quote-aware or every line holding a URL loses everything from the `//` of `https://` — which is exactly the kind of line being edited here.
- **Everything uncertain counts as substantive.** Absent patch, block comments — all treated as "needs applying". A false positive costs a plan; a false negative hides a real change.
- **A root with an apply already in flight is skipped.** Not merely to avoid saying the same thing twice: the entry dispatches on click, and apply.yml's `concurrency: tf-apply-<root>` group queues rather than cancels, so acting on a stale entry silently stacks a duplicate run behind the first. Watch for status `pending` when detecting this — that is what GitHub reports for a run held behind that concurrency group, and it is easy to miss alongside the more obvious `waiting`/`in_progress`.
- **A root with no apply history is skipped** unless `--apply-include-unapplied`. Without a baseline, "has commits in the window" says nothing about whether the root is behind. Right after the CI migration that was 37 of 39 roots, every one pointing at the migration commit itself, which buries the one root that genuinely needed applying.

Two things are read rather than reimplemented. The root list comes from parsing the dispatch workflow's `root` choice options — generated by `.github/scripts/list-roots.sh` and CI-validated against it, so it cannot silently go stale (verified: all 39 parse identical to the script's output). The root-to-file mapping mirrors `changed-roots.sh`, including its fan-out from `google/_dashboards/` to every `google/monitoring-*` root.

The cut point between applied and unapplied is the **position** of the last successful apply's `head_sha` in the newest-first commit list, not a timestamp comparison — a dispatched apply plans some time after it starts, so the clocks lie. Per-commit results are cached forever, since a commit is immutable.

This is a proxy for a plan, not a plan: it cannot see console drift, and a substantive-looking edit can still plan empty. `drift.yml`'s weekly real plan across all roots remains the authority.

`--swiftbar` skips runs entirely, which also sidesteps a trap: SwiftBar re-execs the binary every 5 minutes as a fresh process, so a run-enabled swiftbar path would need the discovery cache to persist or it would pay the full sweep on every tick.

### Unreleased changes (`--release-repos`)

Optional fifth state, off by default, for the listed repos, released by dispatching `--release-workflow` (`release.yml`) with a `version` input. Every such workflow in `validio-internal` (postgres, redis, surface, ve, the websites…) shares that shape: it tags, retags the `g<sha>` image built on main, publishes the chart and creates the GitHub release.

- **Which repos:** an explicit list, not discovered. Discovering "repos whose release workflow you have dispatched" picked up surface, released once and not wanted here, with 28 unreleased commits. The listed repos are also unioned into the runs watch set, so a release you dispatch shows as running and, if it breaks, as failed.
- **Pending:** the highest semver among the latest 20 non-draft, non-prerelease releases, compared `<tag>...main`. Commits whose files are all under `.github/` or `.claude/`, or are markdown, don't count: the image and chart are unchanged, so redis's CODEOWNERS and CI-only commits don't ask for a release. A truncated file list (300) counts. A repo with a release run in flight is skipped, as with applies.
- **Version:** patch, minor for any `feat`, major for any `!`, read from conventional-commit subjects with or without a leading `[TICKET]` (surface writes `[UI-8704] feat(ui): …`). Only a suggestion: postgres 3.8.1 shipped a `feat` as a patch.
- **Picking a row** opens a second fuzzel prompt (`--match-mode=exact`) listing the suggested version, the other two bumps and the compare page; typing a version that matches nothing dispatches that instead. There is no environment gate behind a release, unlike apply, so the row never dispatches on its own, and a lone release row doesn't take the single-item shortcut.
- A repo whose poll fails serves its last answer, so a transient error doesn't drop the row and re-notify on the next tick. Notifications are keyed on repo and tag.

`--release-dry-run` prints each listed repo's verdict and reason.

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

## volvo-exporter module details

### One token holder

Volvo rotates the refresh token on every refresh (single-use) and access tokens last 5 minutes, so every poll refreshes. Two processes holding copies of `tokens.json` will race: whichever refreshes second gets `invalid_grant` and the chain needs a browser login. The `TokenStore` flock only serialises processes that share one file. So the exporter owns the token on its PVC, the pill reads `/state`, and the Deployment uses `strategy: Recreate` so two pods never overlap. Never seed the pod from a *copy* of a live `~/.config/volvo/tokens.json`; log in through the exporter instead. A local test run is safe only with `VOLVO_DATA_DIR=~/.config/volvo` (the same file, the same lock).

Shutdown waits up to 25 s for an in-flight poll rather than cancelling it: a refresh the server has accepted but the process never saved spends the token.

### Login

`/auth` and `/callback` listen on `127.0.0.1:20999` inside the pod, so they are reachable only through `kubectl port-forward`, which matches the hardcoded target of the `docs/volvo-callback` forwarding page. Run `kubectl -n volvo-exporter port-forward deploy/volvo-exporter 20999`, open `http://localhost:20999/auth`, and log in; the callback saves the tokens and triggers an immediate poll. `volvo_auth_ok` goes to 0 when the refresh token is rejected, and the poller keeps re-reading `tokens.json` at the normal interval. It logs the login hint once, not on every poll, and words it for `/auth` rather than the `volvo-ctl auth` that `pkg/volvo`'s errors suggest.

### Polling and quota

Each poll is 10 requests (energy, location, 8 Connected Vehicle leaves): 2,880/day at the 5 min default, against the app's 10,000/day and 100/min. Only the energy read is required; the other parts are best-effort and counted in `volvo_partial_errors_total`. A 429 (`RateLimitError`, including from the token endpoint, which must not be read as a lapsed grant) or other failure doubles the wait up to 1 h, or longer if `Retry-After` says so.

Between full polls, while any doors, windows or engine-status field carries a car timestamp under 10 min old, the exporter re-reads those three leaves every `VOLVO_FAST_INTERVAL` (1 min) and swaps them into a copy of the snapshot. A field's `timestamp` is when the car last *reported* the leaf, shared by every field in it, not when that field changed: the car re-sends unchanged state for a few minutes after a drive and then goes quiet. So a parked, sleeping car costs nothing extra, and the worst case (always awake) is 12 more requests per 5 min, 6,336/day in total. Any fast-poll error turns fast polling off until the next good full poll, which owns the backoff. `volvo_fast_polls_total` counts them and `volvo_reported_timestamp_seconds{leaf}` exports the report times.

### Metrics

Same rules as bambu-exporter: hand-rolled text, base units (distances in `_meters`; promtool rejects `_kilometers`), and a metric only when the API reported the field (`chargingPower` is `PROPERTY_NOT_FOUND` when idle). Location is in `/state` for the pill but deliberately not exported to Prometheus.

## Adding a new command

Pick the shape first — the steps differ, and following the bar-module path for a CLI or a service produces the wrong thing.

**A bar module** (reference: `cmd/waybar-cpu-temp`)

1. Create `cmd/<name>/main.go` with an `--interval` flag.
2. Use `pkg/waybar.New()`, set `.Text`, `.ToolTip`, `.Class`, `.Alt`.
3. Call `.Print()` each iteration to emit JSON, then sleep.
4. Add the build line to `Makefile`.
5. Add the module config in chezmoi under `dot_config/waybar/modules/`, with `restart-interval` so waybar respawns it after `make install`, and place it in `config.jsonc`.
6. Add the `go install …@latest` line to chezmoi's `run_onchange_install-waybar-modules.sh`.

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
