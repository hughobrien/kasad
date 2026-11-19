# kasad control UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A web page at `kasad.wg` showing every Kasa device (state, current power in W, energy today and this month in kWh, RSSI) with an on/off switch on each card, updating live.

**Architecture:** `internal/kasa` gains write calls. `internal/exporter` gains `SetRelay`, a change hub and an exported device view. A new `internal/web` package renders templ cards and serves the page, an SSE stream and the relay POST on the existing listener, next to `/metrics`. Same stack as comfortd and breezyd.

**Tech Stack:** Go 1.27.3, `a-h/templ` 0.3.1118, `starfederation/datastar-go` v1.2.2, datastar JS 1.0.1 (vendored), `prometheus/client_golang` (existing).

**Spec:** `docs/superpowers/specs/2026-11-19-control-ui-design.md`. Read it first. Reference implementation for layout, SSE and datastar wiring: `~/src/comfortd/cmd/comfortd` (`sse.go`, `ui/templates/layout.templ`, `ui/templates/card.templ`, `ui/vendor/`).

## Global Constraints

- Dependencies added: only templ (0.3.1118 through the same `templOverlay` as comfortd, which throws when nixpkgs catches up) and datastar-go v1.2.2. Datastar JS is the same vendored file comfortd ships (`ui/vendor/datastar-1.0.1.min.js` plus its README).
- `go` directive stays `1.27.3`. After any go.mod change, recompute `vendorHash` (set `pkgs.lib.fakeHash`, build on bigthink, paste the `got:` hash).
- Public repo: no real device names, IPs, MACs or screenshots in code, tests or docs. Fixtures are invented (`Lamp`, `10.0.0.1`).
- Commits have no Claude trailer.
- **No write reaches a real device** except Deck LEDs, in Task 5, after Hugh says go. Everything before that uses the fakes in `internal/kasa/kasa_test.go` or stubs.
- `/metrics` and `/healthz` behaviour is unchanged.
- The gate (`checks.gate`) must pass: vet, gofmt (ignoring `vendor/`), tests, race tests, and the templ drift check.

## Review Focus

- A device alias containing `<script>`, quotes or `'` (real aliases include apostrophes): escaped in text and in datastar attributes. Test in Task 3.
- POST for a host kasad does not know, or with `on` missing or not `true`/`false`: 404 / 400 with no device I/O. Task 4.
- A cross-site POST (`Sec-Fetch-Site: cross-site`, or `Origin` differing from `Host`): 403 and no write; a request with neither header (curl) is allowed. Task 4.
- A write that fails or times out, on a device that is down: the card shows the error and the switch is usable again; shown state stays what the device last reported. Tasks 2 and 3.
- Zero devices, or every device down: the page renders (`0 of 0 up`, 0 W); the SSE stream does not hang; a device that expires while a tab is open has its card removed. Tasks 3 and 4. Also: a closed SSE connection releases its hub subscription (Task 2).

---

### Task 1: Write calls and day/month energy in `internal/kasa`

**Files:** Modify `internal/kasa/legacy.go`, `internal/kasa/smart.go`, `internal/kasa/kasa_test.go`

**Interfaces:**
- Produces: `Reading` gains `EnergyTodayWh, EnergyMonthWh *float64`; `PollLegacy(ctx, host string, now time.Time)` takes the clock (used to pick today's and this month's entries); `func SetLegacyRelay(ctx context.Context, host string, on bool) error`; `func (k *KLAP) SetDeviceOn(ctx context.Context, on bool) error`. Both set a state (idempotent), never toggle.

- [ ] **Step 1: Refactor the test fakes.** `fakeLegacy` takes a `func(req string) string` handler (the poll test passes one that checks `legacyPollRequest`); `fakeKLAP` records the last decrypted request body in a field the test can read.
- [ ] **Step 2: Failing tests.** `TestSetLegacyRelaySendsState`: the request is exactly `{"system":{"set_relay_state":{"state":1}}}` for `on=true` and `state":0` for false; reply `{"system":{"set_relay_state":{"err_code":0}}}` gives nil. `TestSetLegacyRelayDeviceError`: reply with `err_code":-1` returns an error. `TestKLAPSetDeviceOn`: the decrypted request has `"method":"set_device_info"` and `"params":{"device_on":true}`; `{"error_code":0}` gives nil, `-1008` gives a `*SmartError`. Run: `go test ./internal/kasa/` fails to compile.
- [ ] **Step 2b: Failing energy tests.** `TestLegacyPollRequestAsksForStats`: the poll request includes `get_daystat` with the clock's `year` and `month` and `get_monthstat` with `year`. `TestParseLegacyDayMonth` (fixture invented, reply carries `day_list` with entries `{year,month,day,energy_wh}` and a `month_list`): for 2026-11-19 gives `EnergyTodayWh` 3 and `EnergyMonthWh` 1185; a day list with no entry for today gives 0 for today; a reply without the stats gives nil for both. `TestKLAPPollEnergy`: the fake's `get_energy_usage` returns `today_energy` 56 and `month_energy` 1185, so both fields are set.
- [ ] **Step 3: Implement.** `SetLegacyRelay` reuses `legacyQuery` and checks `system.set_relay_state.err_code`. Read the new stats in `parseLegacy`/`Poll` (the legacy request is built from the clock at poll time and still goes in one round trip; `exporter` passes `e.now()`). Generalise `smartCall` to `smartCall(ctx, method string, params any)` (omit `params` when nil; update the existing callers) and add `SetDeviceOn` on it. Run: `go test -race ./internal/kasa/` passes.
- [ ] **Step 4: Commit** `kasa: relay write calls`.

### Task 2: Exporter write path, device view and change hub

**Files:** Modify `internal/exporter/exporter.go`, `internal/exporter/exporter_test.go`; Create `internal/exporter/view.go`, `internal/exporter/view_test.go`

**Interfaces:**
- Produces:
  - `type DeviceView struct { Host, Alias, Model string; Up, On, HasState bool; PowerWatts, EnergyTodayKWh, EnergyMonthKWh, RSSI *float64; LastOK time.Time; Err string }` (the energy fields are the device's watt-hours divided by 1000; `Err` is the last failed write's error text, cleared by the next successful poll (a failed poll just shows as offline)).
  - `func (e *Exporter) Devices() []DeviceView`, sorted by alias then host.
  - `func (e *Exporter) SetRelay(ctx context.Context, host string, on bool) error`.
  - `func (e *Exporter) Subscribe() (<-chan struct{}, func())`. The channel has capacity 1 and notifications coalesce; calling the func unsubscribes and stops delivery.
- `Exporter` gets an unexported `backend` struct of funcs (`pollLegacy`, `pollKLAP`, `setLegacy`, `setKLAP`) defaulting to the `kasa` package, so tests can stub it. `pollKind` and `pollOne` use it.

- [ ] **Step 1: Failing tests** (with a stubbed backend): `TestSetRelayLegacy` (calls `setLegacy` with host and state, then the poll runs and `Devices()` shows the polled `On`); `TestSetRelayUnknownHost` (error, backend never called); `TestSetRelayErrorRecorded` (a failing write sets `Err`, a later successful poll clears it); `TestSetRelayNeedsKLAPCreds` (KLAP device, nil creds: error); `TestDevicesView` (kWh conversion of today and month, sort order, nil measurements for a down device); `TestSubscribeNotified` (a poll completing sends on the channel; two quick events leave one pending); `TestUnsubscribe` (no delivery after the returned func is called).
- [ ] **Step 2: Implement** the above. Notify subscribers after every poll result and every write. Writes use the same per-device `PollTimeout`. Run: `go test -race ./internal/exporter/` passes.
- [ ] **Step 3: Remove the plain-text `/` listing** from `Handler` in `metrics.go` (the web package replaces it); keep `/metrics` and `/healthz`. Update the existing tests. Commit `exporter: relay writes, device view, change hub`.

### Task 3: Templates, view model, assets and build wiring

**Files:** Create `internal/web/ui/view.go`, `internal/web/ui/view_test.go`, `internal/web/templates/layout.templ`, `internal/web/templates/card.templ`, `internal/web/templates/render_test.go`, `internal/web/assets.go`, `internal/web/style.css`, `internal/web/favicon.svg`, `internal/web/vendor/datastar-1.0.1.min.js` and `README.md` (copied from comfortd); Modify `go.mod`, `go.sum`, `flake.nix`, `justfile`

**Interfaces:**
- Produces (package `internal/web/ui`, like comfortd's `cmd/comfortd/ui`; `templates` imports it, which avoids an import cycle): `type CardView struct { ID, Host, Name, Model string; Up, On, HasState bool; Power, Today, Month, RSSI, LastSeen, Err string }` and `type PageView struct { Cards []CardView; TotalPower string; UpCount, Total int }`; `func NewCard(d exporter.DeviceView, now time.Time) CardView`; `func NewPage(ds []exporter.DeviceView, now time.Time) PageView`.
  - `ID` is `"device-"` plus the host with `.` replaced by `-`. Display strings are empty when a value is absent: `Power` like `5.6 W`, `Today` and `Month` like `0.003 kWh` and `1.19 kWh` (3 decimals under 1 kWh, 2 under 100, otherwise 1), `RSSI` like `-63 dBm`. `TotalPower` sums `PowerWatts` of up devices, formatted like `305.4 W`.
- Produces (package `internal/web/templates`): `templ Page(p ui.PageView)` and `templ Card(c ui.CardView)`.

- [ ] **Step 1: Failing view tests.** `TestNewCardFormats` (a metered, on, up device gives `5.6 W`, `0.003 kWh`, `1.19 kWh`, `-63 dBm`, ID `device-10-0-0-1`); `TestNewCardUnmetered` (nil power and energy give empty strings); `TestNewCardDown` (Up false, `LastSeen` like `3m ago`); `TestNewPageTotals` (the total counts only up devices; `0 of 0 up` for none).
- [ ] **Step 2: Failing render tests** (render to a string): `TestCardEscapesName` (name `Kid's "<b>Lamp</b>"` appears escaped, no raw `<b>`); `TestCardOnShowsReadings` (power, today, month, RSSI text present; switch `aria-pressed="true"`); `TestCardOfflineDisablesSwitch` (the switch has `disabled`, an `Offline` label, no power text); `TestCardUnmeteredHidesPower`; `TestCardShowsError` (`Err` text present); `TestPageRendersEmpty` (zero cards renders and shows `0 of 0 up`).
- [ ] **Step 3: Implement** `ui` and the templates, following comfortd's `layout.templ` (build-id meta, `data-init` opening `/ui/sse` with the same retry options) and card patterns. The switch posts `@post('/ui/devices/<host>/relay?on=<true|false>')` (the opposite of the current state); use a datastar signal or indicator for the pending style. Generate with `go run github.com/a-h/templ/cmd/templ@v0.3.1118 generate`; commit the `*_templ.go` files. Run: `go test ./internal/web/...` passes.
- [ ] **Step 4: Assets and build wiring.** `go get` templ 0.3.1118 and datastar-go v1.2.2; embed assets in `assets.go` as comfortd does. In `flake.nix` copy comfortd's `templOverlay` (same version, hashes and `throw`), use it for `pkgs`, add `templ` to the dev shell and gate inputs, and add comfortd's templ drift check to the gate. Add `generate` to the `justfile`. Recompute `vendorHash`. Run the gate on bigthink: `nix build` of `checks.x86_64-linux.gate` exits 0 (check the real exit code, not a piped one).
- [ ] **Step 5: Commit** `web: templ cards, view model, assets`.

### Task 4: HTTP handlers, SSE and wiring

**Files:** Create `internal/web/server.go`, `internal/web/server_test.go`, `internal/web/sse.go`, `internal/web/sse_test.go`; Modify `cmd/kasad/main.go`

**Interfaces:**
- Consumes: `exporter.DeviceView`, `Devices()`, `SetRelay`, `Subscribe` (Task 2); `ui.NewPage`, `ui.NewCard`, `templates.Page`, `templates.Card` (Task 3); `ui.NewPage`, `ui.NewCard` live in `internal/web/ui`.
- Produces: `type Devices interface { Devices() []exporter.DeviceView; SetRelay(ctx context.Context, host string, on bool) error; Subscribe() (<-chan struct{}, func()) }` (the Exporter satisfies it; tests use a fake) and `func Handler(d Devices, rest http.Handler) http.Handler`. Routes: `GET /{$}`, `GET /ui/sse`, `POST /ui/devices/{host}/relay`, `GET /ui/static/{style.css,favicon.svg,datastar.js}`; everything else goes to `rest` (the metrics handler).

- [ ] **Step 1: Failing handler tests** (fake `Devices`): `TestIndexRendersCards`; `TestRelayPostCallsSetRelay` (`on=false` calls `SetRelay(host, false)` and answers with an SSE patch of the card, which also clears its pending style); `TestRelayUnknownHost404` and `TestRelayBadOn400` (no `SetRelay` call; the unknown host is detected through `Devices()`); `TestRelayRejectsCrossSite` (any `Sec-Fetch-Site` other than `same-origin` gives 403; with no `Sec-Fetch-Site`, an `Origin` whose host differs from `Host` does too); `TestRelayAllowsNoFetchHeaders` (neither header present is allowed); `TestRelayErrorPatchesCard` (a `SetRelay` error leads to a 200 with an SSE patch of that card carrying the error text; the card is not left pending); `TestStaticAssets`; `TestRestFallsThrough` (`/metrics` reaches `rest`).
- [ ] **Step 2: Failing SSE tests:** `TestSSEInitialCards` (connect, one patch per device); `TestSSEPatchesOnlyChangedCards` (after a notification where one card's display text changed, only that card is patched); `TestSSERemovesExpiredDevice` (a device vanishing from `Devices()` emits a remove for its card); `TestSSEEmptyDoesNotHang` (zero devices: the stream opens and responds to the keepalive); `TestSSEUnsubscribesOnClose` (cancelling the request calls the unsubscribe func); `TestSSEBuildIDScript` (the connect sends the reload-on-mismatch script like comfortd).
- [ ] **Step 3: Implement.** Follow comfortd's `handleSSE`: clear the write deadline, `X-Accel-Buffering: no`, `datastar.NewSSE`, patch cards by selector `#device-...`, a keepalive comment every 30s (a package variable, so tests can shorten it). Keep the last rendered text per card to patch only what changed. Run: `go test -race ./...` passes.
- [ ] **Step 4: Wire `cmd/kasad/main.go`:** `web.Handler(e, e.Handler(version), version)` as the server handler; set `templates.BuildID` from a hash of the running binary, as comfortd does. Run `go vet ./...` and the gate on bigthink; commit `web: handlers, SSE, wiring`.
- [ ] **Step 5: Docs.** Update `README.md` (the UI, `kasad.wg`, the new routes) and `CLAUDE.md` (layout, the new dependencies, `just generate`). Commit `docs: control UI`.

### Task 5: Deploy and first real-device test (needs Hugh)

**Files:** Modify `~/.config/nixos/services/kasad.nix` on bigthink and copy it to `/etc/nixos` (the port already exists in `net-data.nix`)

- [ ] **Step 1: Ask Hugh for the cert pair** `kasad.crt` and `kasad.key` (the `fw` CA is not on bigthink; he supplies a tar as before, e.g. `/tmp/kasad.tar`), review it with him, and install into `/etc/nginx` (owner nginx:root, mode 644 and 600). Stop and wait until he provides it.
- [ ] **Step 2: Config.** Add `services.nginx.virtualHosts."kasad.wg" = net-lib.mkHostProxyPass "kasad" net.lo.ipv4;` to `services/kasad.nix`; `nix flake update kasad`; diff `~/.config/nixos` against `/etc/nixos`, `sudo cp`, `chown -R root:root /etc/nixos`, `doas nixos-rebuild switch`. Verify: `https://kasad.wg/` loads, every card shows state, power, today and month kWh, and RSSI, `/metrics` still scrapes (Prometheus targets up), no failed units.
- [ ] **Step 3: Real write test, Deck LEDs only, after Hugh says go.** Note its current state, switch it off from the page, confirm the card flips from the device's own readback and `kasa_relay_on` changes in Prometheus, switch it back on, confirm again. Report each result. Touch no other device.
- [ ] **Step 4: Cleanup.** Commit and push the nixos repo; kasad repo on `main` and clean; remove temp files (including the cert tar on bigthink and any scripts). Update the `bigthink-monitoring-stack` memory with the UI and `kasad.wg`.
