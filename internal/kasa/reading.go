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
}
