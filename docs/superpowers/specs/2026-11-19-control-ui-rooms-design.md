# kasad UI: rooms, device types, equal cards, dimmer percentage

Follows `2026-11-19-control-ui-design.md`. Dimmer brightness, listed there as a non-goal, is now in scope.

## Changes

1. **Device type instead of model.** The card's second line says what the device is, not its model number: `Socket`, `Light Switch`, `Light Switch (Dimmable)`. Derived from the model prefix: `KP`, `EP` and `HS1xx` are sockets; `KS200` and `HS200` are light switches; `KS220` and `HS220` are dimmable switches; a device that reports a brightness is dimmable whatever its model. An unrecognised model shows its model string, so nothing is mislabelled.
2. **Grouped by room.** Rooms are not stored on the devices (they live in TP-Link's cloud app), so kasad gets them from configuration: an ordered list of `{ name, devices }`, matched to devices by name (exact, case-insensitive). Devices not listed fall into a trailing **Other** section, so a renamed or newly added device is visible rather than lost. A device listed in two rooms belongs to the first. Rooms keep the configured order; empty rooms are not shown; cards within a room sort by name.
   - Plumbing: `--rooms <file>` reads a JSON file (`[{"name":"Kitchen","devices":["..."]}]`); the NixOS module has a `rooms` option (list of `{ name; devices; }`) and writes that file. The real names live in bigthink's private nixos config; the public repo only has the mechanism. Without the flag the page is one ungrouped grid, as now.
   - Grouping is display only: it lives in `internal/web/ui` and the exporter does not know about rooms.
3. **Equal-size cards.** Every card has the same fixed width and height (sized for the tallest, the dimmer), content top-aligned, so rows line up across rooms. Cards without a meter or a dimmer simply have empty space.
4. **Dimmer percentage.** A dimmable card shows an editable number field (1–100, `%`). Committing it (Enter or leaving the field) sets the brightness. The device reports its stored brightness even when off, so the field always shows it; whether setting it while off also switches the light on is checked on a real device before release.
   - Library: `SetLegacyBrightness(ctx, host, pct int) error` sends `{"smartlife.iot.dimmer":{"set_brightness":{"brightness":N}}}` (legacy dimmers; devices coerce 0 to 1, so the field's minimum is 1) and checks `err_code`.
   - Exporter: `SetBrightness(ctx, host, pct int) error`, same shape as `SetRelay` (known host, a dimmer, one retry, immediate re-poll, change notification).
   - Web: `POST /ui/devices/{host}/brightness?pct=N`. Same CSRF check; 400 unless `pct` is a whole number 1–100; 404 for an unknown host; 400 for a device that is not dimmable. Answers with the patched card, like the relay.
   - A live update must not overwrite what is being typed: the field keeps its value while it has focus (comfortd's `data-ignore-morph` pattern; verified in a browser).

## Non-goals

Editing rooms from the page, per-room totals or controls, transitions, SMART-protocol dimmers (none on this LAN), LED night-light.

## Testing

Unit tests for the type label (each model family and the fallback), room grouping (order, case-insensitive match, Other, duplicates, empty rooms, no config), the brightness write (request body, error code), the endpoint (range, non-dimmer, unknown host, CSRF), and card rendering (field present only on dimmers, disabled offline). Layout is checked with a headless-browser screenshot. **No brightness write reaches a real device until Hugh names which dimmer to test.**
