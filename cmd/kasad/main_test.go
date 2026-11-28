package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
)

func TestCheck(t *testing.T) {
	good := exporter.Config{PollInterval: time.Second, DiscoverInterval: time.Minute, Expire: time.Minute, PollTimeout: time.Second}
	if err := check(good, nil); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		mod  func(*exporter.Config)
		args []string
		want string
	}{
		"poll zero":         {func(c *exporter.Config) { c.PollInterval = 0 }, nil, "poll-interval"},
		"poll negative":     {func(c *exporter.Config) { c.PollInterval = -time.Second }, nil, "poll-interval"},
		"discover zero":     {func(c *exporter.Config) { c.DiscoverInterval = 0 }, nil, "discover-interval"},
		"expire zero":       {func(c *exporter.Config) { c.Expire = 0 }, nil, "expire"},
		"poll-timeout zero": {func(c *exporter.Config) { c.PollTimeout = 0 }, nil, "poll-timeout"},
		"stray argument":    {func(*exporter.Config) {}, []string{"listen"}, "unexpected argument"},
	} {
		c := good
		tc.mod(&c)
		err := check(c, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestReadCreds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "creds")
	if err := os.WriteFile(p, []byte("user@example.com\r\nsecret\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := readCreds(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "user@example.com" || c.Password != "secret" {
		t.Fatalf("got %+v", c)
	}

	if err := os.WriteFile(p, []byte("user@example.com\n\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCreds(p); err == nil {
		t.Fatal("empty CRLF password line must be rejected")
	}
}
