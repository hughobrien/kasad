package exporter

import (
	"fmt"
	"io"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hughobrien/kasad/internal/kasa"
)

var deviceLabels = []string{"alias", "host", "model"}

// metric describes one per-device family and how to read its sample.
type metric struct {
	desc  *prometheus.Desc
	typ   prometheus.ValueType
	value func(snapshot) (float64, bool)
}

func newMetric(name, help string, typ prometheus.ValueType, value func(snapshot) (float64, bool)) metric {
	return metric{prometheus.NewDesc(name, help, deviceLabels, nil), typ, value}
}

func reading(f func(*kasa.Reading) *float64) func(snapshot) (float64, bool) {
	return func(s snapshot) (float64, bool) {
		// Measurements are dropped while a device is down, so an outage shows
		// as a gap in Prometheus rather than a flat line of stale values.
		if !s.ok || s.last == nil {
			return 0, false
		}
		if v := f(s.last); v != nil {
			return *v, true
		}
		return 0, false
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

const (
	gauge   = prometheus.GaugeValue
	counter = prometheus.CounterValue
)

var metrics = []metric{
	newMetric("kasa_up", "1 if the last poll of the device succeeded.", gauge,
		func(s snapshot) (float64, bool) { return b2f(s.ok), true }),
	newMetric("kasa_last_success_timestamp_seconds", "Unix time of the last successful poll.", gauge,
		func(s snapshot) (float64, bool) {
			if s.lastOK.IsZero() {
				return 0, false
			}
			return float64(s.lastOK.Unix()), true
		}),
	newMetric("kasa_poll_errors_total", "Failed polls since kasad started.", counter,
		func(s snapshot) (float64, bool) { return float64(s.errors), true }),
	newMetric("kasa_relay_on", "1 if the relay is on.", gauge,
		func(s snapshot) (float64, bool) {
			if !s.ok || s.last == nil || !s.last.HasState {
				return 0, false
			}
			return b2f(s.last.On), true
		}),
	newMetric("kasa_power_watts", "Instantaneous power draw.", gauge, reading(func(r *kasa.Reading) *float64 { return r.PowerWatts })),
	newMetric("kasa_voltage_volts", "Mains voltage.", gauge, reading(func(r *kasa.Reading) *float64 { return r.VoltageVolts })),
	newMetric("kasa_current_amps", "Current draw.", gauge, reading(func(r *kasa.Reading) *float64 { return r.CurrentAmps })),
	newMetric("kasa_energy_watt_hours_total", "Energy counted by the device; resets when the device does.", counter, reading(func(r *kasa.Reading) *float64 { return r.EnergyWh })),
	newMetric("kasa_on_time_seconds", "Seconds since the relay was switched on.", gauge, reading(func(r *kasa.Reading) *float64 { return r.OnTimeSeconds })),
	newMetric("kasa_brightness_percent", "Dimmer brightness.", gauge, reading(func(r *kasa.Reading) *float64 { return r.Brightness })),
	newMetric("kasa_rssi_dbm", "Wi-Fi signal strength at the device.", gauge, reading(func(r *kasa.Reading) *float64 { return r.RSSI })),
}

var devicesDesc = prometheus.NewDesc("kasad_devices", "Devices currently tracked.", nil, nil)

// collector turns the device table into samples at scrape time. Building the
// samples fresh each time is what lets a down device's measurements vanish.
type collector struct{ e *Exporter }

func (c collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- devicesDesc
	for _, m := range metrics {
		ch <- m.desc
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	snaps := c.e.snapshot()
	ch <- prometheus.MustNewConstMetric(devicesDesc, prometheus.GaugeValue, float64(len(snaps)))
	for _, s := range snaps {
		alias, model := "", ""
		if s.last != nil {
			alias, model = s.last.Alias, s.last.Model
		}
		for _, m := range metrics {
			if v, ok := m.value(s); ok {
				ch <- prometheus.MustNewConstMetric(m.desc, m.typ, v, alias, s.host, model)
			}
		}
	}
}

// Handler serves /metrics, /healthz and a plain-text device list at /.
func (e *Exporter) Handler(version string) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "kasad_build_info",
			Help:        "kasad version.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
		collector{e},
	)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, s := range e.snapshot() {
			state := "down"
			if s.ok {
				state = "up"
			}
			alias, model := "", ""
			if s.last != nil {
				alias, model = s.last.Alias, s.last.Model
			}
			fmt.Fprintf(w, "%-15s %-7s %-5s %-8s %s\n", s.host, state, s.kind, model, alias)
		}
	})
	return mux
}
