package kasa

import (
	"context"
	"net/http"
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
