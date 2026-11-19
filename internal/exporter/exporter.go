// Package exporter keeps the set of known devices, polls them in the
// background and renders the last results as Prometheus metrics.
package exporter

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hughobrien/kasad/internal/kasa"
)

type kind int

const (
	kindUnknown kind = iota // try legacy, then KLAP
	kindLegacy
	kindKLAP
)

// Config controls discovery and polling.
type Config struct {
	Broadcast        []string          // discovery targets (IPv4 broadcast or unicast)
	Hosts            []string          // devices to poll even if discovery misses them
	Creds            *kasa.Credentials // nil = legacy devices only
	PollInterval     time.Duration
	DiscoverInterval time.Duration
	Expire           time.Duration // drop a discovered device unseen this long
	PollTimeout      time.Duration
	DiscoverWait     time.Duration
}

type device struct {
	host   string
	static bool
	kind   kind
	klap   *kasa.KLAP

	lastSeen time.Time // discovery reply or successful poll
	lastOK   time.Time
	ok       bool
	errors   uint64
	last     *kasa.Reading // last good reading; labels survive an outage
}

// Exporter owns the device table.
type Exporter struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	devices map[string]*device
}

// New returns an Exporter; call Run to start it.
func New(cfg Config, log *slog.Logger) *Exporter {
	e := &Exporter{cfg: cfg, log: log, now: time.Now, devices: map[string]*device{}}
	for _, h := range cfg.Hosts {
		e.devices[h] = &device{host: h, static: true}
	}
	return e
}

// Run discovers and polls until ctx is done.
func (e *Exporter) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.loop(ctx, e.cfg.DiscoverInterval, e.discover) }()
	go func() { defer wg.Done(); e.loop(ctx, e.cfg.PollInterval, e.pollAll) }()
	wg.Wait()
}

func (e *Exporter) loop(ctx context.Context, every time.Duration, f func(context.Context)) {
	f(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f(ctx)
		}
	}
}

func (e *Exporter) discover(ctx context.Context) {
	found, err := kasa.Discover(ctx, e.cfg.Broadcast, e.cfg.DiscoverWait)
	if err != nil {
		e.log.Warn("discovery failed", "err", err)
		return
	}
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	add := func(host string, k kind) {
		d, ok := e.devices[host]
		if !ok {
			d = &device{host: host}
			e.devices[host] = d
			e.log.Info("discovered device", "host", host)
		}
		d.lastSeen = now
		// Only a legacy reply proves the protocol. A reply on the newer
		// discovery port does not: switches answer it too yet speak legacy
		// over TCP, so those are left for the poller to work out.
		if k == kindLegacy {
			d.kind = k
		}
	}
	for h := range found.Legacy {
		add(h, kindLegacy)
	}
	if e.cfg.Creds != nil {
		for h := range found.Smart {
			add(h, kindUnknown)
		}
	}
	for h, d := range e.devices {
		if d.static {
			continue
		}
		if now.Sub(d.lastSeen) > e.cfg.Expire {
			e.log.Info("dropping device unseen", "host", h, "for", e.cfg.Expire)
			delete(e.devices, h)
		}
	}
}

func (e *Exporter) pollAll(ctx context.Context) {
	e.mu.Lock()
	devs := make([]*device, 0, len(e.devices))
	for _, d := range e.devices {
		devs = append(devs, d)
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.pollOne(ctx, d)
		}()
	}
	wg.Wait()
}

func (e *Exporter) pollOne(ctx context.Context, d *device) {
	e.mu.Lock()
	k, kl := d.kind, d.klap
	e.mu.Unlock()

	var (
		r    *kasa.Reading
		err  error
		used = k
	)
	// One retry: Wi-Fi plugs drop the odd packet.
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, e.cfg.PollTimeout)
		r, used, kl, err = e.pollKind(cctx, d.host, k, kl)
		cancel()
		if err == nil {
			break
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if d.ok || d.errors == 0 {
			e.log.Warn("poll failed", "host", d.host, "err", err)
		}
		d.ok = false
		d.errors++
		return
	}
	now := e.now()
	if !d.ok && d.errors > 0 {
		e.log.Info("device back", "host", d.host)
	}
	d.ok, d.last, d.lastOK, d.lastSeen = true, r, now, now
	d.kind, d.klap = used, kl
}

// pollKind polls with the known protocol, or tries each when unknown.
func (e *Exporter) pollKind(ctx context.Context, host string, k kind, kl *kasa.KLAP) (*kasa.Reading, kind, *kasa.KLAP, error) {
	tryKLAP := func() (*kasa.Reading, kind, *kasa.KLAP, error) {
		if e.cfg.Creds == nil {
			return nil, kindUnknown, kl, errors.New("device needs KLAP but no credentials are configured")
		}
		if kl == nil {
			kl = kasa.NewKLAP(host, *e.cfg.Creds)
		}
		r, err := kl.Poll(ctx)
		return r, kindKLAP, kl, err
	}
	switch k {
	case kindLegacy:
		r, err := kasa.PollLegacy(ctx, host)
		return r, kindLegacy, kl, err
	case kindKLAP:
		return tryKLAP()
	}
	if r, err := kasa.PollLegacy(ctx, host); err == nil {
		return r, kindLegacy, kl, nil
	}
	return tryKLAP()
}

func (k kind) String() string {
	switch k {
	case kindLegacy:
		return "legacy"
	case kindKLAP:
		return "klap"
	}
	return "unknown"
}

// snapshot is a copy of one device's state for rendering.
type snapshot struct {
	host   string
	kind   kind
	ok     bool
	errors uint64
	lastOK time.Time
	last   *kasa.Reading
}

func (e *Exporter) snapshot() []snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]snapshot, 0, len(e.devices))
	for _, d := range e.devices {
		out = append(out, snapshot{d.host, d.kind, d.ok, d.errors, d.lastOK, d.last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].host < out[j].host })
	return out
}
