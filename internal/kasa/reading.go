package kasa

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

// settle applies what is true by definition: with the relay open nothing is
// drawn. A plug's meter lags the relay for a moment after switching off, and a
// stale figure there would be presented as current.
func (r *Reading) settle() *Reading {
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
