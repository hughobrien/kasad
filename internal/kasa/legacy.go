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

const legacyPollRequest = `{"system":{"get_sysinfo":{}},"emeter":{"get_realtime":{}}}`

// PollLegacy reads one device over the legacy protocol.
func PollLegacy(ctx context.Context, host string) (*Reading, error) {
	raw, err := legacyQuery(ctx, host, []byte(legacyPollRequest))
	if err != nil {
		return nil, err
	}
	return parseLegacy(raw)
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

func parseLegacy(raw []byte) (*Reading, error) {
	var r struct {
		System struct {
			Sysinfo *legacySysinfo `json:"get_sysinfo"`
		} `json:"system"`
		Emeter struct {
			Realtime *legacyRealtime `json:"get_realtime"`
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
	return out, nil
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
