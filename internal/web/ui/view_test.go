package ui

import (
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
)

func f(v float64) *float64 { return &v }

var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestNewCardFormats(t *testing.T) {
	c := NewCard(exporter.DeviceView{
		Host: "10.0.0.1", Alias: "Lamp", Model: "KP125(US)", Up: true, On: true, HasState: true,
		PowerWatts: f(5.627), EnergyTodayKWh: f(0.003), EnergyMonthKWh: f(1.185), RSSI: f(-63), LastOK: now,
	}, now)
	if c.ID != "device-10-0-0-1" || c.Name != "Lamp" || c.Kind != "Socket" {
		t.Fatalf("identity = %+v", c)
	}
	if c.Power != "5.6 W" || c.Today != "0.003 kWh" || c.Month != "1.19 kWh" || c.RSSI != "-63 dBm" {
		t.Fatalf("readings = %q %q %q %q", c.Power, c.Today, c.Month, c.RSSI)
	}
}

func TestFormatKWh(t *testing.T) {
	for in, want := range map[float64]string{0: "0.000 kWh", 0.0034: "0.003 kWh", 12.345: "12.35 kWh", 123.456: "123.5 kWh"} {
		if got := formatKWh(in); got != want {
			t.Errorf("formatKWh(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNewCardUnmetered(t *testing.T) {
	c := NewCard(exporter.DeviceView{Host: "10.0.0.2", Alias: "Switch", Up: true, HasState: true, RSSI: f(-50)}, now)
	if c.Power != "" || c.Today != "" || c.Month != "" {
		t.Fatalf("an unmetered switch has no energy text: %+v", c)
	}
	if c.RSSI != "-50 dBm" {
		t.Fatalf("rssi = %q", c.RSSI)
	}
}

func TestNewCardDownAndUnnamed(t *testing.T) {
	c := NewCard(exporter.DeviceView{Host: "10.0.0.3", Alias: "Plug", LastOK: now.Add(-3 * time.Minute)}, now)
	if c.Up || c.LastSeen != "3m ago" {
		t.Fatalf("down card = %+v", c)
	}
	c = NewCard(exporter.DeviceView{Host: "10.0.0.4"}, now) // never answered
	if c.Name != "10.0.0.4" || c.LastSeen != "never" {
		t.Fatalf("unnamed card = %+v", c)
	}
}

func TestNewCardIDIsSafeForIPv6(t *testing.T) {
	if id := NewCard(exporter.DeviceView{Host: "fd00::1"}, now).ID; id != "device-fd00--1" {
		t.Fatalf("id = %q", id)
	}
}

func TestNewPageTotals(t *testing.T) {
	p := NewPage([]exporter.DeviceView{
		{Host: "10.0.0.1", Up: true, PowerWatts: f(5.5)},
		{Host: "10.0.0.2", Up: true, PowerWatts: f(300)},
		{Host: "10.0.0.3", Up: true},                      // unmetered
		{Host: "10.0.0.4", Up: false, PowerWatts: f(999)}, // down: not counted
	}, now, nil)
	if p.TotalPower != "305.5 W" || p.UpCount != 3 || p.Total != 4 || len(p.Sections[0].Cards) != 4 {
		t.Fatalf("page = %+v", p)
	}
	if e := NewPage(nil, now, nil); e.TotalPower != "0.0 W" || e.UpCount != 0 || e.Total != 0 {
		t.Fatalf("empty page = %+v", e)
	}
}

func TestDeviceKind(t *testing.T) {
	for _, tc := range []struct {
		model  string
		bright *float64
		want   string
	}{
		{"KP125(US)", nil, "Socket"},
		{"EP25(US)", nil, "Socket"},
		{"HS103(US)", nil, "Socket"},
		{"KS200(US)", nil, "Light Switch"},
		{"HS200(US)", nil, "Light Switch"},
		{"KS220(US)", f(30), "Light Switch (Dimmable)"},
		{"HS220(US)", nil, "Light Switch (Dimmable)"},
		{"KS200(US)", f(30), "Light Switch (Dimmable)"}, // reports a brightness: dimmable
		{"XYZ9", nil, "XYZ9"},                           // unknown: show the model, never mislabel
		{"", nil, ""},
	} {
		if got := deviceKind(tc.model, tc.bright); got != tc.want {
			t.Errorf("deviceKind(%q, %v) = %q, want %q", tc.model, tc.bright, got, tc.want)
		}
	}
}

func TestNewCardBrightness(t *testing.T) {
	c := NewCard(exporter.DeviceView{Host: "10.0.0.3", Alias: "Down", Model: "KS220(US)", Up: true, Brightness: f(46.4)}, now)
	if !c.Dimmable || c.Brightness != 46 || c.Kind != "Light Switch (Dimmable)" {
		t.Fatalf("%+v", c)
	}
	p := NewCard(exporter.DeviceView{Host: "10.0.0.1", Model: "KP125(US)", Up: true}, now)
	if p.Dimmable {
		t.Fatal("a plug is not dimmable")
	}
}
