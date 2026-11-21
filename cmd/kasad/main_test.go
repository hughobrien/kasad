package main

import (
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
