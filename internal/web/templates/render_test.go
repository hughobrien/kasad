package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/hughobrien/kasad/internal/web/ui"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func onCard() ui.CardView {
	return ui.CardView{ID: "device-10-0-0-1", Host: "10.0.0.1", Name: "Lamp", Kind: "Socket",
		Up: true, On: true, HasState: true, Power: "5.6 W", Today: "0.003 kWh", Month: "1.19 kWh", RSSI: "-63 dBm"}
}

func TestCardOnShowsReadings(t *testing.T) {
	h := render(t, Card(onCard()))
	for _, want := range []string{`id="device-10-0-0-1"`, "Lamp", "5.6 W", "0.003 kWh", "1.19 kWh", "-63 dBm", `aria-pressed="true"`, "/ui/devices/10.0.0.1/relay?on=false"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
}

func TestCardOffOffersToSwitchOn(t *testing.T) {
	c := onCard()
	c.On = false
	h := render(t, Card(c))
	if !strings.Contains(h, `aria-pressed="false"`) || !strings.Contains(h, "relay?on=true") {
		t.Fatalf("off card:\n%s", h)
	}
}

func TestCardEscapesName(t *testing.T) {
	c := onCard()
	c.Name = `Kid's "<b>Lamp</b>"`
	h := render(t, Card(c))
	if strings.Contains(h, "<b>") || !strings.Contains(h, "&lt;b&gt;Lamp&lt;/b&gt;") {
		t.Fatalf("name not escaped:\n%s", h)
	}
}

func TestCardOfflineDisablesSwitch(t *testing.T) {
	c := ui.CardView{ID: "device-10-0-0-1", Host: "10.0.0.1", Name: "Lamp", Kind: "Socket", LastSeen: "3m ago"}
	h := render(t, Card(c))
	if !strings.Contains(h, "disabled") || !strings.Contains(h, "Offline") || !strings.Contains(h, "3m ago") {
		t.Fatalf("offline card:\n%s", h)
	}
	if strings.Contains(h, " W") || strings.Contains(h, "kWh") {
		t.Fatalf("offline card must not show readings:\n%s", h)
	}
}

func TestCardUnmeteredHidesPower(t *testing.T) {
	c := onCard()
	c.Power, c.Today, c.Month = "", "", ""
	h := render(t, Card(c))
	if strings.Contains(h, "kWh") || strings.Contains(h, "Power") || !strings.Contains(h, "-63 dBm") {
		t.Fatalf("unmetered card:\n%s", h)
	}
}

func TestCardShowsError(t *testing.T) {
	c := onCard()
	c.Err = "set_relay_state: device error_code -1"
	h := render(t, Card(c))
	if !strings.Contains(h, "device error_code -1") || !strings.Contains(h, `role="alert"`) {
		t.Fatalf("error card:\n%s", h)
	}
	if strings.Contains(h, "disabled") {
		t.Fatal("a failed write must leave the switch usable")
	}
}

func TestPageRendersEmptyAndWithCards(t *testing.T) {
	h := render(t, Page(ui.PageView{TotalPower: "0.0 W"}))
	for _, want := range []string{"0 of 0 up", "0.0 W", `id="devices"`, "/ui/sse", `name="build"`} {
		if !strings.Contains(h, want) {
			t.Errorf("empty page missing %q:\n%s", want, h)
		}
	}
	h = render(t, Page(ui.PageView{Sections: []ui.Section{{Cards: []ui.CardView{onCard()}}}, TotalPower: "5.6 W", UpCount: 1, Total: 1}))
	if !strings.Contains(h, "1 of 1 up") || !strings.Contains(h, "Lamp") {
		t.Fatalf("page with a card:\n%s", h)
	}
}

func TestCardDimmerIsASliderWithPercent(t *testing.T) {
	c := onCard()
	c.Dimmable, c.Brightness = true, 46
	h := render(t, Card(c))
	for _, want := range []string{`type="range"`, `min="1"`, `max="100"`, `value="46"`, "data-ignore-morph", "/ui/devices/10.0.0.1/brightness?pct=", `>46%</output>`} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	if strings.Contains(render(t, Card(onCard())), `type="range"`) {
		t.Fatal("a plug must have no percent field")
	}
}

func TestCardDimmerFieldIdChangesWithBrightness(t *testing.T) {
	a, b := onCard(), onCard()
	a.Dimmable, a.Brightness = true, 40
	b.Dimmable, b.Brightness = true, 41
	ha, hb := render(t, Card(a)), render(t, Card(b))
	if !strings.Contains(ha, `id="bright-device-10-0-0-1-40"`) || !strings.Contains(hb, `id="bright-device-10-0-0-1-41"`) {
		t.Fatalf("field ids must carry the stored value:\n%s", ha)
	}
}

func TestPageSectionTitlesAndViaNote(t *testing.T) {
	via := onCard()
	via.Via = "Desk"
	h := render(t, Page(ui.PageView{Sections: []ui.Section{
		{Name: "Study", Cards: []ui.CardView{via}},
		{Cards: []ui.CardView{onCard()}},
	}}))
	if !strings.Contains(h, `<h2 class="room-title">Study</h2>`) || !strings.Contains(h, "via Desk") {
		t.Fatalf("%s", h)
	}
	if strings.Count(h, `class="room-title"`) != 1 {
		t.Fatal("an unnamed section gets no heading")
	}
}
