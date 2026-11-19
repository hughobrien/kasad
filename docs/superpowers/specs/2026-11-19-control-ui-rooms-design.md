# kasad UI: device types, equal cards, dimmer percentage

Follows `2026-11-19-control-ui-design.md`. Dimmer brightness, listed there as a non-goal, is now in scope.

**Rooms were dropped (Hugh, 2026-11-19).** Devices do not know their room (it lives in TP-Link's cloud app), so grouping would need either a hand-maintained list in the nixos config or storage kasad does not have; he preferred neither. The earlier design for it (an ordered `{ name, devices }` list, an "Other" section for unlisted devices) is deleted rather than kept as a half-feature; if grouping comes back, a config list needs no storage.

## Changes

1. **Device type instead of model.** The card's second line says what the device is, not its model number: `Socket`, `Light Switch`, `Light Switch (Dimmable)`. Derived from the model prefix: `KP`, `EP` and `HS1xx` are sockets; `KS200` and `HS200` are light switches; `KS220` and `HS220` are dimmable switches; a device that reports a brightness is dimmable whatever its model. An unrecognised model shows its model string, so nothing is mislabelled.
2. **Equal-size cards.** Every card has the same fixed width and height (sized for the tallest, the dimmer), content top-aligned, so rows line up. Cards without a meter or a dimmer simply have empty space.
3. **Dimmer percentage.** A dimmable card shows an editable number field (1–100, `%`). Committing it (Enter or leaving the field) sets the brightness. The device reports its stored brightness even when off, so the field always shows it; whether setting it while off also switches the light on is checked on a real device before release.
   - Library: `SetLegacyBrightness(ctx, host, pct int) error` sends `{"smartlife.iot.dimmer":{"set_brightness":{"brightness":N}}}` (legacy dimmers; devices coerce 0 to 1, so the field's minimum is 1) and checks `err_code`.
   - Exporter: `SetBrightness(ctx, host, pct int) error`, same shape as `SetRelay` (known host, a dimmer, one retry, immediate re-poll, change notification).
   - Web: `POST /ui/devices/{host}/brightness?pct=N`. Same CSRF check; 400 unless `pct` is a whole number 1–100; 404 for an unknown host; 400 for a device that is not dimmable. Answers with the patched card, like the relay.
   - A live update must not overwrite what is being typed. The field is marked `data-ignore-morph` (comfortd's pattern) and its element id includes the stored brightness, so a changed brightness (from the app, say) replaces the element with a new value while an unchanged one leaves a half-typed entry alone. Verified in a browser.

## Non-goals

Rooms or any grouping, transitions, SMART-protocol dimmers (none on this LAN), LED night-light.

## Testing

Unit tests for the type label (each model family and the fallback), the brightness write (request body, error code), the endpoint (range, non-dimmer, unknown host, CSRF), and card rendering (field present only on dimmers, disabled offline). Layout is checked with a headless-browser screenshot. **No brightness write reaches a real device until Hugh names which dimmer to test.**
