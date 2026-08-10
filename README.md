# waybar-modules
Just some personal homelab tools written in GO. Started out as Waybar modules to learn some GO, and mostly still is — but it has since grown a few cloud CLIs and one Prometheus exporter that runs in Kubernetes. The name stayed; see [AGENTS.md](AGENTS.md) for why, and for the conventions each kind follows.

Three kinds of thing live here:

- **Bar modules** — daemons that print one JSON line per tick for Waybar's `custom` module type: `waybar-gitlab-mr`, `waybar-github-pr`, `waybar-wiim-nowplaying`, `waybar-cpu-temp`, `waybar-gpu-temp`, `waybar-tradfri`, `waybar-inhibitors`, `waybar-allsvenskan`, `waybar-batteries`
- **CLIs** — one-shot tools, some with a `waybar` subcommand for a pill: `bambu-ctl`, `volvo-ctl`, `tradfri-ctl`, `waybar-tradfri-auth`
- **A service** — `bambu-exporter`, long-running, serves HTTP, no Waybar output

`make` builds everything except `bambu-exporter` (that one is built by its container image); binaries land in the `build` folder. `make install` copies them to `~/.local/bin`.

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

## volvo-ctl
A CLI for Volvo's cloud API (Energy API v2) that reports an EV's battery/charging state. `volvo-ctl waybar` emits one JSON line for a Waybar custom module (run it with `"interval"`, not as a daemon).

Needs an application from [developer.volvocars.com](https://developer.volvocars.com) published with the scopes listed in `pkg/volvo/config.go` (`openid`, the `conve:*` read scopes for doors/windows/engine/odometer/tyres/warnings/diagnostics/trip statistics, `energy:state:read`, `energy:capability:read`, `location:read` — everything read-only, no `conve:commands`). Simplest is to tick every scope except commands when publishing. Scopes are **fixed at publication**: widening them means creating a new app, swapping all three credentials, and re-running `volvo-ctl auth` — the API answers 403 for unconsented endpoints, which volvo-ctl degrades from rather than erroring. The portal refuses localhost redirect URIs, so register the forwarding page served from this repo's GitHub Pages instead: `https://polarn.github.io/waybar-modules/volvo-callback/` (source in `docs/volvo-callback/` — it forwards the OAuth callback, query string intact, to `http://localhost:20999/callback` where `volvo-ctl auth` is listening; fork the repo or host a copy anywhere if you don't want to trust mine). Put the credentials in `~/.config/volvo/config.json`:

```json
    {"client_id": "...", "client_secret": "...", "vcc_api_key": "...",
     "redirect_uri": "https://polarn.github.io/waybar-modules/volvo-callback/", "vin": "..."}
```

`redirect_uri` must match the portal registration character-for-character. Omit it only for apps registered back when localhost redirect URIs were allowed.

Then run `volvo-ctl auth` (opens a browser for the Volvo ID login; tokens are kept fresh automatically in `~/.config/volvo/tokens.json`). Personal Volvo apps have a limited consent grant, so expect to re-run `auth` periodically — the waybar output switches to a `reauth` class when that happens. See `volvo-ctl help` for the other subcommands (`vehicles`, `status`, `location` — the latter prints the car's last GPS fix with OpenStreetMap/Google Maps links, and `--open` jumps straight to OSM in the browser; the waybar tooltip shows the same fix as a "Last seen" line when the scope is granted). `volvo-ctl climate <start|stop>` remotely starts/stops pre-climatization — the tool's only car-actuating command (scope `conve:climatization_start_stop`; everything else is read-only by design).

## bambu-ctl
A CLI for Bambu Lab's cloud that reports 3D-printer state (tested with a P2S). `bambu-ctl waybar` emits one JSON line for a Waybar custom module (run it with `"interval"`, not as a daemon) — progress + time left while printing, nozzle temperature otherwise, with the full report (temps, AMS humidity, loaded filament slots) in the tooltip.

The printer stays in normal Cloud mode — no LAN-only or Developer Mode needed. Status is read from Bambu's cloud MQTT broker with the account token, the same channel the Handy app uses. Talks MQTT 3.1.1 directly (no client library) since the exchange is a single subscribe + pushall.

Run `bambu-ctl login` once (email + password, or an emailed code / 2FA when Bambu asks); the token lands in `~/.config/bambu-cloud.json` and lasts about 3 months — the pill switches to a `reauth` class when a fresh `login` is needed. Accounts created with Google/Apple sign-in have no API password: either set one in the account settings, or run `bambu-ctl login --token` and paste the `token` cookie from a logged-in makerworld.com browser session. `bambu-ctl status` prints the same state human-readably (`--raw` for the full report JSON).

Control goes over the same channel: `bambu-ctl pause` / `resume` / `stop` (stop asks for confirmation, `--yes` skips), `bambu-ctl speed silent|standard|sport|ludicrous`, and `bambu-ctl light on|off` for the chamber light. Commands wait for the printer's acknowledgement on the report topic and report failures (e.g. `bambu-ctl resume` with nothing paused).

## bambu-exporter
A long-running companion to `bambu-ctl` that keeps one MQTT subscription open and serves what it hears, for Prometheus and for the pill.

It exists because the printer rate-limits `pushall` to roughly one a minute, so nothing can fetch per scrape — that limit is why the waybar module polls at 120 s. Holding the subscription open instead means the partial `push_status` messages the printer streams unprompted (measured: one every 1–2 s) become the data source, and any number of consumers can read as often as they like at no cost to the printer.

| Endpoint | Port | For |
|---|---|---|
| `/metrics` | `$BAMBU_METRICS_ADDR`, default `:9090` | Prometheus text, ~43 `bambulab_*` gauges |
| `/state` | `$BAMBU_HTTP_ADDR`, default `:8080` | the merged report as JSON, for `bambu-ctl waybar` |
| `/healthz` | `$BAMBU_HTTP_ADDR` | process liveness |

Two listeners so `/metrics` can stay cluster-internal while `/state` is reachable on the LAN. Config is environment-only (`BAMBU_SESSION_JSON` holds the contents of `~/.config/bambu-cloud.json`; it falls back to the file on disk for local runs), which suits a Kubernetes `envFrom: secretRef`.

Two behaviours worth knowing. A metric is only published when the printer actually reported the field, so a sensor this firmware doesn't have is absent rather than a confident zero. And `/healthz` deliberately ignores data freshness — a powered-off printer is not an unhealthy exporter, and tying them together would restart the pod every time the printer sleeps; use `bambulab_report_age_seconds` for that, which also catches a subscription that has gone deaf while the socket still looks open.

The cloud token lasts about three months and can only be renewed interactively, so `bambulab_cloud_auth_ok` drops to 0 when it is rejected and `bambulab_cloud_token_age_seconds` lets you warn ahead of time. (`bambulab_cloud_token_expiry_timestamp_seconds` is exported too, but only for JWT tokens — the emailed-code login flow mints opaque ones.)
