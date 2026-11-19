# kasad design

## Intent
Hugh has about 25 Kasa plugs and switches. He wants their power data (and the rest) in the Prometheus on bigthink, with devices found automatically. A web UI that can switch them is a planned follow-up (like comfortd's and breezyd's), not part of this version.

## Goals / non-goals
- Goals: automatic discovery; power, voltage, current, energy, state and signal metrics; tolerance of flaky Wi-Fi; no third-party dependencies; a NixOS module that registers its own scrape job.
- Non-goals (v1): controlling devices, Tapo cameras and bulbs, history inside kasad, authentication on `/metrics` (it binds loopback).
- Anti-goal: stale numbers presented as current.

## Approaches considered
1. python-kasa wrapped in an exporter: works for everything, but pulls in 18 Python packages for a poller.
2. An existing Go exporter (rebelcore/kasa_exporter): young, unproven, dragging in exporter-toolkit and JWT.
3. **Chosen:** a small Go implementation. The legacy protocol is about 30 lines; KLAP is about 150. Matches the other services (Go, `buildGoModule`).

## Decisions
- Background polling (15s) into a cache; `/metrics` never waits on devices.
- A down device drops its measurements and keeps `kasa_up 0` plus its last-success time.
- Discovery by UDP broadcast on 9999 and 20002, twice per round, every minute; expire after 10 minutes unheard.
- Credentials optional; only newer devices use them. Alias and model are labels; MAC and device id are not (public repo, and they add nothing).
