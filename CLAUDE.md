# kasad — notes for agents

Prometheus exporter and control page for TP-Link Kasa plugs and switches, running on bigthink (Hugh's NixOS server).

## Hard rules

- **This repo is public.** No device names, IPs, MACs, serials or account details in commits. Test fixtures are invented. The only screenshots are in `docs/screenshots/`, with every device name replaced or blacked out; check any new one for names, room names and IPs before committing.
- **The TP-Link login** is the two-line file `/tmp/kasa` on the Mac (email, password) and `/persist/kasad/credentials` on bigthink. Never print, log or commit it.
- **No command reaches a real device unless Hugh agreed to that specific change in this conversation.** Polling is read-only; the UI's switch is a write. Tests use the fakes in `internal/kasa/kasa_test.go` and the stubs in `internal/exporter` and `internal/web`.
- Dependencies: `prometheus/client_golang`, `a-h/templ` (0.3.1118, via `templOverlay` in `flake.nix`, which throws once nixpkgs catches up) and `starfederation/datastar-go`, all matching comfortd and breezyd, plus the vendored datastar JS (`internal/web/vendor`). Ask before adding more. After a go.mod change, recompute `vendorHash` (set `pkgs.lib.fakeHash`, build on bigthink, paste the `got:` hash).

## Layout

- `internal/kasa`: the wire protocols. `legacy.go` (XOR, TCP 9999), `klap.go` (KLAP v1/v2 handshake and AES session), `smart.go` (SMART-protocol methods over KLAP), `discover.go` (UDP 9999 and 20002).
- `internal/exporter`: device table, background discovery and polling, `SetRelay`, the change hub (`Subscribe`), `Devices()` view, the push (each successful poll queued per target in `promwrite.Pushers`, flushed after each round) and the Prometheus collector (every reading; pushed ones labelled `pushed_<target>="true"`).
- `internal/promwrite`: remote write 1.0 by hand; the same file is in emporiad, comfortd and breezyd, keep them in step.
- `internal/web`: the page. `ui/` formats readings, `templates/` is templ (`just generate` after edits; commit the `*_templ.go`), `server.go` routes and the CSRF check, `sse.go` the live stream (sends only what changed), `vendor/` datastar.
- `grafana/kasad.json`: importable dashboard (datasource is the `DS_PROMETHEUS` input). `internal/exporter/dashboard_test.go` fails if it uses a metric the exporter no longer exports. It is exported from a live Grafana; keep device names and UIDs out of it.
- `cmd/kasad`: flags and wiring. `nix/module.nix`: the NixOS module.

## Things learned

- A device that answers the UDP 20002 probe is not necessarily KLAP-only: switches answer it but speak legacy over TCP 9999. Only a legacy UDP reply pins the protocol; otherwise the poller tries legacy, then KLAP.
- Discovery is lossy on Wi-Fi: probes are sent twice and devices expire after 10 minutes without a discovery reply or a good poll. Discovery runs every 15 minutes (Hugh's call, 2026-11-19); polls keep known devices alive.
- Readings are pushed as well as scraped: a 15 s poll and a 60 s scrape threw away three readings in four. `/metrics` still has them all for any scraper; the `pushed_<name>` label lets a target's own scrape drop its copies (the module adds the rule). bigthink's store is VictoriaMetrics; a Prometheus target refuses samples older than about an hour and a repeated `--web.enable-remote-write-receiver` flag (so the host sets it once; the module asserts it).
- `internal/promwrite` is identical in all four daemons (SPDX header aside); change the copies together.
- KLAP: handshake1 tries the v2 credential hash, then v1. The `TIMEOUT` cookie sets session life.

## Commands

`go test -race ./...`; `just generate` after editing `.templ`; `just gate` runs the nix gate on bigthink (the Mac has no nix).
