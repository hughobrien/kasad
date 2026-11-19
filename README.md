# kasad

A Prometheus exporter and small control page for TP-Link Kasa smart plugs and
switches. It finds devices on the LAN by itself, polls them in the background,
serves the latest readings at `/metrics`, and shows them on a web page where
each one can be switched on and off. Go, with `prometheus/client_golang`,
templ and datastar like comfortd and breezyd.

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
Wi-Fi signal. The header shows the combined draw and how many devices answer.
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
| `GET /metrics`, `GET /healthz` | Prometheus, liveness |

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
