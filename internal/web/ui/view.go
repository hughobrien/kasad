// Package ui turns exporter state into the strings the page shows.
package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
)

// CardView is one device card. Display strings are empty when the value is
// absent (an unmetered switch has no power; a down device has no readings).
type CardView struct {
	ID, Host, Name, Kind                string // Kind: Socket, Light Switch, ...
	Up, On, HasState                    bool
	Dimmable                            bool
	Brightness                          int // percent, 0 when unknown
	Power, Today, Month, RSSI, LastSeen string
	Err                                 string
	Via                                 string // name of the device this one is plugged into, if configured
}

// Section is a titled group of cards. Name is empty when the page is not grouped.
type Section struct {
	Name  string
	Cards []CardView
}

// PageView is the whole page.
type PageView struct {
	Sections       []Section
	TotalPower     string
	UpCount, Total int
}

// NewCard builds the card for one device.
func NewCard(d exporter.DeviceView, now time.Time) CardView {
	c := CardView{
		ID:       "device-" + safeID(d.Host),
		Host:     d.Host,
		Name:     d.Alias,
		Kind:     deviceKind(d.Model, d.Brightness),
		Up:       d.Up,
		On:       d.On,
		HasState: d.HasState,
		LastSeen: ago(now, d.LastOK),
		Err:      d.Err,
	}
	if c.Name == "" {
		c.Name = d.Host
	}
	c.Dimmable = isDimmable(d.Model, d.Brightness)
	if d.Brightness != nil {
		c.Brightness = int(math.Round(*d.Brightness))
	}
	if d.PowerWatts != nil {
		c.Power = formatW(*d.PowerWatts)
	}
	if d.EnergyTodayKWh != nil {
		c.Today = formatKWh(*d.EnergyTodayKWh)
	}
	if d.EnergyMonthKWh != nil {
		c.Month = formatKWh(*d.EnergyMonthKWh)
	}
	if d.RSSI != nil {
		c.RSSI = fmt.Sprintf("%.0f dBm", *d.RSSI)
	}
	return c
}

// NewPage builds the page. The total counts only devices that are up, and
// leaves out a device whose power is already inside its upstream device's
// reading (m.Downstream) while that upstream device is up.
func NewPage(ds []exporter.DeviceView, now time.Time, m *Meta) PageView {
	p := PageView{Total: len(ds)}
	byName := map[string]exporter.DeviceView{}
	for _, d := range ds {
		if k := nameKey(d); byName[k].Host == "" {
			byName[k] = d
		}
	}
	upstream := func(d exporter.DeviceView) (exporter.DeviceView, bool) {
		if m == nil {
			return exporter.DeviceView{}, false
		}
		name, ok := m.downstream()[nameKey(d)]
		if !ok {
			return exporter.DeviceView{}, false
		}
		u, ok := byName[name]
		return u, ok && u.Host != d.Host
	}

	cards := make([]CardView, len(ds))
	var total float64
	for i, d := range ds {
		cards[i] = NewCard(d, now)
		u, has := upstream(d)
		if has {
			cards[i].Via = displayName(u)
		}
		if d.Up {
			p.UpCount++
			if d.PowerWatts != nil && !(has && u.Up) {
				total += *d.PowerWatts
			}
		}
	}
	p.TotalPower = formatW(total)
	p.Sections = group(ds, cards, m)
	return p
}

func displayName(d exporter.DeviceView) string {
	if d.Alias != "" {
		return d.Alias
	}
	return d.Host
}

// nameKey is how configuration names match devices: case-insensitive.
func nameKey(d exporter.DeviceView) string { return strings.ToLower(displayName(d)) }

// group puts cards in rooms, in the configured order, with leftovers in a
// trailing "Other". Without configuration there is one unnamed section.
func group(ds []exporter.DeviceView, cards []CardView, m *Meta) []Section {
	if m == nil || len(m.Rooms) == 0 {
		return []Section{{Cards: cards}}
	}
	roomOf := map[string]int{} // first room that names a device wins
	for i, r := range m.Rooms {
		for _, n := range r.Devices {
			if _, dup := roomOf[strings.ToLower(n)]; !dup {
				roomOf[strings.ToLower(n)] = i
			}
		}
	}
	buckets := make([][]CardView, len(m.Rooms)+1) // last is Other
	for i, d := range ds {
		r, ok := roomOf[nameKey(d)]
		if !ok {
			r = len(m.Rooms)
		}
		buckets[r] = append(buckets[r], cards[i])
	}
	var out []Section
	for i, r := range m.Rooms {
		if len(buckets[i]) > 0 {
			out = append(out, Section{Name: r.Name, Cards: buckets[i]})
		}
	}
	if o := buckets[len(m.Rooms)]; len(o) > 0 {
		out = append(out, Section{Name: "Other", Cards: o})
	}
	if len(out) == 0 {
		out = []Section{{}}
	}
	return out
}

// safeID makes a host usable in an element id and a CSS selector.
func safeID(host string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, host)
}

func formatW(w float64) string { return fmt.Sprintf("%.1f W", w) }

// formatKWh keeps small daily figures readable: 0.003 kWh, 1.19 kWh, 123.5 kWh.
func formatKWh(v float64) string {
	switch {
	case v < 1:
		return fmt.Sprintf("%.3f kWh", v)
	case v < 100:
		return fmt.Sprintf("%.2f kWh", v)
	}
	return fmt.Sprintf("%.1f kWh", v)
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func isDimmable(model string, brightness *float64) bool {
	m := strings.ToUpper(model)
	return brightness != nil || strings.HasPrefix(m, "KS220") || strings.HasPrefix(m, "HS220")
}

// deviceKind says what a device is, not which model. An unrecognised model is
// shown as is rather than mislabelled.
func deviceKind(model string, brightness *float64) string {
	m := strings.ToUpper(model)
	switch {
	case isDimmable(model, brightness):
		return "Light Switch (Dimmable)"
	case strings.HasPrefix(m, "KS200"), strings.HasPrefix(m, "HS200"):
		return "Light Switch"
	case strings.HasPrefix(m, "KP"), strings.HasPrefix(m, "EP"), strings.HasPrefix(m, "HS1"):
		return "Socket"
	}
	return model
}
