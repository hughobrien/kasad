// Package kasa speaks TP-Link Kasa's local protocols: the legacy XOR protocol
// on TCP/UDP 9999 and KLAP over HTTP, plus the UDP discovery for both.
package kasa

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// LegacyPort is the TCP and UDP port of the legacy protocol.
const LegacyPort = "9999"

// legacyPortForTest lets tests talk to a fake device on another port.
var legacyPortForTest = LegacyPort

const maxLegacyReply = 1 << 16

// xorEncrypt is TP-Link's autokey XOR with initial key 171.
func xorEncrypt(in []byte) []byte {
	out := make([]byte, len(in))
	key := byte(171)
	for i, c := range in {
		key ^= c
		out[i] = key
	}
	return out
}

func xorDecrypt(in []byte) []byte {
	out := make([]byte, len(in))
	key := byte(171)
	for i, c := range in {
		out[i] = key ^ c
		key = c
	}
	return out
}

// legacyQuery sends one request and returns the decrypted reply. Over TCP
// both directions carry a 4-byte big-endian length prefix.
func legacyQuery(ctx context.Context, host string, req []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, legacyPortForTest))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	buf := make([]byte, 4, 4+len(req))
	binary.BigEndian.PutUint32(buf, uint32(len(req)))
	buf = append(buf, xorEncrypt(req)...)
	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxLegacyReply {
		return nil, fmt.Errorf("legacy reply length %d out of range", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return xorDecrypt(body), nil
}

// legacyPollRequest asks for the sysinfo, the live reading and the device's
// own day and month energy records, all in one round trip.
func legacyPollRequest(now time.Time) []byte {
	return []byte(fmt.Sprintf(`{"system":{"get_sysinfo":{}},"emeter":{"get_realtime":{},"get_daystat":{"month":%d,"year":%d},"get_monthstat":{"year":%d}}}`,
		int(now.Month()), now.Year(), now.Year()))
}

// PollLegacy reads one device over the legacy protocol. now picks which
// day and month of the device's energy records to report.
func PollLegacy(ctx context.Context, host string, now time.Time) (*Reading, error) {
	raw, err := legacyQuery(ctx, host, legacyPollRequest(now))
	if err != nil {
		return nil, err
	}
	return parseLegacy(raw, now)
}

// SetLegacyRelay switches the relay to the given state (it does not toggle).
func SetLegacyRelay(ctx context.Context, host string, on bool) error {
	state := 0
	if on {
		state = 1
	}
	raw, err := legacyQuery(ctx, host, []byte(fmt.Sprintf(`{"system":{"set_relay_state":{"state":%d}}}`, state)))
	if err != nil {
		return err
	}
	var r struct {
		System struct {
			Set *struct {
				ErrCode int `json:"err_code"`
			} `json:"set_relay_state"`
		} `json:"system"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("set_relay_state reply: %w", err)
	}
	if r.System.Set == nil {
		return errors.New("set_relay_state: no result in reply")
	}
	if r.System.Set.ErrCode != 0 {
		return fmt.Errorf("set_relay_state: device error_code %d", r.System.Set.ErrCode)
	}
	return nil
}

// SetLegacyBrightness sets a legacy dimmer's stored brightness (1-100).
// Devices coerce 0 to 1, so 0 is refused here rather than silently changed.
func SetLegacyBrightness(ctx context.Context, host string, pct int) error {
	if pct < 1 || pct > 100 {
		return fmt.Errorf("brightness %d out of range 1-100", pct)
	}
	raw, err := legacyQuery(ctx, host, []byte(fmt.Sprintf(`{"smartlife.iot.dimmer":{"set_brightness":{"brightness":%d}}}`, pct)))
	if err != nil {
		return err
	}
	var r struct {
		Dimmer struct {
			Set *struct {
				ErrCode int `json:"err_code"`
			} `json:"set_brightness"`
		} `json:"smartlife.iot.dimmer"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("set_brightness reply: %w", err)
	}
	if r.Dimmer.Set == nil {
		return errors.New("set_brightness: no result in reply")
	}
	if r.Dimmer.Set.ErrCode != 0 {
		return fmt.Errorf("set_brightness: device error_code %d", r.Dimmer.Set.ErrCode)
	}
	return nil
}

// legacyNum is a JSON number that tells "absent" from zero.
type legacyNum = *float64

type legacySysinfo struct {
	Alias      string    `json:"alias"`
	Model      string    `json:"model"`
	RelayState *int      `json:"relay_state"`
	RSSI       legacyNum `json:"rssi"`
	OnTime     legacyNum `json:"on_time"`
	Brightness legacyNum `json:"brightness"`
}

type legacyRealtime struct {
	// Newer firmware (KP125, ...) reports integer milli-units.
	CurrentMA legacyNum `json:"current_ma"`
	VoltageMV legacyNum `json:"voltage_mv"`
	PowerMW   legacyNum `json:"power_mw"`
	TotalWh   legacyNum `json:"total_wh"`
	// Older firmware (HS110, ...) reports float base units.
	Current legacyNum `json:"current"`
	Voltage legacyNum `json:"voltage"`
	Power   legacyNum `json:"power"`
	Total   legacyNum `json:"total"`
}

// legacyStat is one entry of the device's per-day or per-month record.
type legacyStat struct {
	Year     int      `json:"year"`
	Month    int      `json:"month"`
	Day      int      `json:"day"`
	EnergyWh *float64 `json:"energy_wh"`
	Energy   *float64 `json:"energy"` // older firmware: kWh
}

func (s legacyStat) wh() float64 {
	if s.EnergyWh != nil {
		return *s.EnergyWh
	}
	if s.Energy != nil {
		return *s.Energy * 1000
	}
	return 0
}

func parseLegacy(raw []byte, now time.Time) (*Reading, error) {
	var r struct {
		System struct {
			Sysinfo *legacySysinfo `json:"get_sysinfo"`
		} `json:"system"`
		Emeter struct {
			Realtime *legacyRealtime `json:"get_realtime"`
			Daystat  *struct {
				List []legacyStat `json:"day_list"`
			} `json:"get_daystat"`
			Monthstat *struct {
				List []legacyStat `json:"month_list"`
			} `json:"get_monthstat"`
		} `json:"emeter"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("legacy reply: %w", err)
	}
	si := r.System.Sysinfo
	if si == nil || si.Model == "" {
		return nil, errors.New("legacy reply has no sysinfo")
	}
	out := &Reading{
		Alias:         si.Alias,
		Model:         si.Model,
		RSSI:          si.RSSI,
		OnTimeSeconds: si.OnTime,
		Brightness:    si.Brightness,
	}
	if si.RelayState != nil {
		out.On = *si.RelayState != 0
		out.HasState = true
	}
	if rt := r.Emeter.Realtime; rt != nil {
		out.PowerWatts = pick(scale(rt.PowerMW, 1e-3), rt.Power)
		out.VoltageVolts = pick(scale(rt.VoltageMV, 1e-3), rt.Voltage)
		out.CurrentAmps = pick(scale(rt.CurrentMA, 1e-3), rt.Current)
		out.EnergyWh = pick(rt.TotalWh, scale(rt.Total, 1000))
	}
	// A device that answered the stats but has no entry for today (or this
	// month) has recorded nothing yet, which is 0, not "unknown".
	if ds := r.Emeter.Daystat; ds != nil {
		v := 0.0
		for _, e := range ds.List {
			if e.Year == now.Year() && e.Month == int(now.Month()) && e.Day == now.Day() {
				v = e.wh()
			}
		}
		out.EnergyTodayWh = &v
	}
	if ms := r.Emeter.Monthstat; ms != nil {
		v := 0.0
		for _, e := range ms.List {
			if e.Year == now.Year() && e.Month == int(now.Month()) {
				v = e.wh()
			}
		}
		out.EnergyMonthWh = &v
	}
	return out.settle(), nil
}

func scale(v *float64, f float64) *float64 {
	if v == nil {
		return nil
	}
	x := *v * f
	return &x
}

func pick(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

// timeoutCtx is a small helper for per-call deadlines.
func timeoutCtx(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
