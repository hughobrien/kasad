// Package ui turns exporter state into the strings the page shows.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
)

// CardView is one device card. Display strings are empty when the value is
// absent (an unmetered switch has no power; a down device has no readings).
type CardView struct {
	ID, Host, Name, Model               string
	Up, On, HasState                    bool
	Power, Today, Month, RSSI, LastSeen string
	Err                                 string
}

// PageView is the whole page.
type PageView struct {
	Cards          []CardView
	TotalPower     string
	UpCount, Total int
}

// NewCard builds the card for one device.
func NewCard(d exporter.DeviceView, now time.Time) CardView {
	c := CardView{
		ID:       "device-" + safeID(d.Host),
		Host:     d.Host,
		Name:     d.Alias,
		Model:    d.Model,
		Up:       d.Up,
		On:       d.On,
		HasState: d.HasState,
		LastSeen: ago(now, d.LastOK),
		Err:      d.Err,
	}
	if c.Name == "" {
		c.Name = d.Host
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

// NewPage builds the page. The total counts only devices that are up.
func NewPage(ds []exporter.DeviceView, now time.Time) PageView {
	p := PageView{Total: len(ds)}
	var total float64
	for _, d := range ds {
		p.Cards = append(p.Cards, NewCard(d, now))
		if d.Up {
			p.UpCount++
			if d.PowerWatts != nil {
				total += *d.PowerWatts
			}
		}
	}
	p.TotalPower = formatW(total)
	return p
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
