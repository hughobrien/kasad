package exporter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/kasa"
)

// stub records backend calls and returns canned results.
type stub struct {
	legacySets []bool
	klapSets   []bool
	setErr     error
	reading    *kasa.Reading
}

func (s *stub) install(e *Exporter) {
	e.be = backend{
		pollLegacy: func(context.Context, string, time.Time) (*kasa.Reading, error) { return s.reading, nil },
		pollKLAP:   func(context.Context, *kasa.KLAP) (*kasa.Reading, error) { return s.reading, nil },
		setLegacy: func(_ context.Context, _ string, on bool) error {
			s.legacySets = append(s.legacySets, on)
			return s.setErr
		},
		setKLAP: func(_ context.Context, _ *kasa.KLAP, on bool) error {
			s.klapSets = append(s.klapSets, on)
			return s.setErr
		},
	}
}

func stubbed(t *testing.T) (*Exporter, *stub) {
	t.Helper()
	e := newTest()
	e.cfg.PollTimeout = time.Second
	s := &stub{reading: &kasa.Reading{Alias: "Lamp", Model: "KP125(US)", On: true, HasState: true}}
	s.install(e)
	e.devices["10.0.0.1"] = &device{host: "10.0.0.1", kind: kindLegacy, ok: true,
		last: &kasa.Reading{Alias: "Lamp", Model: "KP125(US)", HasState: true}}
	return e, s
}

func TestSetRelayLegacy(t *testing.T) {
	e, s := stubbed(t)
	if err := e.SetRelay(t.Context(), "10.0.0.1", true); err != nil {
		t.Fatal(err)
	}
	if len(s.legacySets) != 1 || !s.legacySets[0] {
		t.Fatalf("writes = %v", s.legacySets)
	}
	// The device is polled straight after the write: the view shows what it reports.
	if v := e.Devices()[0]; !v.On || !v.HasState {
		t.Fatalf("view after write = %+v", v)
	}
}

func TestSetRelayUnknownHost(t *testing.T) {
	e, s := stubbed(t)
	if err := e.SetRelay(t.Context(), "10.9.9.9", true); err == nil {
		t.Fatal("unknown host must be an error")
	}
	if len(s.legacySets)+len(s.klapSets) != 0 {
		t.Fatal("no device I/O for an unknown host")
	}
}

func TestSetRelayUnseenDevice(t *testing.T) {
	e, s := stubbed(t)
	e.devices["10.0.0.2"] = &device{host: "10.0.0.2"} // never answered: protocol unknown
	if err := e.SetRelay(t.Context(), "10.0.0.2", true); err == nil {
		t.Fatal("a device that has never answered must be an error")
	}
	if len(s.legacySets)+len(s.klapSets) != 0 {
		t.Fatal("no device I/O for an unseen device")
	}
}

func TestSetRelayErrorRecorded(t *testing.T) {
	e, s := stubbed(t)
	s.setErr = errors.New("boom")
	if err := e.SetRelay(t.Context(), "10.0.0.1", false); err == nil {
		t.Fatal("expected the write error")
	}
	if len(s.legacySets) != 2 {
		t.Fatalf("a failed write is tried twice, got %d", len(s.legacySets))
	}
	if v := e.Devices()[0]; v.Err == "" {
		t.Fatalf("view must carry the error: %+v", v)
	}
	// The next successful poll clears it.
	e.pollOne(t.Context(), e.devices["10.0.0.1"])
	if v := e.Devices()[0]; v.Err != "" {
		t.Fatalf("error should clear after a good poll: %+v", v)
	}
}

func TestSetRelayKLAP(t *testing.T) {
	e, s := stubbed(t)
	e.cfg.Creds = &kasa.Credentials{Username: "a", Password: "b"}
	e.devices["10.0.0.3"] = &device{host: "10.0.0.3", kind: kindKLAP, ok: true,
		klap: kasa.NewKLAP("10.0.0.3", *e.cfg.Creds), last: &kasa.Reading{Alias: "Plug"}}
	if err := e.SetRelay(t.Context(), "10.0.0.3", false); err != nil {
		t.Fatal(err)
	}
	if len(s.klapSets) != 1 || s.klapSets[0] {
		t.Fatalf("klap writes = %v", s.klapSets)
	}
}

func TestSetRelayNeedsKLAPCreds(t *testing.T) {
	e, s := stubbed(t)
	e.devices["10.0.0.3"] = &device{host: "10.0.0.3", kind: kindKLAP}
	if err := e.SetRelay(t.Context(), "10.0.0.3", true); err == nil {
		t.Fatal("KLAP device without credentials must be an error")
	}
	if len(s.klapSets) != 0 {
		t.Fatal("no write without credentials")
	}
}

func TestDevicesView(t *testing.T) {
	e := newTest()
	f := func(v float64) *float64 { return &v }
	now := time.Unix(1700000000, 0)
	e.devices["10.0.0.9"] = &device{host: "10.0.0.9", ok: true, lastOK: now, last: &kasa.Reading{
		Alias: "bravo", Model: "KP125(US)", On: true, HasState: true, PowerWatts: f(5.5), RSSI: f(-60),
		EnergyTodayWh: f(3), EnergyMonthWh: f(1185),
	}}
	e.devices["10.0.0.8"] = &device{host: "10.0.0.8", ok: false, errors: 3, last: &kasa.Reading{
		Alias: "Alpha", Model: "KS200(US)", On: true, HasState: true, PowerWatts: f(9),
	}}
	e.devices["10.0.0.7"] = &device{host: "10.0.0.7"} // never answered

	vs := e.Devices()
	// Sorted by name (case-insensitive; a device with no name sorts by host), then host.
	if got := []string{vs[0].Host, vs[1].Host, vs[2].Host}; got[0] != "10.0.0.7" || got[1] != "10.0.0.8" || got[2] != "10.0.0.9" {
		// "10.0.0.7" < "alpha" < "bravo" as strings
		t.Fatalf("order = %v", got)
	}
	up := vs[2]
	if !up.Up || *up.PowerWatts != 5.5 || *up.EnergyTodayKWh != 0.003 || *up.EnergyMonthKWh != 1.185 || *up.RSSI != -60 {
		t.Fatalf("up view = %+v", up)
	}
	down := vs[1]
	if down.Up || down.Alias != "Alpha" || down.PowerWatts != nil || down.On || down.HasState {
		t.Fatalf("a down device keeps its name and loses its readings and state: %+v", down)
	}
}

func TestSubscribeNotified(t *testing.T) {
	e, _ := stubbed(t)
	ch, unsub := e.Subscribe()
	defer unsub()
	e.pollOne(t.Context(), e.devices["10.0.0.1"])
	e.pollOne(t.Context(), e.devices["10.0.0.1"])
	select {
	case <-ch:
	default:
		t.Fatal("no notification after a poll")
	}
	select {
	case <-ch:
		t.Fatal("two quick events must coalesce into one")
	default:
	}
}

func TestUnsubscribe(t *testing.T) {
	e, _ := stubbed(t)
	ch, unsub := e.Subscribe()
	unsub()
	e.pollOne(t.Context(), e.devices["10.0.0.1"])
	select {
	case <-ch:
		t.Fatal("delivery after unsubscribe")
	default:
	}
}
