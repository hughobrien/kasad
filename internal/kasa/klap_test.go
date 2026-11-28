package kasa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKLAPOwnTransport(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	creds := Credentials{Username: "a", Password: "b"}
	srv, _ := fakeKLAP(t, creds, nil)
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	k, k2 := NewKLAP(host, creds), NewKLAP(host, creds)
	for _, c := range []*KLAP{k, k2} {
		if err := c.handshake(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tr, ok := k.client.Transport.(*http.Transport)
	if !ok || k.client.Transport == http.DefaultTransport {
		t.Fatalf("transport is %T, want a private *http.Transport", k.client.Transport)
	}
	if tr.Proxy != nil {
		t.Error("transport consults proxy settings")
	}
	if k2.client.Transport == k.client.Transport {
		t.Error("two sessions share a transport")
	}
}

func TestKLAPSessionLifetime(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"TP_SESSIONID=abc;TIMEOUT=1440", 1420 * time.Second},
		{"TP_SESSIONID=abc; TIMEOUT=60", 40 * time.Second},
		{"TIMEOUT=300", 280 * time.Second},
		{"TP_SESSIONID=abc;TIMEOUT=10", 10 * time.Second},
		{"TP_SESSIONID=abc;TIMEOUT=abc", 24*time.Hour - 20*time.Second},
		{"TP_SESSIONID=abc;TIMEOUT=0", 24*time.Hour - 20*time.Second},
		{"TP_SESSIONID=abc;TIMEOUT=-5", 24*time.Hour - 20*time.Second},
		{"", 24*time.Hour - 20*time.Second},
	} {
		hdr := http.Header{}
		if tc.header != "" {
			hdr.Add("Set-Cookie", tc.header)
		}
		if got := sessionLifetime(hdr); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.header, got, tc.want)
		}
	}

	creds := Credentials{Username: "a", Password: "b"}
	v2, _ := creds.authHashes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/handshake1":
			var local [16]byte
			_, _ = r.Body.Read(local[:])
			remote := make([]byte, 16)
			w.Header().Add("Set-Cookie", "TP_SESSIONID=abc;TIMEOUT=1440")
			_, _ = w.Write(append(remote, sha256sum(local[:], remote, v2)...))
		case "/app/handshake2":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	k := NewKLAP(strings.TrimPrefix(srv.URL, "http://"), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := k.handshake(ctx); err != nil {
		t.Fatal(err)
	}
	rem := time.Until(k.expires)
	if rem < 1400*time.Second || rem > 1425*time.Second {
		t.Fatalf("expires in %v, want ~1420s", rem)
	}
}
