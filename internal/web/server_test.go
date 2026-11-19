package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHandler(f *fakeDevices) http.Handler {
	return Handler(f, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("metrics-here"))
	}))
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
		"curl":            nil,
		"matching origin": {"Origin": "https://kasad.wg"},
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

func TestRestFallsThrough(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler(newFake()).ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Body.String() != "metrics-here" {
		t.Fatalf("body %q", w.Body.String())
	}
}
