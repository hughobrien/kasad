package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/hughobrien/kasad/internal/web/templates"
	"github.com/hughobrien/kasad/internal/web/ui"
)

var keepaliveInterval = 30 * time.Second

func renderString(ctx context.Context, c templ.Component) string {
	var b bytes.Buffer
	_ = c.Render(ctx, &b)
	return b.String()
}

// stream remembers what this client has been sent, so only differences go out.
type stream struct {
	ids     []string
	cards   map[string]string // card id -> rendered HTML
	summary string
}

func (s *server) sse(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Debug("sse: clear write deadline", "err", err)
	}
	w.Header().Set("X-Accel-Buffering", "no")
	sse := datastar.NewSSE(w, r)

	// Subscribe before the first read so a change in between is not lost.
	events, unsub := s.d.Subscribe()
	defer unsub()

	st := &stream{}
	if err := s.push(r.Context(), sse, st); err != nil {
		return
	}
	// A tab opened before a deploy reloads when the stream reconnects to the new daemon.
	if err := sse.ExecuteScript(fmt.Sprintf(`var m=document.querySelector('meta[name=build]');if(m&&m.content!==%q)location.reload()`, templates.BuildID)); err != nil {
		return
	}

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-events:
			if err := s.push(r.Context(), sse, st); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// push sends what changed since the last push: the whole grid when devices
// came or went (or on the first call), otherwise only the cards whose
// rendering changed, plus the summary line when it did.
func (s *server) push(ctx context.Context, sse *datastar.ServerSentEventGenerator, st *stream) error {
	page := ui.NewPage(s.d.Devices(), s.now(), s.meta)

	// ids carries the layout: a section marker before its cards, so a device
	// moving between rooms (or a room appearing) changes the list.
	var ids []string
	var cards []ui.CardView
	html := map[string]string{}
	for _, sec := range page.Sections {
		ids = append(ids, "§"+sec.Name)
		for _, c := range sec.Cards {
			ids = append(ids, c.ID)
			cards = append(cards, c)
			html[c.ID] = renderString(ctx, templates.Card(c))
		}
	}

	if st.cards == nil || !slices.Equal(ids, st.ids) {
		if err := sse.PatchElementTempl(templates.Devices(page.Sections),
			datastar.WithSelector("#devices"), datastar.WithModeOuter()); err != nil {
			return err
		}
	} else {
		for _, c := range cards {
			if html[c.ID] == st.cards[c.ID] {
				continue
			}
			if err := sse.PatchElementTempl(templates.Card(c),
				datastar.WithSelectorf("#%s", c.ID), datastar.WithModeOuter()); err != nil {
				return err
			}
		}
	}
	st.ids, st.cards = ids, html

	if sum := renderString(ctx, templates.Summary(page)); sum != st.summary {
		if err := sse.PatchElementTempl(templates.Summary(page),
			datastar.WithSelector("#summary"), datastar.WithModeOuter()); err != nil {
			return err
		}
		st.summary = sum
	}
	return nil
}
