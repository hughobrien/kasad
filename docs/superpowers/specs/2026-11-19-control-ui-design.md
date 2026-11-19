# kasad control UI design

## Intent

kasad already knows every Kasa plug and switch on the LAN and polls them. Hugh wants a small web page, like comfortd's and breezyd's, that shows each device and lets him switch it on and off. The Kasa phone app does this through TP-Link's cloud; this page does it locally, from `kasad.wg`.

## Goals

- One card per discovered device: name, model, on/off state, a clear offline state, and the readings Hugh asked for: **power** now (W), **historical energy** in kWh, today and this month, as recorded by the device itself, and **signal strength** (RSSI, dBm). Power and energy show only on metered plugs (the per-day and per-month counters come back in the same single request as the live reading, so polling costs no extra round trip); the page header shows the **combined power** of all devices and `n of m up`.
- A switch on every card. Every device is switchable from the start; the `.wg` network and its few authorised clients are the access control (Hugh's decision).
- Updates appear live without reloading, including changes made elsewhere (the phone app, a wall switch).
- Same stack and look as comfortd and breezyd, so the three feel like one family.

## Non-goals (this version)

- Dimmer brightness, LED night-light, rename, schedules, rooms or groups (rooms live in TP-Link's cloud, not on the device), reboot, firmware.
- Cameras and bulbs. Authentication inside kasad.
- Any change to the metrics.

## Anti-goals

- A control that claims success it did not verify: the card must show what the device reports, not what we asked for.
- A page a stranger's website can drive: cross-site requests must be rejected.

## Approaches considered

1. **templ + datastar + SSE** (chosen). What comfortd and breezyd use: server-rendered components, datastar vendored as one JS bundle, one SSE stream that patches cards by id. Costs two new dependencies, both already in use elsewhere.
2. Plain HTML forms with a full page reload. No JS and no new dependencies, but no live updates, and every tap reloads the page.
3. A JS single-page app. Heavier, a build step, and unlike the other projects.

## Design

### Library: `internal/kasa`

- `SetLegacyRelay(ctx, host, on)`: sends `{"system":{"set_relay_state":{"state":0|1}}}` over the existing legacy transport and checks `err_code == 0`.
- `(*KLAP).SetDeviceOn(ctx, on)`: SMART `set_device_info` with `{"device_on": bool}`; a non-zero `error_code` is an error.
- Both are idempotent: they set a state, they do not toggle.
- Reads gain `EnergyTodayWh` and `EnergyMonthWh`: legacy devices answer `get_daystat` and `get_monthstat` in the same request (checked live: entries like `{year, month, day, energy_wh}`); SMART devices report `today_energy` and `month_energy` in `get_energy_usage`. The metrics are unchanged.

### Exporter: `internal/exporter`

- `Exporter.SetRelay(ctx, host, on)` looks the host up in the device table (unknown host = error, so the UI cannot be pointed at arbitrary addresses), writes with the device's protocol, then polls that device immediately.
- A small hub: every poll result and every write publishes "device changed" to subscribers. SSE handlers subscribe.
- The device table also records the last write error per device, shown on its card until the next success.

### Web UI: `cmd/kasad/ui`

- Routes on the existing listener (so `/metrics` stays where it is): `GET /` page, `GET /ui/sse`, `POST /ui/devices/{host}/relay?on=true|false`, static assets (style, favicon, vendored datastar).
- Cards are keyed by host in routes and element ids (`#device-192-168-1-41`). Alias slugs would collide (two devices can share a name); the host is unique and always known. Aliases only appear as text.
- The POST sends the desired state, not a toggle, so a double tap or a retry cannot flip the device back. The button is `aria-pressed`; the card shows a pending style until the next SSE patch reports the device's actual state, or an error line if the write failed.
- SSE: on connect, send every card; afterwards patch only the cards whose rendered output changed (watts jitter, so changes are compared on the displayed text). Keepalive comments every 30s, `X-Accel-Buffering: no`. The page carries a build id; the stream reloads the tab if it differs, like comfortd.
- Order: by name, then host. The header shows the combined draw in watts and `n of m up`.
- Offline devices render dimmed ("Offline", last seen) with the switch disabled.
- CSRF: reject `POST`s whose `Sec-Fetch-Site` is present and not `same-origin`, and whose `Origin` does not match the `Host`, so another website open in an authorised browser cannot toggle things. (My call; neither sibling project is known to do this.)

### Deployment

- `kasad.wg` vhost via `net-lib.mkHostProxyPass "kasad" net.lo.ipv4`, with a cert pair issued by the `fw` CA, as for the other `.wg` names. Hugh supplies the cert tar; the CA key is not on bigthink.
- kasad's listen address does not change.

### Build and dependencies

- Added: `a-h/templ` at the version comfortd pins (0.3.1118, via the same `templOverlay` that throws once nixpkgs catches up) and `starfederation/datastar-go`, plus the same vendored datastar JS file comfortd ships.
- The flake gate gains the templ drift check (generate, diff) like the siblings; `just generate` runs templ.

### Testing and safety

- Unit tests with the existing fake legacy server and fake KLAP device: write calls, error codes, unknown host, desired-state idempotence, SSE patch only on change, CSRF rejection, render tests for card states (on, off, offline, pending, error, unmetered).
- **No write reaches a real device unless Hugh agrees to that specific device in this conversation.** First real check: Hugh chose **Deck LEDs** (the plug at the deck); we toggle it off and back on and confirm it from the device's own readback.
- Public repo: no device names, IPs or screenshots; fixtures are invented.

## Left to the builder

CSS details and card layout within comfortd's look, exact copy, the hub's buffering, test names. The responsive layout is a grid of cards that works on a phone.
