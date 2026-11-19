package exporter

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/kasa"
)

func f(v float64) *float64 { return &v }

func newTest() *Exporter {
	return New(Config{Expire: 10 * time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
