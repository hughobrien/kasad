# kasad

A Prometheus exporter for TP-Link Kasa smart plugs and switches. It finds
devices on the LAN by itself, polls them in the background and serves the
latest readings at `/metrics`. Standard library only, no dependencies.

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

## Running

```
kasad --broadcast 192.168.1.255 --credentials ./creds   # creds: email, then password
curl localhost:9881/metrics
```

| Flag | Default | |
|---|---|---|
| `--listen` | `127.0.0.1:9881` | HTTP address; `/` lists devices, `/healthz` for liveness |
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

`go test ./...`. The hermetic CI gate is `nix build .#checks.x86_64-linux.gate`
(`just gate` runs it on a remote Nix host).

Licensed GPL-3.0-or-later.
