package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/hughobrien/kasad/internal/exporter"
)

// Meta is what devices cannot say about themselves: which room they are in and
// which device they are plugged into. It comes from a JSON file, matched to
// devices by name (case-insensitive).
type Meta struct {
	Rooms      []Room            `json:"rooms"`
	Downstream map[string]string `json:"downstream"` // device name -> name of the device it is plugged into

	once  sync.Once
	lower map[string]string // Downstream lower-cased, built on first use
}

// Room is a titled group; the order in the file is the order on the page.
type Room struct {
	Name    string   `json:"name"`
	Devices []string `json:"devices"`
}

// LoadMeta reads and checks the file. Unknown keys are an error so a typo
// does not silently drop part of the configuration.
func LoadMeta(path string) (*Meta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Meta
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range m.Rooms {
		if strings.TrimSpace(r.Name) == "" {
			return nil, fmt.Errorf("%s: a room has no name", path)
		}
	}
	for d, u := range m.Downstream {
		if strings.EqualFold(d, u) {
			return nil, fmt.Errorf("%s: %q cannot be downstream of itself", path, d)
		}
	}
	return &m, nil
}

// downstream is built once, under the Once, as pages render concurrently.
func (m *Meta) downstream() map[string]string {
	m.once.Do(func() {
		m.lower = make(map[string]string, len(m.Downstream))
		for d, u := range m.Downstream {
			m.lower[strings.ToLower(d)] = strings.ToLower(u)
		}
	})
	return m.lower
}

// Unmatched lists configured names that match no current device (a renamed
// or removed device, or a typo), in file order, without repeats.
func (m *Meta) Unmatched(ds []exporter.DeviceView) []string {
	have := map[string]bool{}
	for _, d := range ds {
		have[nameKey(d)] = true
	}
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		k := strings.ToLower(n)
		if !have[k] && !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	for _, r := range m.Rooms {
		for _, n := range r.Devices {
			add(n)
		}
	}
	for _, d := range sortedKeys(m.Downstream) {
		add(d)
		add(m.Downstream[d])
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
