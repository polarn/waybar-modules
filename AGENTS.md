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
- `cmd/waybar-github-pr/` - Your open PRs (approved·total), PRs awaiting your or your team's review, GitHub notifications, workflow runs, merge-queue state, terraform roots pending apply and repos with unreleased changes, with a fuzzel picker on click. Runs as a work and a personal pill (`--scope`). SwiftBar-enabled.
- `cmd/waybar-wiim-nowplaying/` - Now-playing info from a WiiM device (amp/mini/pro), with volume control. SwiftBar-enabled.
- `cmd/waybar-cpu-temp/` - CPU temperature, tooltip lists every CPU hwmon sensor
- `cmd/waybar-gpu-temp/` - GPU temperature; picks the card with the most sensors (the discrete one), tooltip lists loaded llama-swap models
- `cmd/waybar-tradfri/` - State of a chosen set of DIRIGERA lights
- `cmd/waybar-inhibitors/` - How many clients hold a screensaver idle-inhibitor, and which
- `cmd/waybar-allsvenskan/` - Allsvenskan fixtures and live scores for a configured team
- `cmd/waybar-batteries/` - Aggregated battery level across Bluetooth (UPower) and SteelSeries (headsetcontrol) peripherals

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
- `run_onchange_install-waybar-modules.sh`: the `go install …@latest` list. For macOS, `run_onchange_install-swiftbar-modules.sh` and `dot_config/swiftbar/plugins/`.
- `dot_config/systemd/user/waybar-inhibitors.service`: `waybar-inhibitors` runs as a user unit, and the pill reads its output file.

## Building

- `make build` - Compiles the 11 desktop binaries to `./build/`
- `make install` - Builds, installs to `~/.local/bin/`, and kills running instances. Waybar respawns only the modules whose config sets `restart-interval`, so run `systemctl --user restart waybar` afterwards
- `make build-swiftbar` / `make install-swiftbar` - Same flow but only for the SwiftBar-enabled subset (currently `waybar-github-pr`, `waybar-wiim-nowplaying`). Used on macOS where the Linux-only modules (hwmon temps, tradfri, etc.) wouldn't compile or be useful.
- `make clean` - Removes `./build/`

Note `make install` does a blind `pkill -x` on every binary name it installs.

## Conventions

Everywhere:

- **No external dependencies beyond what's in `go.mod`**, which is deliberately tiny: `github.com/godbus/dbus/v5` and nothing else direct. This is a real constraint, not an aspiration — every HTTP API here is talked to with `net/http` and `encoding/json` by hand, and the bambu repo hand-rolls MQTT for the same reason.
- Flags use the standard `flag` package: `--host`, `--interval`, etc.
- Keep things simple and self-contained; avoid unnecessary abstractions.
- One file per command is the norm and the right default. `cmd/waybar-github-pr/runs.go` is the only split, because it owns its own cache, TTL, concurrency and notify state.

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

Note the running state is genuinely narrow: a gated `terraform apply` here took 47 s against a 60 s poll, so green is often missed entirely. That is expected and not worth chasing with a faster interval — the approval gate, the state that actually blocks, sits for minutes or hours and is always caught. Code lives in `runs.go` — the one split in this repo, justified because it owns its own cache, TTL, concurrency and notify state, none of which the PR loop touches.

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

## What moved out

In October 2026 the Bambu Lab and Volvo domains were split into `polarn/bambu` (`bambu-ctl`, `bambu-exporter`, `pkg/bambu`) and `polarn/volvo` (`volvo-ctl`, `volvo-exporter`, `pkg/volvo`), each with its history filtered out of this one. The cut is per domain, not "waybar vs not": a CLI and its exporter share the client package, so they travel together. tradfri stayed because two of its three commands are bar modules and the whole domain is 1.3k lines.

Each of those repos carries its own copy of `pkg/waybar` (30 lines) rather than importing this module: a cross-module dependency on `@latest` would multiply the stale-proxy footgun documented in chezmoi's CLAUDE.md.

**`docs/volvo-callback/` stays here even though the Volvo code left.** `https://polarn.github.io/waybar-modules/volvo-callback/` is the registered OAuth redirect URI of a published Volvo app, fixed at publication, and GitHub Pages does not redirect after a repo rename. Renaming this repo, or moving that directory, breaks `volvo-ctl auth` and the exporter's login until a new Volvo app is published.

Nothing imports this module as a library and it has never been tagged — chezmoi uses `@latest` and `polarn/container-images` pins raw commit SHAs (`SRC_REF`). Dockerfiles that still pin a pre-split SHA of this repo keep building, since the history is intact.
