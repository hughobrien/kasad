package kasa

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

const kp125Reply = `{"system":{"get_sysinfo":{"alias":"Lamp","model":"KP125(US)","relay_state":1,"rssi":-60,"on_time":120}},
"emeter":{"get_realtime":{"current_ma":154,"voltage_mv":122598,"power_mw":5535,"total_wh":441,"err_code":0}}}`

const ks200Reply = `{"system":{"get_sysinfo":{"alias":"Switch","model":"KS200(US)","relay_state":0,"rssi":-50,"on_time":0}},
"emeter":{"err_code":-1,"err_msg":"module not support"}}`

func TestParseLegacy(t *testing.T) {
	r, err := parseLegacy([]byte(kp125Reply))
	if err != nil {
		t.Fatal(err)
	}
	if r.Alias != "Lamp" || !r.On || !r.HasState {
		t.Fatalf("info = %+v", r)
	}
	if *r.PowerWatts != 5.535 || *r.VoltageVolts != 122.598 || *r.CurrentAmps != 0.154 || *r.EnergyWh != 441 {
		t.Fatalf("emeter = %v %v %v %v", *r.PowerWatts, *r.VoltageVolts, *r.CurrentAmps, *r.EnergyWh)
	}

	r, err = parseLegacy([]byte(ks200Reply))
	if err != nil {
		t.Fatal(err)
	}
	if r.On || r.PowerWatts != nil || r.VoltageVolts != nil {
		t.Fatalf("switch must have state but no meter: %+v", r)
	}

	if _, err := parseLegacy([]byte(`{"system":{}}`)); err == nil {
		t.Fatal("reply without sysinfo must be an error")
	}
}

func TestParseLegacyOldFirmwareUnits(t *testing.T) {
	r, err := parseLegacy([]byte(`{"system":{"get_sysinfo":{"model":"HS110","relay_state":1}},
"emeter":{"get_realtime":{"current":0.5,"voltage":120.1,"power":60.2,"total":1.5}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if *r.PowerWatts != 60.2 || *r.VoltageVolts != 120.1 || *r.EnergyWh != 1500 {
		t.Fatalf("got %v %v %v", *r.PowerWatts, *r.VoltageVolts, *r.EnergyWh)
	}
}

// fakeLegacy serves one reply on a TCP listener using the real framing.
func fakeLegacy(t *testing.T, reply string) string {
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
				if string(xorDecrypt(body)) != legacyPollRequest {
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
	addr := fakeLegacy(t, kp125Reply)
	host, port, _ := net.SplitHostPort(addr)
	old := legacyPortForTest
	legacyPortForTest = port
	defer func() { legacyPortForTest = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := PollLegacy(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if r.Alias != "Lamp" || *r.PowerWatts != 5.535 {
		t.Fatalf("got %+v", r)
	}
}

// fakeKLAP is a device: KLAP v2 handshake plus SMART methods.
func fakeKLAP(t *testing.T, creds Credentials, methods map[string]any) *httptest.Server {
	t.Helper()
	v2, _ := creds.authHashes()
	var local, remote []byte
	var sess *klapSession
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
	return srv
}

func TestKLAPPoll(t *testing.T) {
	creds := Credentials{Username: "me@example.com", Password: "hunter2"}
	srv := fakeKLAP(t, creds, map[string]any{
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

func TestKLAPWrongPassword(t *testing.T) {
	srv := fakeKLAP(t, Credentials{Username: "a", Password: "b"}, nil)
	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), Credentials{Username: "a", Password: "nope"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := k.Poll(ctx); err != ErrAuth {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestKLAPOptionalEnergyMethods(t *testing.T) {
	creds := Credentials{Username: "a", Password: "b"}
	srv := fakeKLAP(t, creds, map[string]any{
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
