package exporter

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hughobrien/kasad/internal/kasa"
)

// Handler serves /metrics, /healthz and a plain-text device list at /.
func (e *Exporter) Handler(version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		e.writeMetrics(w, version)
	})
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

type family struct {
	name, help, typ string
	// value returns the sample for a snapshot, or false to omit it.
	value func(snapshot) (float64, bool)
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

var families = []family{
	{"kasa_up", "1 if the last poll of the device succeeded.", "gauge",
		func(s snapshot) (float64, bool) { return b2f(s.ok), true }},
	{"kasa_last_success_timestamp_seconds", "Unix time of the last successful poll.", "gauge",
		func(s snapshot) (float64, bool) {
			if s.lastOK.IsZero() {
				return 0, false
			}
			return float64(s.lastOK.Unix()), true
		}},
	{"kasa_poll_errors_total", "Failed polls since kasad started.", "counter",
		func(s snapshot) (float64, bool) { return float64(s.errors), true }},
	{"kasa_relay_on", "1 if the relay is on.", "gauge",
		func(s snapshot) (float64, bool) {
			if !s.ok || s.last == nil || !s.last.HasState {
				return 0, false
			}
			return b2f(s.last.On), true
		}},
	{"kasa_power_watts", "Instantaneous power draw.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.PowerWatts })},
	{"kasa_voltage_volts", "Mains voltage.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.VoltageVolts })},
	{"kasa_current_amps", "Current draw.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.CurrentAmps })},
	{"kasa_energy_watt_hours_total", "Energy counted by the device; resets when the device does.", "counter", reading(func(r *kasa.Reading) *float64 { return r.EnergyWh })},
	{"kasa_on_time_seconds", "Seconds since the relay was switched on.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.OnTimeSeconds })},
	{"kasa_brightness_percent", "Dimmer brightness.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.Brightness })},
	{"kasa_rssi_dbm", "Wi-Fi signal strength at the device.", "gauge", reading(func(r *kasa.Reading) *float64 { return r.RSSI })},
}

func (e *Exporter) writeMetrics(w io.Writer, version string) {
	snaps := e.snapshot()
	fmt.Fprintf(w, "# HELP kasad_build_info kasad version.\n# TYPE kasad_build_info gauge\nkasad_build_info{version=%s} 1\n", quote(version))
	fmt.Fprintf(w, "# HELP kasad_devices Devices currently tracked.\n# TYPE kasad_devices gauge\nkasad_devices %d\n", len(snaps))
	for _, f := range families {
		var rows strings.Builder
		for _, s := range snaps {
			v, ok := f.value(s)
			if !ok {
				continue
			}
			alias, model := "", ""
			if s.last != nil {
				alias, model = s.last.Alias, s.last.Model
			}
			fmt.Fprintf(&rows, "%s{alias=%s,host=%s,model=%s} %s\n",
				f.name, quote(alias), quote(s.host), quote(model),
				strconv.FormatFloat(v, 'g', -1, 64))
		}
		if rows.Len() == 0 {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s", f.name, f.help, f.name, f.typ, rows.String())
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// quote renders a label value per the Prometheus text format.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
