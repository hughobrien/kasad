// Package web serves the control page: a card per device, a live event
// stream, and the switch.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/starfederation/datastar-go/datastar"

	"github.com/hughobrien/kasad/internal/exporter"
	"github.com/hughobrien/kasad/internal/web/templates"
	"github.com/hughobrien/kasad/internal/web/ui"
)

// Devices is what the page needs from the exporter.
type Devices interface {
	Devices() []exporter.DeviceView
	SetRelay(ctx context.Context, host string, on bool) error
	SetBrightness(ctx context.Context, host string, pct int) error
	Subscribe() (<-chan struct{}, func())
}

type server struct {
	d   Devices
	now func() time.Time
}

// Handler serves the page and its routes; every other path goes to rest
// (the metrics handler).
func Handler(d Devices, rest http.Handler) http.Handler {
	s := &server{d: d, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /ui/sse", s.sse)
	mux.HandleFunc("POST /ui/devices/{host}/relay", s.relay)
	mux.HandleFunc("POST /ui/devices/{host}/brightness", s.brightness)
	mux.HandleFunc("GET /ui/static/style.css", serveAsset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /ui/static/favicon.svg", serveAsset("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /ui/static/datastar.js", serveAsset(DatastarFile, "text/javascript; charset=utf-8"))
	mux.Handle("/", rest)
	return mux
}

func serveAsset(file, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assetFS.ReadFile(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.Page(ui.NewPage(s.d.Devices(), s.now())).Render(r.Context(), w); err != nil {
		slog.Debug("render index", "err", err)
	}
}

// sameOrigin rejects requests another website could have caused a browser on
// this network to make. Browsers send Sec-Fetch-Site; older clients may send
// only Origin; tools like curl send neither and are allowed.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

func (s *server) relay(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	var on bool
	switch r.URL.Query().Get("on") {
	case "true":
		on = true
	case "false":
	default:
		http.Error(w, "on must be true or false", http.StatusBadRequest)
		return
	}
	host := r.PathValue("host")
	if !s.known(host) {
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}

	if err := s.d.SetRelay(r.Context(), host, on); err != nil {
		slog.Warn("switch failed", "host", host, "on", on, "err", err)
	}
	s.patchCard(w, r, host)
}

// patchCard answers with the card as it now stands (carrying the error, if
// any): that also drops the click's pending style when nothing changed.
func (s *server) patchCard(w http.ResponseWriter, r *http.Request, host string) {
	sse := datastar.NewSSE(w, r)
	if v, ok := s.find(host); ok {
		c := ui.NewCard(v, s.now())
		_ = sse.PatchElementTempl(templates.Card(c), datastar.WithSelectorf("#%s", c.ID), datastar.WithModeOuter())
	}
}

func (s *server) known(host string) bool {
	_, ok := s.find(host)
	return ok
}

func (s *server) find(host string) (exporter.DeviceView, bool) {
	for _, v := range s.d.Devices() {
		if v.Host == host {
			return v, true
		}
	}
	return exporter.DeviceView{}, false
}

func (s *server) brightness(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	pct, err := strconv.Atoi(r.URL.Query().Get("pct"))
	if err != nil || pct < 1 || pct > 100 {
		http.Error(w, "pct must be a whole number from 1 to 100", http.StatusBadRequest)
		return
	}
	host := r.PathValue("host")
	v, ok := s.find(host)
	if !ok {
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	if !ui.NewCard(v, s.now()).Dimmable {
		http.Error(w, "device is not a dimmer", http.StatusBadRequest)
		return
	}
	if err := s.d.SetBrightness(r.Context(), host, pct); err != nil {
		slog.Warn("brightness failed", "host", host, "pct", pct, "err", err)
	}
	s.patchCard(w, r, host)
}
