# waybar-modules
Waybar modules written in GO, started to learn some GO. See [AGENTS.md](AGENTS.md) for the conventions each kind follows.

Two kinds of thing live here:

- **Bar modules** — daemons that print one JSON line per tick for Waybar's `custom` module type: `waybar-gitlab-mr`, `waybar-github-pr`, `waybar-wiim-nowplaying`, `waybar-cpu-temp`, `waybar-gpu-temp`, `waybar-tradfri`, `waybar-inhibitors`, `waybar-allsvenskan`, `waybar-batteries`
- **CLIs** — one-shot tools: `tradfri-ctl`, `waybar-tradfri-auth`

`make` builds everything; binaries land in the `build` folder. `make install` copies them to `~/.local/bin`.

The Bambu Lab printer tooling (`bambu-ctl`, `bambu-exporter`) and the Volvo tooling (`volvo-ctl`, `volvo-exporter`) grew here and moved to [polarn/bambu](https://github.com/polarn/bambu) and [polarn/volvo](https://github.com/polarn/volvo) in October 2026. `docs/volvo-callback/` stays: its GitHub Pages URL is the registered OAuth redirect of the published Volvo app.

## waybar-gitlab-mr
A Waybar "module" to display number of merge requests the user has to review

It needs a Gitlab personal access token with api read only access. It needs to be set using the `GITLAB_TOKEN` environment variable.

You can control how often it will poll the Gitlab API, by default it will poll every `60` seconds. Use the CLI option `-interval X` to change.

The command will output JSON that Waybar can use to update a module. The fields outputted are:

* text - the number of merge requests found, 0 or more.
* tooltip - outputs merge request titles separated with linefeed.
* class - For usage in `style.css`, outputs `none` or `found` depending on if there is a merge request or not.
* alt - use this to pick an icon using `format-icons`

Add this to your Waybar configuration file, usually `~/.config/waybar/config`:

```json
    "custom/gitlab": {
        "format": "{} {icon}",
        "return-type": "json",
        "format-icons": {
            "found": "",
            "none": ""
        },
        "exec-if": "which waybar-gitlab-mr",
        "exec": "GITLAB_TOKEN=<token-with-read-api> waybar-gitlab-mr"
    }
```

Here is an example of how to style it using the `~/.config/waybar/style.css` file, remember that `class` outputted in the JSON from the command will have `none` or `found` set so you can use that here. You can also just skip the class part of the style, and just have one block.

```css
#custom-gitlab.none {
    margin-top: 6px;
    margin-left: 8px;
    padding-left: 10px;
    margin-bottom: 0px;
    padding-right: 10px;
    border-radius: 10px;
    transition: none;
    color: #514112;
    background: #d4ab30;
}

#custom-gitlab.found {
    margin-top: 6px;
    margin-left: 8px;
    padding-left: 10px;
    margin-bottom: 0px;
    padding-right: 10px;
    border-radius: 10px;
    transition: none;
    color: #78611a;
    background: #fecf48;
}
```

## waybar-wiim-nowplaying
A Waybar module that displays the currently playing song from a WiiM device (amp, mini, pro, etc.) on your network.

It uses the LinkPlay HTTP API exposed by WiiM devices to poll for playback status.

Usage: `waybar-wiim-nowplaying --host <wiim-ip>` with an optional `--interval` flag (default 5 seconds).

The command outputs JSON with:

* text - "Artist - Title" when playing, empty when stopped/paused.
* tooltip - Title, artist and album on separate lines.
* class - `playing` or `stopped` for styling.
* alt - same as class, for `format-icons`.

Add this to your Waybar configuration file, usually `~/.config/waybar/config`:

```json
    "custom/wiim": {
        "format": "{} {icon}",
        "return-type": "json",
        "format-icons": {
            "playing": "󰎈",
            "stopped": "󰎊"
        },
        "exec-if": "which waybar-wiim-nowplaying",
        "exec": "waybar-wiim-nowplaying --host 192.168.1.100"
    }
```

## waybar-github-pr
Shows how many of your open GitHub PRs are approved and ready to merge, with a bell variant when there are unread notifications. Authentication piggybacks on the `gh` CLI, so `gh auth login` is the only setup. Clicking runs the binary again with `--open`: one ready PR opens directly via `xdg-open`, several present a `fuzzel` picker first. It also fires `notify-send` when a PR becomes ready, and caches state under `$XDG_RUNTIME_DIR` so the click path doesn't re-query. SwiftBar-enabled.

It also tracks GitHub Actions runs that want your attention: runs stopped at a deployment gate **you** can approve, runs **you** dispatched that are still going, and ones that finished badly — a failed run sticks in the pill until you click it, so a broken deploy can't go quiet the way a successful one does. Each becomes a picker entry linking to the run (approving stays in the browser — a mis-select in a fuzzel list should not deploy to production), and a new gate raises a `notify-send`. With `--apply-repo` it additionally tracks terraform roots that were merged to `main` but never applied — picking one dispatches the apply for you. That comparison ignores files terraform doesn't read and strips comments before diffing, so a PR that only rewrites comments across two dozen roots correctly reports nothing to do. The repos to poll are discovered automatically — every non-archived repo in your orgs that has an environment gated on `required_reviewers` — and cached for an hour under `$XDG_CACHE_HOME`; `--watch-runs` adds repos discovery can't find, and `--discover-only` prints the set. `--runs=false` turns the whole thing off.

## waybar-cpu-temp
CPU temperature, with a tooltip listing every CPU-related hwmon sensor it found. Sensors are auto-discovered, so there's nothing to configure per machine — `--interval` is the only flag.

## waybar-gpu-temp
GPU temperature, same auto-discovery. When there's both a discrete card and an iGPU it picks the one exposing the most sensors, which is the discrete one. The tooltip additionally lists models currently loaded in [llama-swap](https://github.com/mostlygeek/llama-swap), polled from `--llama-swap` (default `http://localhost:9292`); that part is skipped quietly if nothing answers. `--sensor` and `--model` override the auto-pick.

## waybar-tradfri
State of a chosen set of lights on an IKEA DIRIGERA hub. The pill is one dot per tracked light, each painted in that light's actual colour (or a dim grey when it's off), followed by `on/total`. The tooltip names each light and its brightness. Needs `--host` (the hub) and `--token`, which you get once from `waybar-tradfri-auth`.

## waybar-tradfri-auth
One-time pairing helper for a DIRIGERA hub: press the hub's action button when prompted and it writes an access token to `--output` (default `~/.config/waybar-tradfri/token`, mode 0600). Both `waybar-tradfri` and `tradfri-ctl` read that same path. Despite the `waybar-` prefix it is a plain CLI, not a bar module — the name is historical. The token is per-hub and never committed, so every host pairs separately.

## tradfri-ctl
Control CLI for the same DIRIGERA hub — the read/write companion to the read-only `waybar-tradfri`. Subcommands: `list`, `toggle`, `set`, `scenes`, `music`, `set-music`, and `rotate-music`. It defaults its token path to `~/.config/waybar-tradfri/token` and will tell you to run `waybar-tradfri-auth` if that's missing.

`rotate-music` is the reason this one is containerised: it rewrites which Sonos favorite a SOMRIG button scene plays, and runs daily as a Kubernetes CronJob (image `ghcr.io/polarn/tradfri-ctl`, built from `polarn/container-images`).

## waybar-allsvenskan
Allsvenskan fixtures and live scores, via FotMob's unofficial JSON endpoint. The pill shows the most relevant match for the configured team, preferring live, then scheduled today, then finished today, then yesterday's result, then the next upcoming one. The tooltip lists every Allsvenskan match across yesterday, today and the next week (`--days-before` defaults to 1, `--days-after` to 7). Pass `--team` once per team you follow; matching is on the exact name, case-insensitive. Desktop notifications for kickoff, goals, half time and full time are **on by default** (`--notify=false` to silence); `--notify-all` extends them to every Allsvenskan match rather than just your teams.

## waybar-batteries
One pill for every battery-equipped peripheral it can reach: Bluetooth devices via `upower`, and SteelSeries USB-dongle headsets via `headsetcontrol`. Up to `--max-inline` devices (default 2) are all shown inline, sorted by charge ascending; beyond that it shows just the lowest one plus a `+N`, with the rest in the tooltip. Adding a new source means writing one poll function that returns `[]Device`.

