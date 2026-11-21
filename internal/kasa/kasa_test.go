package kasa

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestXORRoundTrip(t *testing.T) {
	in := []byte(`{"system":{"get_sysinfo":{}}}`)
	if got := xorDecrypt(xorEncrypt(in)); !bytes.Equal(got, in) {
		t.Fatalf("round trip = %q", got)
	}
	// Known vector: the first byte is 171 ^ '{'.
	if xorEncrypt([]byte("{"))[0] != 171^'{' {
		t.Fatal("first byte wrong")
	}
}

var testNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

const kp125Reply = `{"system":{"get_sysinfo":{"alias":"Lamp","model":"KP125(US)","relay_state":1,"rssi":-60,"on_time":120}},
"emeter":{"get_realtime":{"current_ma":154,"voltage_mv":122598,"power_mw":5535,"total_wh":441,"err_code":0}}}`

const ks200Reply = `{"system":{"get_sysinfo":{"alias":"Switch","model":"KS200(US)","relay_state":0,"rssi":-50,"on_time":0}},
"emeter":{"err_code":-1,"err_msg":"module not support"}}`

func TestParseLegacy(t *testing.T) {
	r, err := parseLegacy([]byte(kp125Reply), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.Alias != "Lamp" || !r.On || !r.HasState {
		t.Fatalf("info = %+v", r)
	}
	if *r.PowerWatts != 5.535 || *r.VoltageVolts != 122.598 || *r.CurrentAmps != 0.154 || *r.EnergyWh != 441 {
		t.Fatalf("emeter = %v %v %v %v", *r.PowerWatts, *r.VoltageVolts, *r.CurrentAmps, *r.EnergyWh)
	}

	r, err = parseLegacy([]byte(ks200Reply), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.On || r.PowerWatts != nil || r.VoltageVolts != nil {
		t.Fatalf("switch must have state but no meter: %+v", r)
	}

	if _, err := parseLegacy([]byte(`{"system":{}}`), testNow); err == nil {
		t.Fatal("reply without sysinfo must be an error")
	}
}

func TestParseLegacyOldFirmwareUnits(t *testing.T) {
	r, err := parseLegacy([]byte(`{"system":{"get_sysinfo":{"model":"HS110","relay_state":1}},
"emeter":{"get_realtime":{"current":0.5,"voltage":120.1,"power":60.2,"total":1.5}}}`), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if *r.PowerWatts != 60.2 || *r.VoltageVolts != 120.1 || *r.EnergyWh != 1500 {
		t.Fatalf("got %v %v %v", *r.PowerWatts, *r.VoltageVolts, *r.EnergyWh)
	}
}

// fakeLegacy serves one reply on a TCP listener using the real framing.
func fakeLegacy(t *testing.T, handler func(req string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var hdr [4]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint32(hdr[:]))
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
				reply := handler(string(xorDecrypt(body)))
				if reply == "" {
					return
				}
				enc := xorEncrypt([]byte(reply))
				out := make([]byte, 4, 4+len(enc))
				binary.BigEndian.PutUint32(out, uint32(len(enc)))
				_, _ = c.Write(append(out, enc...))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestLegacyQueryFraming(t *testing.T) {
	addr := fakeLegacy(t, func(req string) string {
		if req != string(legacyPollRequest(testNow)) {
			return ""
		}
		return kp125Reply
	})
	host, port, _ := net.SplitHostPort(addr)
	old := legacyPortForTest
	legacyPortForTest = port
	defer func() { legacyPortForTest = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := PollLegacy(ctx, host, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.Alias != "Lamp" || *r.PowerWatts != 5.535 {
		t.Fatalf("got %+v", r)
	}
}

// fakeKLAP is a device: KLAP v2 handshake plus SMART methods.
type requestLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *requestLog) add(b string) { l.mu.Lock(); l.bodies = append(l.bodies, b); l.mu.Unlock() }

func (l *requestLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.bodies) == 0 {
		return ""
	}
	return l.bodies[len(l.bodies)-1]
}

func fakeKLAP(t *testing.T, creds Credentials, methods map[string]any) (*httptest.Server, *requestLog) {
	t.Helper()
	v2, _ := creds.authHashes()
	var local, remote []byte
	var sess *klapSession
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/app/handshake1":
			local = body
			remote = bytes.Repeat([]byte{7}, 16)
			http.SetCookie(w, &http.Cookie{Name: "TP_SESSIONID", Value: "abc"})
			_, _ = w.Write(append(remote, sha256sum(local, remote, v2)...))
		case "/app/handshake2":
			if !bytes.Equal(body, sha256sum(remote, local, v2)) {
				http.Error(w, "bad", http.StatusForbidden)
				return
			}
			sess = newKlapSession(local, remote, v2)
		case "/app/request":
			if _, err := r.Cookie("TP_SESSIONID"); err != nil {
				http.Error(w, "no cookie", http.StatusForbidden)
				return
			}
			seq, _ := strconv.Atoi(r.URL.Query().Get("seq"))
			iv := append(append([]byte{}, sess.iv...), seqBytes(int32(seq))...)
			if !bytes.Equal(body[:32], sha256sum(sess.sig, seqBytes(int32(seq)), body[32:])) {
				http.Error(w, "bad signature", http.StatusForbidden)
				return
			}
			blk, _ := aes.NewCipher(sess.key)
			pt := make([]byte, len(body)-32)
			cipher.NewCBCDecrypter(blk, iv).CryptBlocks(pt, body[32:])
			pt = pt[:len(pt)-int(pt[len(pt)-1])]
			log.add(string(pt))
			var req struct{ Method string }
			_ = json.Unmarshal(pt, &req)
			res, ok := methods[req.Method]
			env := map[string]any{"error_code": 0, "result": res}
			if !ok {
				env = map[string]any{"error_code": -1008}
			}
			out, _ := json.Marshal(env)
			pad := aes.BlockSize - len(out)%aes.BlockSize
			out = append(out, bytes.Repeat([]byte{byte(pad)}, pad)...)
			ct := make([]byte, len(out))
			cipher.NewCBCEncrypter(blk, iv).CryptBlocks(ct, out)
			_, _ = w.Write(append(sha256sum(sess.sig, seqBytes(int32(seq)), ct), ct...))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func TestKLAPPoll(t *testing.T) {
	creds := Credentials{Username: "me@example.com", Password: "hunter2"}
	srv, _ := fakeKLAP(t, creds, map[string]any{
		"get_device_info": map[string]any{
			"model": "EP25", "device_on": true, "on_time": 99, "rssi": -55,
			"nickname": base64.StdEncoding.EncodeToString([]byte("Fridge")),
		},
		"get_energy_usage": map[string]any{"current_power": 3225},
		"get_emeter_data":  map[string]any{"power_mw": 3225, "voltage_mv": 122513, "current_ma": 60, "energy_wh": 1234},
	})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two polls: the second reuses the session (sequence number advances).
	for i := 0; i < 2; i++ {
		r, err := k.Poll(ctx)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if r.Alias != "Fridge" || !r.On || *r.PowerWatts != 3.225 || *r.VoltageVolts != 122.513 || *r.EnergyWh != 1234 {
			t.Fatalf("poll %d: %+v", i, r)
		}
	}
}

func TestKLAPBadNicknameBytes(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv, _ := fakeKLAP(t, creds, map[string]any{
		"get_device_info": map[string]any{
			"model": "EP25", "nickname": base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 'L'}),
		},
	})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := k.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(r.Alias) || r.Alias == "" {
		t.Fatalf("alias %q", r.Alias)
	}
}

func TestKLAPWrongPassword(t *testing.T) {
	srv, _ := fakeKLAP(t, Credentials{Username: "a", Password: "b"}, nil)
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), Credentials{Username: "a", Password: "nope"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := k.Poll(ctx); err != ErrAuth {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestKLAPOptionalEnergyMethods(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv, _ := fakeKLAP(t, creds, map[string]any{
		"get_device_info": map[string]any{"model": "P100", "device_on": false},
	})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := k.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.PowerWatts != nil || r.On {
		t.Fatalf("unmetered plug: %+v", r)
	}
}

func TestSetLegacyRelaySendsState(t *testing.T) {
	for _, tc := range []struct {
		on   bool
		want string
	}{
		{true, `{"system":{"set_relay_state":{"state":1}}}`},
		{false, `{"system":{"set_relay_state":{"state":0}}}`},
	} {
		var got string
		addr := fakeLegacy(t, func(req string) string {
			got = req
			return `{"system":{"set_relay_state":{"err_code":0}}}`
		})
		host, port, _ := net.SplitHostPort(addr)
		old := legacyPortForTest
		legacyPortForTest = port
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := SetLegacyRelay(ctx, host, tc.on)
		cancel()
		legacyPortForTest = old
		if err != nil || got != tc.want {
			t.Fatalf("on=%v: err=%v request=%q", tc.on, err, got)
		}
	}
}

func TestSetLegacyRelayDeviceError(t *testing.T) {
	addr := fakeLegacy(t, func(string) string { return `{"system":{"set_relay_state":{"err_code":-1}}}` })
	host, port, _ := net.SplitHostPort(addr)
	old := legacyPortForTest
	legacyPortForTest = port
	defer func() { legacyPortForTest = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := SetLegacyRelay(ctx, host, true); err == nil {
		t.Fatal("err_code -1 must be an error")
	}
}

func TestKLAPSetDeviceOn(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv, log := fakeKLAP(t, creds, map[string]any{"set_device_info": map[string]any{}})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := k.SetDeviceOn(ctx, true); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Method string
		Params map[string]any
	}
	if err := json.Unmarshal([]byte(log.last()), &req); err != nil {
		t.Fatal(err)
	}
	if req.Method != "set_device_info" || req.Params["device_on"] != true {
		t.Fatalf("request = %s", log.last())
	}

	// An unknown method makes the fake answer -1008: surfaced as *SmartError.
	srv2, _ := fakeKLAP(t, creds, nil)
	k2 := NewKLAP(strings.TrimPrefix(srv2.URL, "http://"), creds)
	var se *SmartError
	if err := k2.SetDeviceOn(ctx, false); !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SmartError", err)
	}
}

func TestLegacyPollRequestAsksForStats(t *testing.T) {
	var req struct {
		Emeter map[string]map[string]int `json:"emeter"`
	}
	if err := json.Unmarshal(legacyPollRequest(testNow), &req); err != nil {
		t.Fatal(err)
	}
	if req.Emeter["get_daystat"]["year"] != 2026 || req.Emeter["get_daystat"]["month"] != 11 || req.Emeter["get_monthstat"]["year"] != 2026 {
		t.Fatalf("request = %s", legacyPollRequest(testNow))
	}
	if !strings.Contains(string(legacyPollRequest(testNow)), `"time":{"get_time":{}}`) {
		t.Fatalf("device time not asked for: %s", legacyPollRequest(testNow))
	}
	if _, ok := req.Emeter["get_realtime"]; !ok {
		t.Fatal("realtime reading dropped")
	}
}

const statsReply = `{"system":{"get_sysinfo":{"alias":"Lamp","model":"KP125(US)","relay_state":1}},
"emeter":{"get_realtime":{"power_mw":5000},
"get_daystat":{"day_list":[{"year":2026,"month":11,"day":18,"energy_wh":56},{"year":2026,"month":11,"day":19,"energy_wh":3}]},
"get_monthstat":{"month_list":[{"year":2026,"month":10,"energy_wh":1410},{"year":2026,"month":11,"energy_wh":1185}]}}}`

func TestParseLegacyDayMonth(t *testing.T) {
	r, err := parseLegacy([]byte(statsReply), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.EnergyTodayWh == nil || *r.EnergyTodayWh != 3 || r.EnergyMonthWh == nil || *r.EnergyMonthWh != 1185 {
		t.Fatalf("today=%v month=%v", r.EnergyTodayWh, r.EnergyMonthWh)
	}

	// A device that does not answer the stats has neither.
	r, _ = parseLegacy([]byte(kp125Reply), testNow)
	if r.EnergyTodayWh != nil || r.EnergyMonthWh != nil {
		t.Fatalf("no stats in reply: %v %v", r.EnergyTodayWh, r.EnergyMonthWh)
	}
}

func TestKLAPPollEnergy(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv, _ := fakeKLAP(t, creds, map[string]any{
		"get_device_info":  map[string]any{"model": "EP25"},
		"get_energy_usage": map[string]any{"current_power": 3225, "today_energy": 56, "month_energy": 1185},
	})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := k.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.EnergyTodayWh == nil || *r.EnergyTodayWh != 56 || r.EnergyMonthWh == nil || *r.EnergyMonthWh != 1185 {
		t.Fatalf("today=%v month=%v", r.EnergyTodayWh, r.EnergyMonthWh)
	}
}

func TestOffRelayMeansZeroPowerAndCurrent(t *testing.T) {
	// A plug's meter lags the relay for a moment after switching off; with the
	// relay open the draw is 0 by definition, so do not report the stale figure.
	off := strings.Replace(kp125Reply, `"relay_state":1`, `"relay_state":0`, 1)
	r, err := parseLegacy([]byte(off), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if *r.PowerWatts != 0 || *r.CurrentAmps != 0 {
		t.Fatalf("off: power %v current %v, want 0", *r.PowerWatts, *r.CurrentAmps)
	}
	if *r.VoltageVolts != 122.598 {
		t.Fatalf("mains voltage is still there with the relay open: %v", *r.VoltageVolts)
	}
	on, _ := parseLegacy([]byte(kp125Reply), testNow)
	if *on.PowerWatts != 5.535 {
		t.Fatalf("on must be untouched: %v", *on.PowerWatts)
	}
}

func TestKLAPOffRelayMeansZeroPower(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv, _ := fakeKLAP(t, creds, map[string]any{
		"get_device_info": map[string]any{"model": "EP25", "device_on": false},
		"get_emeter_data": map[string]any{"power_mw": 3225, "current_ma": 60, "voltage_mv": 122000},
	})
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := k.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *r.PowerWatts != 0 || *r.CurrentAmps != 0 {
		t.Fatalf("off: power %v current %v", *r.PowerWatts, *r.CurrentAmps)
	}
}

func TestSetLegacyBrightnessSendsRequest(t *testing.T) {
	var got string
	addr := fakeLegacy(t, func(req string) string {
		got = req
		return `{"smartlife.iot.dimmer":{"set_brightness":{"err_code":0}}}`
	})
	host, port, _ := net.SplitHostPort(addr)
	old := legacyPortForTest
	legacyPortForTest = port
	defer func() { legacyPortForTest = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := SetLegacyBrightness(ctx, host, 42); err != nil {
		t.Fatal(err)
	}
	if want := `{"smartlife.iot.dimmer":{"set_brightness":{"brightness":42}}}`; got != want {
		t.Fatalf("request = %s, want %s", got, want)
	}
}

func TestSetLegacyBrightnessErrors(t *testing.T) {
	addr := fakeLegacy(t, func(string) string { return `{"smartlife.iot.dimmer":{"set_brightness":{"err_code":-1}}}` })
	host, port, _ := net.SplitHostPort(addr)
	old := legacyPortForTest
	legacyPortForTest = port
	defer func() { legacyPortForTest = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := SetLegacyBrightness(ctx, host, 42); err == nil {
		t.Fatal("err_code -1 must be an error")
	}
	for _, bad := range []int{0, -5, 101} {
		if err := SetLegacyBrightness(ctx, host, bad); err == nil {
			t.Errorf("%d%% must be refused before it reaches the device", bad)
		}
	}
}

func TestParseLegacyStatPicking(t *testing.T) {
	day := func(d, wh int) string {
		return fmt.Sprintf(`{"year":2026,"month":11,"day":%d,"energy_wh":%d}`, d, wh)
	}
	mon := func(m, wh int) string {
		return fmt.Sprintf(`{"year":2026,"month":%d,"energy_wh":%d}`, m, wh)
	}
	reply := func(devTime, daystat, monthstat string) []byte {
		tm := ""
		if devTime != "" {
			tm = `"time":{"get_time":` + devTime + `},`
		}
		return []byte(`{"system":{"get_sysinfo":{"alias":"Lamp","model":"KP125(US)","relay_state":1}},` + tm + `
"emeter":{"get_realtime":{"power_mw":5000},"get_daystat":` + daystat + `,"get_monthstat":` + monthstat + `}}`)
	}
	at := func(y, m, d int) string {
		return fmt.Sprintf(`{"err_code":0,"year":%d,"month":%d,"mday":%d,"hour":0}`, y, m, d)
	}
	ok := `{"err_code":0,"day_list":[` + day(18, 56) + `,` + day(19, 3) + `]}`
	okm := `{"err_code":0,"month_list":[` + mon(10, 1410) + `,` + mon(11, 1185) + `]}`
	none, nonem := `{"err_code":0,"day_list":[]}`, `{"err_code":0,"month_list":[]}`
	server := func(m, d, h int) time.Time { return time.Date(2026, time.Month(m), d, h, 0, 0, 0, time.UTC) }
	tests := []struct {
		name                 string
		devTime, daystat, ms string
		now                  time.Time
		today, month         *float64
	}{
		// Without get_time the server's date is the reference.
		{"exact match", "", ok, okm, testNow, f64(3), f64(1185)},
		{"no device time, no entry for today yet", "", ok, okm, server(11, 20, 12), f64(0), f64(1185)},
		{"empty lists", "", none, nonem, testNow, f64(0), f64(0)},
		// With it, the device's date is.
		{"device time agrees", at(2026, 11, 19), ok, okm, testNow, f64(3), f64(1185)},
		{"idle device, correct clock, nothing yet today", at(2026, 11, 20), ok, okm, server(11, 20, 12), f64(0), f64(1185)},
		{"device a day behind across midnight", at(2026, 11, 19), ok, okm, server(11, 20, 0), f64(3), f64(1185)},
		{"device a day ahead across midnight", at(2026, 11, 19), ok, okm, server(11, 18, 23), f64(3), f64(1185)},
		// The device is still in the previous month: the day list the server
		// asked for (December, here empty) is not its month, so today is
		// unknown; the month figure is still its November entry.
		{"device in previous month", at(2026, 11, 30), none, okm, server(12, 1, 0), nil, f64(1185)},
		// Different year: both lists are for the wrong period.
		{"device in previous year", at(2026, 12, 31), none, nonem, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil},
		{"device clock weeks behind", at(2026, 10, 5), ok, okm, testNow, nil, f64(1410)},
		// A refused or missing get_time falls back to the server's date.
		{"get_time refused", `{"err_code":-1}`, ok, okm, testNow, f64(3), f64(1185)},
		{"get_time refused, no entry for today", `{"err_code":-1}`, ok, okm, server(11, 20, 12), f64(0), f64(1185)},
		// A refused stats call is unknown, whatever the rest says.
		{"daystat refused", at(2026, 11, 19), `{"err_code":-1}`, okm, testNow, nil, f64(1185)},
		{"monthstat refused", at(2026, 11, 19), ok, `{"err_code":-1,"err_msg":"module not support"}`, testNow, f64(3), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseLegacy(reply(tc.devTime, tc.daystat, tc.ms), tc.now)
			if err != nil {
				t.Fatal(err)
			}
			if r.PowerWatts == nil || *r.PowerWatts != 5 || r.Alias != "Lamp" {
				t.Fatalf("other fields lost: %+v", r)
			}
			for _, c := range []struct {
				what      string
				got, want *float64
			}{{"today", r.EnergyTodayWh, tc.today}, {"month", r.EnergyMonthWh, tc.month}} {
				if (c.got == nil) != (c.want == nil) || (c.got != nil && *c.got != *c.want) {
					t.Errorf("%s = %v, want %v", c.what, deref(c.got), deref(c.want))
				}
			}
		})
	}
}

func f64(v float64) *float64 { return &v }

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// signedReply builds a reply the way a device does: the request's sequence
// number, CBC under the request's IV, signature over the ciphertext.
func signedReply(s *klapSession, plain []byte) []byte {
	blk, _ := aes.NewCipher(s.key)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(blk, s.lastIV).CryptBlocks(ct, plain)
	return append(sha256sum(s.sig, seqBytes(s.seq), ct), ct...)
}

func TestKLAPDecryptVerifies(t *testing.T) {
	newSess := func() *klapSession {
		s := newKlapSession(bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16), bytes.Repeat([]byte{3}, 32))
		s.encrypt([]byte("request"))
		return s
	}
	good := append([]byte(`{"error_code":0}`), bytes.Repeat([]byte{16}, 16)...)

	s := newSess()
	out, err := s.decrypt(signedReply(s, good))
	if err != nil || string(out) != `{"error_code":0}` {
		t.Fatalf("good reply: %q, %v", out, err)
	}

	s = newSess()
	r := signedReply(s, good)
	r[0] ^= 1
	if _, err := s.decrypt(r); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("flipped signature byte: err = %v", err)
	}

	s = newSess()
	r = signedReply(s, good)
	r[40] ^= 1
	if _, err := s.decrypt(r); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("flipped ciphertext byte: err = %v", err)
	}

	// Validly signed, but the padding is wrong in an earlier pad byte.
	s = newSess()
	bad := append([]byte(`{"error_code":0}`), 1)
	bad = append(bad, bytes.Repeat([]byte{15}, 14)...)
	bad = append(bad, 15)
	bad[len(bad)-3] = 9
	if _, err := s.decrypt(signedReply(s, bad)); err == nil || !strings.Contains(err.Error(), "padding") {
		t.Errorf("bad padding: err = %v", err)
	}
}
