package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/kasa"
	"github.com/hughobrien/kasad/internal/promwrite"
)

func f(v float64) *float64 { return &v }

func newTest() *Exporter {
	return New(Config{Expire: 10 * time.Minute}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func scrape(t *testing.T, e *Exporter) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.Handler("test").ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestMetricsBadAlias(t *testing.T) {
	e := newTest()
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", ok: true, last: &kasa.Reading{Alias: "\xff\xfeL", Model: "KP125(US)", HasState: true}}
	e.devices["10.0.0.2"] = &device{host: "10.0.0.2", ok: true, last: &kasa.Reading{Alias: "Switch", Model: "KS200(US)", HasState: true}}

	out := scrape(t, e)
	for _, want := range []string{
		`kasa_up{alias="Switch",host="10.0.0.2",model="KS200(US)"} 1`,
		"kasa_up{alias=\"\uFFFDL\",host=\"10.0.0.1\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMetricsUpAndDown(t *testing.T) {
	e := newTest()
	now := time.Unix(1700000000, 0)
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", ok: true, lastOK: now, last: &kasa.Reading{
		Alias: `Kid's "Lamp"`, Model: "KP125(US)", On: true, HasState: true,
		PowerWatts: f(5.5), VoltageVolts: f(122.5), RSSI: f(-60),
	}}
	e.devices["10.0.0.2"] = &device{host: "10.0.0.2", ok: true, last: &kasa.Reading{Alias: "Switch", Model: "KS200(US)", HasState: true}}

	out := scrape(t, e)
	for _, want := range []string{
		`kasa_up{alias="Kid's \"Lamp\"",host="10.0.0.1",model="KP125(US)"} 1`,
		`kasa_power_watts{alias="Kid's \"Lamp\"",host="10.0.0.1",model="KP125(US)"} 5.5`,
		`kasa_relay_on{alias="Switch",host="10.0.0.2",model="KS200(US)"} 0`,
		`kasa_last_success_timestamp_seconds{alias="Kid's \"Lamp\"",host="10.0.0.1",model="KP125(US)"} 1.7e+09`,
		`kasad_devices 2`,
		"# TYPE kasa_energy_watt_hours_total counter",
	} {
		if want == "# TYPE kasa_energy_watt_hours_total counter" {
			if strings.Contains(out, want) {
				t.Errorf("no device reports energy, family must be absent")
			}
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A switch has no meter: no power sample for it.
	if strings.Contains(out, `kasa_power_watts{alias="Switch"`) {
		t.Error("switch must not report power")
	}

	// Device goes down: measurements vanish (a gap), up drops to 0, labels stay.
	e.devices["10.0.0.1"].ok = false
	out = scrape(t, e)
	if strings.Contains(out, "kasa_power_watts") || strings.Contains(out, "kasa_rssi_dbm") {
		t.Errorf("stale measurements served for a down device:\n%s", out)
	}
	if !strings.Contains(out, `kasa_up{alias="Kid's \"Lamp\"",host="10.0.0.1",model="KP125(US)"} 0`) {
		t.Errorf("down device lost its labels:\n%s", out)
	}
}

func TestPollKindNeedsCreds(t *testing.T) {
	e := newTest() // no Creds
	_, _, _, err := e.pollKind(t.Context(), "127.0.0.1", kindKLAP, nil)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v", err)
	}
}

type fakeStore struct {
	writes [][]promwrite.Series
	err    error
}

func (s *fakeStore) Write(_ context.Context, series []promwrite.Series) error {
	if s.err != nil {
		return s.err
	}
	s.writes = append(s.writes, series)
	return nil
}

func samplesOf(ws [][]promwrite.Series, name string) []promwrite.Sample {
	var out []promwrite.Sample
	for _, w := range ws {
		for _, s := range w {
			if s.Labels["__name__"] == name {
				out = append(out, s.Samples...)
			}
		}
	}
	return out
}

// pushTest has one legacy plug that reads 10 W more on every poll, pushed
// to one target, vm.
func pushTest() (*Exporter, *fakeStore, *time.Time) {
	store := &fakeStore{}
	push := promwrite.NewPushers(promwrite.Targets{{Name: "vm", URL: "http://vm"}}, nil)
	push[0].Queue.W = store
	e := New(Config{Expire: 10 * time.Minute, PollTimeout: time.Second}, push, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1700000000, 0)
	e.now = func() time.Time { return now }
	push[0].Queue.Now = e.now
	w := 0.0
	e.be.pollLegacy = func(context.Context, string, time.Time) (*kasa.Reading, error) {
		w += 10
		return &kasa.Reading{Alias: "Lamp", Model: "KP115(US)", On: true, HasState: true, PowerWatts: f(w)}, nil
	}
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", kind: kindLegacy}
	return e, store, &now
}

func TestPushesEveryReading(t *testing.T) {
	e, store, now := pushTest()
	for range 3 {
		round(t.Context(), e)
		*now = now.Add(15 * time.Second)
	}
	p := samplesOf(store.writes, "kasa_power_watts")
	if len(p) != 3 || p[0].V != 10 || p[2].V != 30 || !p[1].T.Equal(time.Unix(1700000015, 0)) {
		t.Fatalf("want three readings at their poll times, got %v", p)
	}
	s := store.writes[0][0]
	if s.Labels["job"] != "kasad" || s.Labels["alias"] != "Lamp" || s.Labels["host"] != "10.0.0.1" || s.Labels["model"] != "KP115(US)" || s.Labels["pushed_vm"] != "" {
		t.Errorf("labels %v", s.Labels)
	}
	// /metrics keeps every reading for any scraper, the pushed ones marked
	// so vm's own scrape can drop them; health is not marked.
	out := scrape(t, e)
	for _, want := range []string{
		`kasa_power_watts{alias="Lamp",host="10.0.0.1",model="KP115(US)",pushed_vm="true"} 30`,
		`kasa_relay_on{alias="Lamp",host="10.0.0.1",model="KP115(US)",pushed_vm="true"} 1`,
		`kasa_up{alias="Lamp",host="10.0.0.1",model="KP115(US)"} 1`,
		`kasad_push_errors_total{target="vm"} 0`,
		`kasad_push_queued_samples{target="vm"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestNoTargetsServesReadingsUnmarked(t *testing.T) {
	e, _, _ := pushTest()
	e = New(Config{Expire: 10 * time.Minute, PollTimeout: time.Second}, nil, e.log)
	e.be.pollLegacy = func(context.Context, string, time.Time) (*kasa.Reading, error) {
		return &kasa.Reading{Alias: "Lamp", Model: "KP115(US)", PowerWatts: f(5)}, nil
	}
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", kind: kindLegacy}
	round(t.Context(), e)
	out := scrape(t, e)
	if !strings.Contains(out, `kasa_power_watts{alias="Lamp",host="10.0.0.1",model="KP115(US)"} 5`) || strings.Contains(out, "pushed_") {
		t.Errorf("want unmarked readings:\n%s", out)
	}
}

func TestPushFailures(t *testing.T) {
	e, store, now := pushTest()
	store.err = errors.New("connection refused")
	round(t.Context(), e)
	*now = now.Add(15 * time.Second)
	round(t.Context(), e)
	if n := e.push[0].Queue.Len(); n != 4 { // power and relay, twice
		t.Fatalf("a failed push should keep the readings: %d samples queued", n)
	}
	store.err = nil
	*now = now.Add(15 * time.Second)
	round(t.Context(), e)
	if p := samplesOf(store.writes, "kasa_power_watts"); len(p) != 3 || p[0].V != 10 || p[2].V != 30 {
		t.Fatalf("want the kept readings sent in order with the new one: %v", p)
	}
	if !strings.Contains(scrape(t, e), `kasad_push_errors_total{target="vm"} 2`) {
		t.Error("push errors not counted")
	}
	if e.push[0].Queue.MaxAge != queueAge {
		t.Error("the backlog should be bounded")
	}
}

// round polls once and flushes every target, as Run's flushers would.
func round(ctx context.Context, e *Exporter) {
	e.pollAll(ctx)
	for i := range e.push {
		e.flushOne(ctx, i)
	}
}

// What is still queued when Run ends is sent before Run returns.
func TestRunFlushesOnShutdown(t *testing.T) {
	e, store, _ := pushTest()
	e.cfg.PollInterval, e.cfg.DiscoverInterval = time.Hour, time.Hour
	e.push[0].Queue.Add(promwrite.Series{
		Labels:  map[string]string{"__name__": "kasa_power_watts", "host": "10.0.0.9"},
		Samples: []promwrite.Sample{{T: time.Unix(1700000000, 0), V: 1}}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e.Run(ctx)
	if n := e.push[0].Queue.Len(); n != 0 {
		t.Fatalf("%d samples left queued", n)
	}
	var found bool
	for _, w := range store.writes {
		for _, s := range w {
			found = found || s.Labels["host"] == "10.0.0.9"
		}
	}
	if !found {
		t.Fatal("the queued sample was never written")
	}
}

// hangStore blocks every write until its context ends, like a receiver that
// takes the connection and never answers.
type hangStore struct{ entered chan struct{} }

func (h *hangStore) Write(ctx context.Context, _ []promwrite.Series) error {
	h.entered <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

// chanStore reports each write on a channel.
type chanStore struct{ got chan int }

func (c *chanStore) Write(_ context.Context, s []promwrite.Series) error {
	c.got <- len(s)
	return nil
}

func TestHangingTargetDoesNotStallPolling(t *testing.T) {
	hang := &hangStore{entered: make(chan struct{}, 10)}
	ok := &chanStore{got: make(chan int, 10)}
	push := promwrite.NewPushers(promwrite.Targets{{Name: "slow", URL: "http://slow"}, {Name: "fast", URL: "http://fast"}}, nil)
	push[0].Queue.W, push[1].Queue.W = hang, ok
	e := New(Config{Expire: 10 * time.Minute, PollTimeout: time.Second}, push, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.be.pollLegacy = func(context.Context, string, time.Time) (*kasa.Reading, error) {
		return &kasa.Reading{Alias: "Lamp", Model: "KP115(US)", On: true, HasState: true, PowerWatts: f(5)}, nil
	}
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", kind: kindLegacy}

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for i := range e.push {
		wg.Add(1)
		go func() { defer wg.Done(); e.flusher(ctx, i) }()
	}

	e.pollAll(ctx)
	<-hang.entered // slow's write is now stuck
	<-ok.got
	// Further rounds return at once and fast still gets its readings.
	for range 3 {
		e.pollAll(ctx)
		select {
		case <-ok.got:
		case <-time.After(10 * time.Second):
			t.Fatal("the healthy target was held up by the hanging one")
		}
	}
	if e.push[0].Queue.Len() == 0 {
		t.Error("the hanging target should keep its backlog")
	}
}

// A round's poll that started before a switch and finishes after the switch's
// own poll must not overwrite the newer reading.
func TestStalePollDoesNotOverwriteWrite(t *testing.T) {
	e := newTest()
	e.cfg.PollTimeout = time.Second
	d := &device{host: "10.0.0.1", kind: kindLegacy}
	e.devices[d.host] = d

	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	e.be.pollLegacy = func(_ context.Context, _ string, _ time.Time) (*kasa.Reading, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 { // the round's poll: reads "off", its reply held back
			close(started)
			<-release
			return &kasa.Reading{Alias: "Lamp", HasState: true, On: false}, nil
		}
		return &kasa.Reading{Alias: "Lamp", HasState: true, On: true}, nil
	}
	e.be.setLegacy = func(context.Context, string, bool) error { return nil }

	done := make(chan struct{})
	go func() { e.pollOne(t.Context(), d); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("round poll never started")
	}
	if err := e.SetRelay(t.Context(), d.host, true); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("round poll never finished")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if d.last == nil || !d.last.On {
		t.Fatalf("stale poll overwrote the reading taken after the switch: %+v", d.last)
	}
	if !d.ok || d.errors != 0 {
		t.Errorf("ok=%v errors=%d", d.ok, d.errors)
	}
}

// unknownTest is a device of unknown protocol with a short PollTimeout.
func unknownTest() (*Exporter, *device) {
	e := New(Config{Expire: 10 * time.Minute, PollTimeout: 50 * time.Millisecond, Creds: &kasa.Credentials{Username: "user@example.invalid", Password: "x"}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d := &device{host: "192.0.2.1"}
	e.devices = map[string]*device{d.host: d}
	return e, d
}

func TestUnknownLegacyHangsKLAPStillTried(t *testing.T) {
	e, d := unknownTest()
	e.be.pollLegacy = func(ctx context.Context, _ string, _ time.Time) (*kasa.Reading, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	e.be.pollKLAP = func(ctx context.Context, _ *kasa.KLAP) (*kasa.Reading, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &kasa.Reading{}, nil
	}
	e.pollOne(t.Context(), d)
	if !d.ok || d.kind != kindKLAP {
		t.Fatalf("ok = %v kind = %v, want ok klap", d.ok, d.kind)
	}
}

func TestUnknownProtocolErrors(t *testing.T) {
	e, _ := unknownTest()
	e.be.pollLegacy = func(context.Context, string, time.Time) (*kasa.Reading, error) {
		return nil, errors.New("connection refused")
	}
	e.be.pollKLAP = func(context.Context, *kasa.KLAP) (*kasa.Reading, error) {
		return nil, kasa.ErrAuth
	}
	_, _, _, err := e.pollKind(t.Context(), "192.0.2.1", kindUnknown, nil)
	if err == nil || !strings.Contains(err.Error(), "legacy: connection refused") || !strings.Contains(err.Error(), "klap: "+kasa.ErrAuth.Error()) {
		t.Fatalf("err = %v", err)
	}

	e.cfg.Creds = nil
	_, _, _, err = e.pollKind(t.Context(), "192.0.2.1", kindUnknown, nil)
	if err == nil || !strings.Contains(err.Error(), "legacy: connection refused") || !strings.Contains(err.Error(), "no credentials") || strings.Contains(err.Error(), "needs KLAP") {
		t.Fatalf("err = %v", err)
	}
}
