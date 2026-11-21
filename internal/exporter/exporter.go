// Package exporter keeps the set of known devices, polls them in the
// background, pushes every reading with remote write (at the time it was
// taken) to each target, and serves the latest readings and health on
// /metrics, the pushed ones labelled pushed_<target>="true".
package exporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hughobrien/kasad/internal/kasa"
	"github.com/hughobrien/kasad/internal/promwrite"
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
	Job              string // job label on pushed series
}

type device struct {
	host   string
	static bool
	kind   kind
	klap   *kasa.KLAP

	writeErr string    // last failed write; cleared by the next successful poll
	lastSeen time.Time // discovery reply or successful poll
	lastOK   time.Time
	ok       bool
	errors   uint64
	last     *kasa.Reading // last good reading; labels survive an outage

	// gen is bumped when a poll starts or a write begins; a poll whose gen
	// has moved on when it finishes is stale and its result is dropped.
	gen uint64
}

// backend is the device I/O, a seam so tests need no network.
type backend struct {
	pollLegacy    func(ctx context.Context, host string, now time.Time) (*kasa.Reading, error)
	pollKLAP      func(ctx context.Context, k *kasa.KLAP) (*kasa.Reading, error)
	setLegacy     func(ctx context.Context, host string, on bool) error
	setKLAP       func(ctx context.Context, k *kasa.KLAP, on bool) error
	setBrightness func(ctx context.Context, host string, pct int) error
}

func realBackend() backend {
	return backend{
		pollLegacy:    kasa.PollLegacy,
		pollKLAP:      func(ctx context.Context, k *kasa.KLAP) (*kasa.Reading, error) { return k.Poll(ctx) },
		setLegacy:     kasa.SetLegacyRelay,
		setBrightness: kasa.SetLegacyBrightness,
		setKLAP:       func(ctx context.Context, k *kasa.KLAP, on bool) error { return k.SetDeviceOn(ctx, on) },
	}
}

// Exporter owns the device table.
type Exporter struct {
	cfg Config
	log *slog.Logger
	now func() time.Time
	be  backend

	// subs are woken (coalesced, never blocking) whenever a device changes.
	subs map[chan struct{}]struct{}

	// push gets every reading, one queue per target.
	push promwrite.Pushers
	// wake[i] asks push[i]'s flusher for a flush (size 1: wake-ups coalesce).
	wake         []chan struct{}
	flushTimeout time.Duration

	mu      sync.Mutex
	devices map[string]*device
}

// queueAge bounds each target's backlog while it is away: about 1,000
// samples a poll is 240,000 an hour.
const queueAge = 2 * time.Hour

// flushTimeout bounds one write, so a target that accepts connections and
// never answers is given up on well within a poll interval.
const flushTimeout = 10 * time.Second

// New returns an Exporter; call Run to start it. Every reading is pushed to
// each of push (none is fine: /metrics has them all).
func New(cfg Config, push promwrite.Pushers, log *slog.Logger) *Exporter {
	if cfg.Job == "" {
		cfg.Job = "kasad"
	}
	for _, p := range push {
		p.Queue.MaxAge = queueAge
	}
	e := &Exporter{cfg: cfg, log: log, now: time.Now, be: realBackend(), push: push,
		flushTimeout: flushTimeout, subs: map[chan struct{}]struct{}{}, devices: map[string]*device{}}
	for range push {
		e.wake = append(e.wake, make(chan struct{}, 1))
	}
	for _, h := range cfg.Hosts {
		e.devices[h] = &device{host: h, static: true}
	}
	return e
}

// Run discovers and polls until ctx is done.
func (e *Exporter) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2 + len(e.push))
	for i := range e.push {
		go func() { defer wg.Done(); e.flusher(ctx, i) }()
	}
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
	for _, w := range e.wake {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// flusher sends target i's queue whenever a round has finished. It runs on
// its own so polling never waits on a push, and a target that hangs delays
// only itself. Rounds that end during a flush share the next one.
func (e *Exporter) flusher(ctx context.Context, i int) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake[i]:
			e.flushOne(ctx, i)
		}
	}
}

// flushOne sends what target i has queued; on failure it keeps its queue
// for the next flush.
func (e *Exporter) flushOne(ctx context.Context, i int) {
	ctx, cancel := context.WithTimeout(ctx, e.flushTimeout)
	defer cancel()
	if err := e.push[i].Queue.Flush(ctx); err != nil {
		e.log.Warn("push failed", "target", e.push[i].Name, "err", err)
	}
}

// series turns one reading into a series per measured metric, one sample at
// the poll's time.
func (e *Exporter) series(at time.Time, s snapshot) []promwrite.Series {
	alias, model := "", ""
	if s.last != nil {
		alias, model = labelText(s.last.Alias), labelText(s.last.Model)
	}
	var out []promwrite.Series
	for _, m := range metrics {
		if !m.pushed {
			continue
		}
		if v, ok := m.value(s); ok {
			out = append(out, promwrite.Series{
				Labels:  map[string]string{"__name__": m.name, "job": e.cfg.Job, "alias": alias, "host": s.host, "model": model},
				Samples: []promwrite.Sample{{T: at, V: v}}})
		}
	}
	return out
}

func (e *Exporter) pollOne(ctx context.Context, d *device) {
	e.mu.Lock()
	k, kl := d.kind, d.klap
	d.gen++
	gen := d.gen
	e.mu.Unlock()

	var (
		r    *kasa.Reading
		err  error
		used = k
	)
	// One retry: Wi-Fi plugs drop the odd packet.
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		r, used, kl, err = e.pollKind(ctx, d.host, k, kl)
		if err == nil {
			break
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if d.gen != gen {
		return // a write or a newer poll began meanwhile; this reading is stale
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if d.ok || d.errors == 0 {
			e.log.Warn("poll failed", "host", d.host, "err", err)
		}
		d.ok = false
		d.errors++
		e.notify()
		return
	}
	now := e.now()
	if !d.ok && d.errors > 0 {
		e.log.Info("device back", "host", d.host)
	}
	d.ok, d.last, d.lastOK, d.lastSeen = true, r, now, now
	d.kind, d.klap = used, kl
	d.writeErr = ""
	e.push.Add(e.series(now, snapshot{host: d.host, kind: d.kind, ok: true, lastOK: now, last: r})...)
	e.notify()
}

// notify wakes every subscriber. Callers hold e.mu.
func (e *Exporter) notify() {
	for ch := range e.subs {
		select {
		case ch <- struct{}{}:
		default: // one wake-up is already pending
		}
	}
}

// Subscribe returns a channel that receives a value whenever any device
// changes (a poll finished or a write was made); bursts coalesce. Call the
// returned func to stop.
func (e *Exporter) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

// SetRelay switches a known device on or off, then polls it straight away so
// the view reports what the device says rather than what we asked for.
func (e *Exporter) SetRelay(ctx context.Context, host string, on bool) error {
	return e.write(ctx, host, func(d *device) error { return nil },
		func(c context.Context) error { return e.be.setLegacy(c, host, on) },
		func(c context.Context, kl *kasa.KLAP) error { return e.be.setKLAP(c, kl, on) })
}

// ErrNotDimmable is returned for a brightness write to a device without a dimmer.
var ErrNotDimmable = errors.New("device is not a dimmer")

// SetBrightness sets a dimmer's brightness (1-100). Only legacy dimmers exist
// on the LAN; a KLAP device is refused.
func (e *Exporter) SetBrightness(ctx context.Context, host string, pct int) error {
	if pct < 1 || pct > 100 {
		return fmt.Errorf("brightness %d out of range 1-100", pct)
	}
	return e.write(ctx, host,
		func(d *device) error {
			if d.last == nil || d.last.Brightness == nil || d.kind != kindLegacy {
				return ErrNotDimmable
			}
			return nil
		},
		func(c context.Context) error { return e.be.setBrightness(c, host, pct) },
		nil)
}

// write looks up the host, checks it with allow, writes with the device's
// protocol (klapSet nil = not supported), then polls it straight away.
func (e *Exporter) write(ctx context.Context, host string, allow func(*device) error,
	legacySet func(context.Context) error, klapSet func(context.Context, *kasa.KLAP) error) error {
	e.mu.Lock()
	d, ok := e.devices[host]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown device %q", host)
	}
	k, kl := d.kind, d.klap
	err := allow(d)
	if err == nil {
		d.gen++ // polls already in flight predate this write
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}

	var set func(context.Context) error
	switch k {
	case kindLegacy:
		set = legacySet
	case kindKLAP:
		if klapSet == nil {
			return ErrNotDimmable
		}
		if e.cfg.Creds == nil {
			return errors.New("device needs KLAP but no credentials are configured")
		}
		if kl == nil {
			kl = kasa.NewKLAP(host, *e.cfg.Creds)
		}
		set = func(c context.Context) error { return klapSet(c, kl) }
	default:
		return fmt.Errorf("device %q has not answered yet", host)
	}

	// Setting a state is idempotent, so one retry is safe on flaky Wi-Fi.
	for attempt := 0; attempt < 2; attempt++ {
		if err = ctx.Err(); err != nil {
			break
		}
		cctx, cancel := context.WithTimeout(ctx, e.cfg.PollTimeout)
		err = set(cctx)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		e.mu.Lock()
		d.writeErr = err.Error()
		e.notify()
		e.mu.Unlock()
		return err
	}
	e.pollOne(ctx, d)
	return nil
}

// pollKind polls with the known protocol, or tries each when unknown. Each
// attempt gets its own PollTimeout, so a device that silently drops TCP 9999
// still leaves KLAP a full try.
func (e *Exporter) pollKind(ctx context.Context, host string, k kind, kl *kasa.KLAP) (*kasa.Reading, kind, *kasa.KLAP, error) {
	tryLegacy := func() (*kasa.Reading, error) {
		cctx, cancel := context.WithTimeout(ctx, e.cfg.PollTimeout)
		defer cancel()
		return e.be.pollLegacy(cctx, host, e.now())
	}
	tryKLAP := func() (*kasa.Reading, kind, *kasa.KLAP, error) {
		if e.cfg.Creds == nil {
			return nil, kindUnknown, kl, errors.New("device needs KLAP but no credentials are configured")
		}
		if kl == nil {
			kl = kasa.NewKLAP(host, *e.cfg.Creds)
		}
		cctx, cancel := context.WithTimeout(ctx, e.cfg.PollTimeout)
		defer cancel()
		r, err := e.be.pollKLAP(cctx, kl)
		return r, kindKLAP, kl, err
	}
	switch k {
	case kindLegacy:
		r, err := tryLegacy()
		return r, kindLegacy, kl, err
	case kindKLAP:
		return tryKLAP()
	}
	r, lerr := tryLegacy()
	if lerr == nil {
		return r, kindLegacy, kl, nil
	}
	r, used, kl, kerr := tryKLAP()
	if kerr == nil {
		return r, used, kl, nil
	}
	// Report both attempts. Without credentials KLAP was never tried, so say
	// the device did not answer legacy rather than blame the credentials.
	if e.cfg.Creds == nil {
		kerr = errors.New("KLAP not tried: no credentials are configured")
	}
	return nil, kindUnknown, kl, fmt.Errorf("protocol unknown: legacy: %v; klap: %v", lerr, kerr)
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
