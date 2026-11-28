package kasa

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"
)

// SmartDiscoveryPort answers for devices that use the newer protocols.
const SmartDiscoveryPort = "20002"

var (
	legacyDiscoveryQuery = xorEncrypt([]byte(`{"system":{"get_sysinfo":{}}}`))
	smartDiscoveryQuery  = []byte{0x02, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0x46, 0x3c, 0xb5, 0xd3}
)

// Found is the result of one discovery round: addresses that answered the
// legacy probe (UDP 9999) and addresses that answered only the newer one
// (UDP 20002).
type Found struct {
	Legacy map[string]bool
	Smart  map[string]bool
}

// Discover broadcasts both probes to every target and collects replies for
// wait. Targets are IPv4 broadcast (or unicast) addresses.
func Discover(ctx context.Context, targets []string, wait time.Duration) (Found, error) {
	f := Found{Legacy: map[string]bool{}, Smart: map[string]bool{}}
	pc, err := net.ListenPacket("udp4", "0.0.0.0:0") // Go sets SO_BROADCAST on UDP sockets
	if err != nil {
		return f, err
	}
	defer pc.Close()

	// Wi-Fi plugs drop broadcasts, so send each probe twice.
	for round := 0; round < 2; round++ {
		for _, t := range targets {
			if net.ParseIP(t) == nil {
				continue
			}
			if p, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(t, LegacyPort)); err == nil {
				_, _ = pc.WriteTo(legacyDiscoveryQuery, p)
			}
			if p, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(t, SmartDiscoveryPort)); err == nil {
				_, _ = pc.WriteTo(smartDiscoveryQuery, p)
			}
		}
		if round == 0 {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return f, nil
			}
		}
	}

	dl := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = pc.SetReadDeadline(dl)
	// Cancellation ends the blocked read at once.
	stop := context.AfterFunc(ctx, func() { _ = pc.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			break // deadline
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		host := ua.IP.String()
		switch ua.Port {
		case 9999:
			if parseLegacyDiscovery(buf[:n]) {
				f.Legacy[host] = true
			}
		case 20002:
			if parseSmartDiscovery(buf[:n]) {
				f.Smart[host] = true
			}
		}
	}
	return f, nil
}

func parseLegacyDiscovery(b []byte) bool {
	r, err := parseLegacy(xorDecrypt(b), time.Now())
	return err == nil && r.Model != ""
}

func parseSmartDiscovery(b []byte) bool {
	if len(b) <= 16 {
		return false
	}
	var r struct {
		ErrorCode int `json:"error_code"`
		Result    *struct {
			DeviceType     string `json:"device_type"`
			DeviceModel    string `json:"device_model"`
			MgtEncryptSchm *struct {
				EncryptType string `json:"encrypt_type"`
			} `json:"mgt_encrypt_schm"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimRight(b[16:], "\x00"), &r); err != nil {
		return false
	}
	if r.ErrorCode != 0 || r.Result == nil {
		return false
	}
	if r.Result.DeviceModel == "" && r.Result.DeviceType == "" {
		return false
	}
	if r.Result.MgtEncryptSchm != nil && r.Result.MgtEncryptSchm.EncryptType != "" {
		et := r.Result.MgtEncryptSchm.EncryptType
		if !strings.EqualFold(et, "KLAP") && !strings.EqualFold(et, "XOR") {
			return false
		}
	}
	if r.Result.DeviceType != "" {
		dt := strings.ToUpper(r.Result.DeviceType)
		if !strings.Contains(dt, "PLUG") && !strings.Contains(dt, "SWITCH") &&
			!strings.Contains(dt, "DIMMER") && !strings.Contains(dt, "STRIP") {
			return false
		}
	}
	return true
}
