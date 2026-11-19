package exporter

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Grafana dashboard in grafana/kasad.json queries these metrics. If a
// metric is renamed or dropped the dashboard silently shows nothing, so check
// every kasa_ name it uses still exists, and that it stays importable
// (datasource is an input, not a UID from one particular Grafana).
func TestDashboardMatchesMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../grafana/kasad.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash map[string]any
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatalf("dashboard is not valid JSON: %v", err)
	}

	have := map[string]bool{}
	name := regexp.MustCompile(`fqName: "([a-z_]+)"`)
	for _, m := range metrics {
		if g := name.FindStringSubmatch(m.desc.String()); g != nil {
			have[g[1]] = true
		}
	}
	if len(have) == 0 {
		t.Fatal("found no metric names; the pattern for Desc.String() is stale")
	}

	used := map[string]bool{}
	use := regexp.MustCompile(`\bkasa_[a-z_]+\b`)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if e, ok := x["expr"].(string); ok {
				for _, n := range use.FindAllString(e, -1) {
					used[n] = true
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(dash)
	if len(used) == 0 {
		t.Fatal("the dashboard queries no kasa_ metrics")
	}
	for n := range used {
		if !have[n] {
			t.Errorf("dashboard uses %s, which the exporter does not export", n)
		}
	}

	// Portable: only the datasource input and Grafana's built-in annotation source.
	for _, m := range regexp.MustCompile(`"uid":\s*"([^"]*)"`).FindAllStringSubmatch(string(raw), -1) {
		switch m[1] {
		case "${DS_PROMETHEUS}", "-- Grafana --", "kasad":
		default:
			t.Errorf("dashboard hard-codes uid %q; use the ${DS_PROMETHEUS} input", m[1])
		}
	}
	if !strings.Contains(string(raw), `"__inputs"`) {
		t.Error("dashboard has no __inputs, so Grafana will not ask which datasource to use")
	}
}
