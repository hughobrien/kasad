# kasad

A Prometheus exporter and small control page for TP-Link Kasa smart plugs and
switches. It finds devices on the LAN by itself, polls them in the background,
serves the latest readings at `/metrics`, and shows them on a web page where
each one can be switched on and off. Go, with `prometheus/client_golang`,
templ and datastar like comfortd and breezyd.

## Screenshots

The control page (device names replaced with generic ones):

![kasad control page: cards grouped by room, each with a switch, power, energy and signal; a dimmer has a brightness slider](docs/screenshots/ui.png)

A Grafana dashboard built on the metrics (device names blacked out):

![Grafana dashboard: total draw, energy, devices answering, mains voltage, power and energy per plug, and on/off timelines](docs/screenshots/grafana.png)

## What it reads

| Devices | Protocol | Needs a TP-Link login | Metrics |
|---|---|---|---|
| KP125 and other legacy plugs and switches (KS200, KS220, ...) | XOR over TCP 9999 | no | state, signal, uptime; power, voltage, current and energy on metered plugs |
| EP25 and other newer plugs | KLAP over HTTP | yes | the same |

Devices that speak both are polled over the legacy protocol, which needs no
account. Without `--credentials`, KLAP-only devices are skipped.

```
kasa_up{alias,host,model}                     1 if the last poll worked
kasa_last_success_timestamp_seconds
kasa_poll_errors_total
kasa_relay_on
kasa_power_watts  kasa_voltage_volts  kasa_current_amps
kasa_energy_watt_hours_total                  the device's own counter
kasa_on_time_seconds  kasa_brightness_percent  kasa_rssi_dbm
kasad_devices  kasad_build_info
```

Wi-Fi plugs are unreliable, so polling is separate from scraping: `/metrics`
never waits on a device. When a device stops answering, its measurements
disappear (a gap in the graph, not a flat line of stale values), `kasa_up`
goes to 0 and `kasa_last_success_timestamp_seconds` says when it last worked.

## Web UI

`/` is a page with a card per device: on/off switch, current power in W,
energy today and this month in kWh (as recorded by the device itself), and
Wi-Fi signal. Dimmers also have a brightness slider with the percentage beside it. The second line of each card says what the device is (Socket, Light Switch, Light Switch (Dimmable)), and all cards are the same size. The header shows the combined draw and how many devices answer.
It updates live (server-sent events), including changes made from the Kasa app
or a wall switch. A switch sends the state it wants, never a toggle, and the
card then shows what the device reports. Offline devices are dimmed and their
switch is disabled.

There is no login: put it behind something that is already restricted (it is
served at `kasad.wg` on a private network). Cross-site requests to the switch
are refused, so another website open in a browser on that network cannot drive
it.

| Route | |
|---|---|
| `GET /` | the page |
| `GET /ui/sse` | live updates |
| `POST /ui/devices/{host}/relay?on=true\|false` | switch a device |
| `POST /ui/devices/{host}/brightness?pct=1..100` | set a dimmer's brightness (does not switch it on) |
| `GET /metrics`, `GET /healthz` | Prometheus, liveness |

### Rooms and downstream devices

Devices do not know their room or what is plugged into what, so that is
configuration: `--metadata file.json`, or the module's `rooms` and
`downstream` options.

```json
{
  "rooms": [
    {"name": "Kitchen", "devices": ["Kettle", "Fridge"]},
    {"name": "Study", "devices": ["Desk", "Speakers"]}
  ],
  "downstream": {"Speakers": "Desk"}
}
```

Names are the device names from the Kasa app, matched case-insensitively.
Rooms appear in the order listed; devices not listed go in a trailing "Other"
section, and a device named in two rooms stays in the first. `downstream` marks
a device plugged into another metered one: its power is left out of the
header's combined total while the upstream device is up (that reading already
includes it), and its card says "via ...". An unknown key or an unreadable file
stops kasad at startup; names that match no device are logged as a warning
after two minutes. Without the flag the page is one ungrouped grid.

### Firewall

Discovery replies arrive from UDP source ports 9999 and 20002, which a
connection-tracking firewall treats as unsolicited and drops. On such a host
allow them in, for example with nftables `udp sport { 9999, 20002 } accept` on
the LAN interface. The module's `openFirewall` only opens the page's TCP port.

## Running

```
kasad --broadcast 192.168.1.255 --credentials ./creds   # creds: email, then password
curl localhost:9881/metrics
```

| Flag | Default | |
|---|---|---|
| `--listen` | `127.0.0.1:9881` | HTTP address for the page, `/metrics` and `/healthz` |
| `--broadcast` | `255.255.255.255` | comma-separated discovery targets. On a host with several interfaces, name the LAN's broadcast address |
| `--hosts` | | devices to poll even if discovery misses them |
| `--credentials` | | TP-Link account file for newer devices |
| `--poll-interval` | `15s` | |
| `--discover-interval` | `1m` | |
| `--expire` | `10m` | forget a discovered device after this long unheard |

Discovery packets get lost on Wi-Fi, so each probe is sent twice and devices
are only dropped after `--expire`.

## NixOS

```nix
{
  imports = [ inputs.kasad.nixosModules.default ];
  services.kasad = {
    enable = true;
    broadcast = [ "192.168.1.255" ];
    credentialsFile = "/persist/kasad/credentials";
    prometheus.enable = true;   # adds the scrape job when services.prometheus is on
  };
}
```

## Development

`go test ./...`. After editing a `.templ` file run `just generate` (templ 0.3.1118, the version the flake pins) and commit the `*_templ.go` files. The hermetic CI gate is `nix build .#checks.x86_64-linux.gate`
(`just gate` runs it on a remote Nix host).

Licensed GPL-3.0-or-later.
