package ui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hughobrien/kasad/internal/exporter"
)

func devs(names ...string) []exporter.DeviceView {
	var out []exporter.DeviceView
	for i, n := range names {
		out = append(out, exporter.DeviceView{Host: "10.0.0." + string(rune('1'+i)), Alias: n, Up: true, PowerWatts: f(10)})
	}
	return out
}

func sectionNames(p PageView) []string {
	var out []string
	for _, s := range p.Sections {
		out = append(out, s.Name)
	}
	return out
}

func cardNames(s Section) string {
	var out []string
	for _, c := range s.Cards {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestNoMetaIsOneUngroupedSection(t *testing.T) {
	p := NewPage(devs("A", "B"), now, nil)
	if len(p.Sections) != 1 || p.Sections[0].Name != "" || cardNames(p.Sections[0]) != "A,B" {
		t.Fatalf("%+v", p.Sections)
	}
	if p := NewPage(nil, now, nil); len(p.Sections) != 1 || len(p.Sections[0].Cards) != 0 {
		t.Fatalf("empty page: %+v", p.Sections)
	}
}

func TestRoomsOrderOtherAndCase(t *testing.T) {
	m := &Meta{Rooms: []Room{
		{Name: "Kitchen", Devices: []string{"fridge", "Oven"}},
		{Name: "Empty", Devices: []string{"Ghost"}},
		{Name: "Bedroom", Devices: []string{"LAMP", "Fridge"}}, // Fridge already in Kitchen
	}}
	p := NewPage(devs("Fridge", "Lamp", "Oven", "Stray"), now, m)
	if got := strings.Join(sectionNames(p), "|"); got != "Kitchen|Bedroom|Other" {
		t.Fatalf("sections = %s", got)
	}
	if cardNames(p.Sections[0]) != "Fridge,Oven" || cardNames(p.Sections[1]) != "Lamp" || cardNames(p.Sections[2]) != "Stray" {
		t.Fatalf("%+v", p.Sections)
	}
}

func TestNoOtherSectionWhenEverythingIsPlaced(t *testing.T) {
	m := &Meta{Rooms: []Room{{Name: "R", Devices: []string{"A"}}}}
	if got := sectionNames(NewPage(devs("A"), now, m)); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestDownstreamNotCountedTwice(t *testing.T) {
	m := &Meta{Downstream: map[string]string{"Sound": "Desk"}}
	ds := devs("Desk", "Sound")
	ds[0].PowerWatts, ds[1].PowerWatts = f(40), f(15) // Desk's reading already includes Sound
	p := NewPage(ds, now, m)
	if p.TotalPower != "40.0 W" {
		t.Fatalf("total = %s, want 40.0 W", p.TotalPower)
	}
	var via string
	for _, c := range p.Sections[0].Cards {
		if c.Name == "Sound" {
			via = c.Via
		}
	}
	if via != "Desk" {
		t.Fatalf("via = %q", via)
	}

	// Desk down: nothing else accounts for Sound, so it counts.
	ds[0].Up, ds[0].PowerWatts = false, nil
	if p := NewPage(ds, now, m); p.TotalPower != "15.0 W" {
		t.Fatalf("upstream down: total = %s, want 15.0 W", p.TotalPower)
	}
}

func TestLoadMeta(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "m.json")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	m, err := LoadMeta(write(`{"rooms":[{"name":"K","devices":["a"]}],"downstream":{"b":"a"}}`))
	if err != nil || len(m.Rooms) != 1 || m.Downstream["b"] != "a" {
		t.Fatalf("%+v %v", m, err)
	}
	for name, body := range map[string]string{
		"syntax":  `{`,
		"unknown": `{"room":[]}`, // a typo'd key must not be silently ignored
		"noname":  `{"rooms":[{"devices":["a"]}]}`,
		"selfref": `{"downstream":{"a":"A"}}`,
	} {
		if _, err := LoadMeta(write(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := LoadMeta(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must be an error")
	}
}

func TestUnmatched(t *testing.T) {
	m := &Meta{Rooms: []Room{{Name: "R", Devices: []string{"A", "Typo"}}}, Downstream: map[string]string{"B": "Nope"}}
	got := strings.Join(m.Unmatched(devs("A", "B")), ",")
	if got != "Typo,Nope" {
		t.Fatalf("unmatched = %q", got)
	}
}

func TestConcurrentPages(t *testing.T) {
	m := &Meta{Downstream: map[string]string{"Sound": "Desk", "Lamp": "Desk"}}
	ds := devs("Desk", "Sound", "Lamp")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			NewPage(ds, now, m)
		}()
	}
	wg.Wait()
}
