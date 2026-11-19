package exporter

import (
	"sort"
	"strings"
	"time"
)

// DeviceView is what the web UI shows about one device. Measurements are nil
// while the device is down, as in the metrics, and state is unknown then.
type DeviceView struct {
	Host, Alias, Model string
	Up, On, HasState   bool

	PowerWatts     *float64
	EnergyTodayKWh *float64 // as recorded by the device itself
	EnergyMonthKWh *float64
	RSSI           *float64

	LastOK time.Time
	Err    string // last failed write, empty once a poll succeeds
}

// Devices returns every tracked device, sorted by name (a device that has
// never answered sorts by its address), then address.
func (e *Exporter) Devices() []DeviceView {
	e.mu.Lock()
	out := make([]DeviceView, 0, len(e.devices))
	for _, d := range e.devices {
		v := DeviceView{Host: d.host, Up: d.ok, LastOK: d.lastOK, Err: d.writeErr}
		if r := d.last; r != nil {
			v.Alias, v.Model = r.Alias, r.Model
			if d.ok {
				v.On, v.HasState, v.RSSI = r.On, r.HasState, r.RSSI
				v.PowerWatts = r.PowerWatts
				v.EnergyTodayKWh = whToKWh(r.EnergyTodayWh)
				v.EnergyMonthKWh = whToKWh(r.EnergyMonthWh)
			}
		}
		out = append(out, v)
	}
	e.mu.Unlock()

	key := func(v DeviceView) string {
		if v.Alias != "" {
			return strings.ToLower(v.Alias)
		}
		return v.Host
	}
	sort.Slice(out, func(i, j int) bool {
		if ki, kj := key(out[i]), key(out[j]); ki != kj {
			return ki < kj
		}
		return out[i].Host < out[j].Host
	})
	return out
}

func whToKWh(wh *float64) *float64 {
	if wh == nil {
		return nil
	}
	v := *wh / 1000
	return &v
}
