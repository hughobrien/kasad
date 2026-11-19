package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/kasad/internal/web/templates"
)

type sseEvent struct{ data string }

// connect opens /ui/sse and streams parsed events (and raw keepalive
// comments, as "keepalive") until the context is cancelled.
func connect(t *testing.T, f *fakeDevices) (<-chan string, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(newHandler(f))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ": keepalive"):
				out <- "keepalive"
			case line == "":
				if len(cur) > 0 {
					out <- strings.Join(cur, "\n")
					cur = nil
				}
			default:
				cur = append(cur, line)
			}
		}
	}()
	return out, cancel
}

func next(t *testing.T, ch <-chan string, within time.Duration) (string, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(within):
		return "", false
	}
}

// initial reads events until the build-id script, which ends the opening burst.
func initial(t *testing.T, ch <-chan string) []string {
	t.Helper()
	var evs []string
	for {
		e, ok := next(t, ch, 3*time.Second)
		if !ok {
			t.Fatalf("stream ended before the opening burst finished: %v", evs)
		}
		evs = append(evs, e)
		if strings.Contains(e, "location.reload") {
			return evs
		}
	}
}

func drain(ch <-chan string, d time.Duration) []string {
	var evs []string
	deadline := time.After(d)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, e)
		case <-deadline:
			return evs
		}
	}
}

func TestSSEInitialCards(t *testing.T) {
	ch, _ := connect(t, newFake(dev("10.0.0.1", "Lamp", 5.5), dev("10.0.0.2", "Fan", 300)))
	all := strings.Join(initial(t, ch), "\n")
	for _, want := range []string{"#devices", "device-10-0-0-1", "device-10-0-0-2", "305.5 W", "2 of 2 up"} {
		if !strings.Contains(all, want) {
			t.Errorf("opening burst missing %q:\n%s", want, all)
		}
	}
}

func TestSSEBuildIDScript(t *testing.T) {
	old := templates.BuildID
	templates.BuildID = "build-abc"
	defer func() { templates.BuildID = old }()
	ch, _ := connect(t, newFake(dev("10.0.0.1", "Lamp", 5.5)))
	all := strings.Join(initial(t, ch), "\n")
	if !strings.Contains(all, `"build-abc"`) {
		t.Fatalf("build id not in the reload script:\n%s", all)
	}
}

func TestSSEPatchesOnlyChangedCards(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5), dev("10.0.0.2", "Fan", 300))
	ch, _ := connect(t, f)
	initial(t, ch)

	f.change(dev("10.0.0.1", "Lamp", 6.5), dev("10.0.0.2", "Fan", 300))
	evs := strings.Join(drain(ch, 300*time.Millisecond), "\n")
	if !strings.Contains(evs, "#device-10-0-0-1") || !strings.Contains(evs, "6.5 W") {
		t.Fatalf("changed card not patched:\n%s", evs)
	}
	if strings.Contains(evs, "device-10-0-0-2") {
		t.Fatalf("an unchanged card was re-sent:\n%s", evs)
	}
	if !strings.Contains(evs, "summary") {
		t.Fatalf("the total changed but the summary was not patched:\n%s", evs)
	}

	// A notification with nothing different sends nothing.
	f.change(dev("10.0.0.1", "Lamp", 6.5), dev("10.0.0.2", "Fan", 300))
	if evs := drain(ch, 300*time.Millisecond); len(evs) != 0 {
		t.Fatalf("nothing changed, got %v", evs)
	}
}

func TestSSEMembershipChangeResendsList(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5), dev("10.0.0.2", "Fan", 300))
	ch, _ := connect(t, f)
	initial(t, ch)

	// A device expires: its card must go.
	f.change(dev("10.0.0.1", "Lamp", 5.5))
	evs := strings.Join(drain(ch, 300*time.Millisecond), "\n")
	if !strings.Contains(evs, "#devices") || strings.Contains(evs, "device-10-0-0-2") {
		t.Fatalf("expired device still present:\n%s", evs)
	}

	// A device appears: it shows up in the right place.
	f.change(dev("10.0.0.1", "Lamp", 5.5), dev("10.0.0.3", "Heater", 900))
	evs = strings.Join(drain(ch, 300*time.Millisecond), "\n")
	if !strings.Contains(evs, "device-10-0-0-3") {
		t.Fatalf("new device missing:\n%s", evs)
	}
}

func TestSSEEmptyDoesNotHang(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 50 * time.Millisecond
	defer func() { keepaliveInterval = old }()
	ch, _ := connect(t, newFake())
	all := strings.Join(initial(t, ch), "\n")
	if !strings.Contains(all, "0 of 0 up") {
		t.Fatalf("empty page:\n%s", all)
	}
	for {
		e, ok := next(t, ch, 2*time.Second)
		if !ok {
			t.Fatal("no keepalive on an empty stream")
		}
		if e == "keepalive" {
			return
		}
	}
}

func TestSSEUnsubscribesOnClose(t *testing.T) {
	f := newFake(dev("10.0.0.1", "Lamp", 5.5))
	ch, cancel := connect(t, f)
	initial(t, ch)
	cancel()
	for i := 0; i < 40 && f.unsubscribed.Load() == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if f.unsubscribed.Load() != 1 {
		t.Fatalf("unsubscribed %d times, want 1", f.unsubscribed.Load())
	}
}
