package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
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

// pushTest has one legacy plug that reads 10 W more on every poll.
func pushTest() (*Exporter, *fakeStore, *time.Time) {
	store := &fakeStore{}
	e := New(Config{Expire: 10 * time.Minute, PollTimeout: time.Second}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1700000000, 0)
	e.now = func() time.Time { return now }
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
		e.pollAll(t.Context())
		*now = now.Add(15 * time.Second)
	}
	p := samplesOf(store.writes, "kasa_power_watts")
	if len(p) != 3 || p[0].V != 10 || p[2].V != 30 || !p[1].T.Equal(time.Unix(1700000015, 0)) {
		t.Fatalf("want three readings at their poll times, got %v", p)
	}
	s := store.writes[0][0]
	if s.Labels["job"] != "kasad" || s.Labels["alias"] != "Lamp" || s.Labels["host"] != "10.0.0.1" || s.Labels["model"] != "KP115(US)" {
		t.Errorf("labels %v", s.Labels)
	}
	out := scrape(t, e)
	if strings.Contains(out, "kasa_power_watts") || strings.Contains(out, "kasa_relay_on") {
		t.Errorf("measurements are pushed, not scraped:\n%s", out)
	}
	for _, want := range []string{`kasa_up{alias="Lamp",host="10.0.0.1",model="KP115(US)"} 1`, "kasad_push_errors_total 0", "kasad_push_queued_readings 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPushFailures(t *testing.T) {
	e, store, now := pushTest()
	store.err = errors.New("connection refused")
	e.pollAll(t.Context())
	*now = now.Add(15 * time.Second)
	e.pollAll(t.Context())
	if len(e.queue) != 2 {
		t.Fatalf("a failed push should keep the readings: %d queued", len(e.queue))
	}
	store.err = nil
	*now = now.Add(15 * time.Second)
	e.pollAll(t.Context())
	if p := samplesOf(store.writes, "kasa_power_watts"); len(p) != 3 || p[0].V != 10 || p[2].V != 30 {
		t.Fatalf("want the kept readings sent in order with the new one: %v", p)
	}

	store.err = &promwrite.RefusedError{Status: 400, Body: "out of order sample"}
	e.pollAll(t.Context())
	if len(e.queue) != 0 {
		t.Error("a refused push should be dropped")
	}

	store.err = errors.New("down")
	e.pollAll(t.Context())
	*now = now.Add(time.Hour)
	store.err = nil
	n := len(samplesOf(store.writes, "kasa_power_watts"))
	e.pollAll(t.Context())
	if got := len(samplesOf(store.writes, "kasa_power_watts")) - n; got != 1 {
		t.Errorf("an hour-old reading should be dropped, not sent: %d sent", got)
	}
	if !strings.Contains(scrape(t, e), "kasad_push_errors_total 4") {
		t.Error("push errors not counted")
	}
}
