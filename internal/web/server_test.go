package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hughobrien/kasad/internal/web/ui"
)

func newHandler(f *fakeDevices) http.Handler { return newHandlerMeta(f, nil) }

func newHandlerMeta(f *fakeDevices, m *ui.Meta) http.Handler {
	return Handler(f, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("metrics-here"))
	}), m, []string{"kasad.wg", "example.com"})
}

func post(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", target, nil)
	r.Host = "kasad.wg"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var fromPage = map[string]string{"Sec-Fetch-Site": "same-origin"}

func TestIndexRendersCards(t *testing.T) {
	h := newHandler(newFake(dev("10.0.0.1", "Lamp", 5.5), dev("10.0.0.2", "Fan", 300)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	for _, want := range []string{"Lamp", "Fan", "305.5 W", "2 of 2 up", `id="device-10-0-0-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRelayPostCallsSetRelay(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5))
	w := post(newHandler(f), "/ui/devices/10.0.0.1/relay?on=false", fromPage)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if len(f.sets) != 1 || f.sets[0] != "10.0.0.1=false" {
		t.Fatalf("writes = %v", f.sets)
	}
	// The response itself patches the card (dropping its pending style even if the state did not change).
	if !strings.Contains(w.Body.String(), `id="device-10-0-0-1"`) {
		t.Fatalf("no card patch in response:\n%s", w.Body.String())
	}
}

func TestRelayUnknownHost404(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5))
	if w := post(newHandler(f), "/ui/devices/10.9.9.9/relay?on=true", fromPage); w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
	if len(f.sets) != 0 {
		t.Fatal("no write for an unknown host")
	}
}

func TestRelayBadOn400(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5))
	for _, q := range []string{"", "?on=", "?on=maybe", "?on=1"} {
		if w := post(newHandler(f), "/ui/devices/10.0.0.1/relay"+q, fromPage); w.Code != 400 {
			t.Errorf("%q: status %d", q, w.Code)
		}
	}
	if len(f.sets) != 0 {
		t.Fatal("no write for a bad request")
	}
}

func TestRelayRejectsCrossSite(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"cross-site":     {"Sec-Fetch-Site": "cross-site"},
		"same-site":      {"Sec-Fetch-Site": "same-site"},
		"none":           {"Sec-Fetch-Site": "none"},
		"foreign origin": {"Origin": "https://evil.example"},
	} {
		f := newFake(dev("10.0.0.1", "Lamp", 5.5))
		if w := post(newHandler(f), "/ui/devices/10.0.0.1/relay?on=false", hdr); w.Code != 403 {
			t.Errorf("%s: status %d", name, w.Code)
		}
		if len(f.sets) != 0 {
			t.Errorf("%s: a write got through", name)
		}
	}
}

func TestRelayAllowsNoFetchHeadersAndMatchingOrigin(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"curl":                       nil,
		"matching origin":            {"Origin": "https://kasad.wg"},
		"matching origin mixed case": {"Origin": "https://KASAD.WG"},
	} {
		f := newFake(dev("10.0.0.1", "Lamp", 5.5))
		if w := post(newHandler(f), "/ui/devices/10.0.0.1/relay?on=false", hdr); w.Code != 200 {
			t.Errorf("%s: status %d", name, w.Code)
		}
		if len(f.sets) != 1 {
			t.Errorf("%s: write blocked", name)
		}
	}
}

func TestRelayErrorPatchesCard(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5))
	f.setErr = errors.New("boom")
	w := post(newHandler(f), "/ui/devices/10.0.0.1/relay?on=false", fromPage)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "boom") || !strings.Contains(w.Body.String(), `id="device-10-0-0-1"`) {
		t.Fatalf("status %d:\n%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "disabled") {
		t.Fatal("a failed write must leave the switch usable")
	}
}

func TestStaticAssets(t *testing.T) {
	h := newHandler(newFake())
	for path, ctype := range map[string]string{
		"/ui/static/style.css":   "text/css",
		"/ui/static/favicon.svg": "image/svg+xml",
		"/ui/static/datastar.js": "text/javascript",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), ctype) || w.Body.Len() < 100 {
			t.Errorf("%s: status %d, type %q, %d bytes", path, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
}

func TestHostCheck(t *testing.T) {
	for host, want := range map[string]int{
		"192.168.1.10":          200,
		"192.168.1.10:9881":     200,
		"[fd00::1]":             200,
		"[fd00::1]:9881":        200,
		"localhost:9881":        200,
		"LocalHost":             200,
		"KASAD.wg:80":           200,
		"kasad.wg.":             200,
		"attacker.example":      403,
		"attacker.example:9881": 403,
		"":                      403,
	} {
		f := newFake(dev("10.0.0.1", "Lamp", 5.5))
		r := httptest.NewRequest("POST", "/ui/devices/10.0.0.1/relay?on=false", nil)
		r.Host = host
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		newHandler(f).ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%q: status %d, want %d", host, w.Code, want)
		}
		if (want == 200) != (len(f.sets) == 1) {
			t.Errorf("%q: writes %d", host, len(f.sets))
		}
		if want == 403 && !strings.Contains(w.Body.String(), "--allowed-hosts") {
			t.Errorf("%q: body %q does not name the flag", host, w.Body.String())
		}
	}
}

func TestHostCheckCoversPageAndSSEButNotMetrics(t *testing.T) {
	for path, want := range map[string]int{"/": 403, "/ui/sse": 403, "/metrics": 200} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "attacker.example"
		w := httptest.NewRecorder()
		newHandler(newFake()).ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d", path, w.Code, want)
		}
	}
}

func TestRestFallsThrough(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler(newFake()).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Body.String() != "metrics-here" {
		t.Fatalf("body %q", w.Body.String())
	}
}

func TestBrightnessPostCallsSetBrightness(t *testing.T) {
	f := newFake(dimDev("10.0.0.5", "Down", 40))
	w := post(newHandler(f), "/ui/devices/10.0.0.5/brightness?pct=70", fromPage)
	if w.Code != 200 || len(f.brights) != 1 || f.brights[0] != "10.0.0.5=70" {
		t.Fatalf("status %d, writes %v", w.Code, f.brights)
	}
	if !strings.Contains(w.Body.String(), `id="bright-device-10-0-0-5-70"`) {
		t.Fatalf("response must patch the card with the new value:\n%s", w.Body.String())
	}
}

func TestBrightnessRefusals(t *testing.T) {
	f := newFake(dimDev("10.0.0.5", "Down", 40), dev("10.0.0.1", "Lamp", 5))
	h := newHandler(f)
	for target, want := range map[string]int{
		"/ui/devices/10.0.0.5/brightness":         400,
		"/ui/devices/10.0.0.5/brightness?pct=":    400,
		"/ui/devices/10.0.0.5/brightness?pct=0":   400,
		"/ui/devices/10.0.0.5/brightness?pct=101": 400,
		"/ui/devices/10.0.0.5/brightness?pct=-3":  400,
		"/ui/devices/10.0.0.5/brightness?pct=5.5": 400,
		"/ui/devices/10.0.0.5/brightness?pct=abc": 400,
		"/ui/devices/10.9.9.9/brightness?pct=50":  404,
		"/ui/devices/10.0.0.1/brightness?pct=50":  400, // a plug is not a dimmer
	} {
		if w := post(h, target, fromPage); w.Code != want {
			t.Errorf("%s: status %d, want %d", target, w.Code, want)
		}
	}
	if w := post(h, "/ui/devices/10.0.0.5/brightness?pct=50", map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}
	if len(f.brights) != 0 {
		t.Fatalf("a refused request wrote: %v", f.brights)
	}
}

func TestBrightnessErrorPatchesCard(t *testing.T) {
	f := newFake(dimDev("10.0.0.5", "Down", 40))
	f.setErr = errors.New("boom")
	w := post(newHandler(f), "/ui/devices/10.0.0.5/brightness?pct=70", fromPage)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("status %d:\n%s", w.Code, w.Body.String())
	}
}

func TestIndexGroupsByRoomAndDedupesTotal(t *testing.T) {
	m := &ui.Meta{
		Rooms:      []ui.Room{{Name: "Study", Devices: []string{"Desk", "Sound"}}, {Name: "Hall", Devices: []string{"Lamp"}}},
		Downstream: map[string]string{"Sound": "Desk"},
	}
	f := newFake(dev("10.0.0.1", "Desk", 40), dev("10.0.0.2", "Sound", 15), dev("10.0.0.3", "Lamp", 5), dev("10.0.0.4", "Stray", 1))
	w := httptest.NewRecorder()
	newHandlerMeta(f, m).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	order := []string{">Study<", ">Hall<", ">Other<"}
	last := -1
	for _, o := range order {
		i := strings.Index(body, o)
		if i < 0 || i < last {
			t.Fatalf("section %s missing or out of order:\n%s", o, body)
		}
		last = i
	}
	if !strings.Contains(body, "46.0 W") || !strings.Contains(body, "via Desk") {
		t.Fatalf("total should skip Sound (40+5+1) and Sound should say via Desk:\n%s", body)
	}
}

func TestRelayResponseKeepsViaNote(t *testing.T) {
	m := &ui.Meta{Downstream: map[string]string{"Sound": "Desk"}}
	f := newFake(dev("10.0.0.1", "Desk", 40), dev("10.0.0.2", "Sound", 15))
	w := post(newHandlerMeta(f, m), "/ui/devices/10.0.0.2/relay?on=false", fromPage)
	if !strings.Contains(w.Body.String(), "via Desk") {
		t.Fatalf("patched card lost its via note:\n%s", w.Body.String())
	}
}
