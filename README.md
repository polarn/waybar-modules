# waybar-modules
Just some personal Waybar modules written in GO. Just to learn some GO.

Currently you can use `make` to build, the binary will be in the `build` folder.

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
