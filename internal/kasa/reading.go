package kasa

import "strings"

// Reading is one poll of one device. Optional measurements are nil when the
// device does not report them (a switch has no power meter).
type Reading struct {
	Alias string
	Model string

	On       bool
	HasState bool

	RSSI          *float64 // dBm
	OnTimeSeconds *float64
	Brightness    *float64 // percent, dimmers only

	PowerWatts   *float64
	VoltageVolts *float64
	CurrentAmps  *float64
	EnergyWh     *float64 // cumulative since the device's counter started

	// Recorded by the device itself, per its own calendar. Nil if it does not
	// report them.
	EnergyTodayWh *float64
	EnergyMonthWh *float64
}

// validUTF8 replaces bytes that are not UTF-8; a device name ends up as a label
// value, and Prometheus refuses those.
func validUTF8(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

// settle cleans the device's strings and applies what is true by definition: with the relay open nothing is
// drawn. A plug's meter lags the relay for a moment after switching off, and a
// stale figure there would be presented as current.
func (r *Reading) settle() *Reading {
	r.Alias, r.Model = validUTF8(r.Alias), validUTF8(r.Model)
	if r.HasState && !r.On {
		zero := func(v **float64) {
			if *v != nil {
				z := 0.0
				*v = &z
			}
		}
		zero(&r.PowerWatts)
		zero(&r.CurrentAmps)
	}
	return r
}
