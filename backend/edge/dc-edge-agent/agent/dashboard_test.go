// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/devicechain-io/dc-edge-agent/config"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The edge agent's Grafana dashboard ships as a file (dashboards/edge-agent.json), not in the
// chart: agents run on customer hardware and are scraped by the customer's own Prometheus,
// so nothing in a platform install ever sees these series. That also means the chart's
// dashboard check (hack/check-dashboards.sh, which reads the services' Go registrations) does
// not cover this file. These tests do, against the REAL registry newEdgeMetrics builds, so a
// renamed or retyped metric fails here instead of drawing an empty graph on a site.

const dashboardPath = "../dashboards/edge-agent.json"

// family is what a panel query is checked against: the metric's type and the label names its
// series carry.
type family struct {
	kind   dto.MetricType
	labels map[string]bool
}

// registeredFamilies gathers the registry newEdgeMetrics builds for an agent, exactly as the
// agent exposes it on /metrics.
func registeredFamilies(t *testing.T) map[string]family {
	t.Helper()
	cfg := config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://127.0.0.1:1"},
	}
	a := newAgent(t, cfg, nil)
	m := newEdgeMetrics(a)
	got, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]family{}
	for _, f := range got {
		fam := family{kind: f.GetType(), labels: map[string]bool{}}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				fam.labels[l.GetName()] = true
			}
		}
		out[f.GetName()] = fam
	}
	if len(out) < 15 {
		t.Fatalf("the agent's registry gathered only %d families; the check would pass over nothing", len(out))
	}
	return out
}

// exprs returns every PromQL expression in the document: panel targets and the variables'
// label_values queries.
func exprs(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if s, ok := child.(string); ok && (k == "expr" || k == "definition") {
				*out = append(*out, s)
				continue
			}
			exprs(child, out)
		}
	case []any:
		for _, child := range v {
			exprs(child, out)
		}
	}
}

// metricToken matches the series the agent exports: its own namespace, and the Go runtime and
// process collectors it registers beside them.
var metricToken = regexp.MustCompile(`\b(devicechain_[a-z0-9_]+|go_[a-z0-9_]+|process_[a-z0-9_]+)(\{[^}]*\})?`)

var rateWrapped = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b(rate|irate|increase)\(\s*` + regexp.QuoteMeta(name) + `(\{[^}]*\})?\[`)
}

var selectorLabel = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)`)

// dashboardProblems checks every expression in doc against the registered families and
// returns what is wrong, plus the set of families the dashboard reads. Counters must sit
// inside rate/irate/increase, gauges must not, a name must exist (a histogram's _bucket,
// _sum and _count are accepted only for a histogram), and a selector may name only labels
// the series carry or the ones the scrape adds.
func dashboardProblems(doc []byte, fams map[string]family) (problems []string, used map[string]bool) {
	used = map[string]bool{}
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return []string{"dashboard is not JSON: " + err.Error()}, used
	}
	var all []string
	exprs(root, &all)
	if len(all) == 0 {
		return []string{"the dashboard contains no query at all; this check would pass over nothing"}, used
	}
	scrapeLabels := map[string]bool{"job": true, "instance": true, "__name__": true}
	for _, expr := range all {
		for _, m := range metricToken.FindAllStringSubmatch(expr, -1) {
			name, sel := m[1], m[2]
			base, derived := name, ""
			if _, ok := fams[name]; !ok {
				for _, suf := range []string{"_bucket", "_sum", "_count"} {
					if strings.HasSuffix(name, suf) {
						if f, ok := fams[strings.TrimSuffix(name, suf)]; ok && f.kind == dto.MetricType_HISTOGRAM {
							base, derived = strings.TrimSuffix(name, suf), suf
						}
					}
				}
			}
			f, ok := fams[base]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: no such series is exported by the agent (in %q)", name, expr))
				continue
			}
			used[base] = true
			wrapped := rateWrapped(name).MatchString(expr)
			switch {
			case f.kind == dto.MetricType_COUNTER && !wrapped:
				problems = append(problems, fmt.Sprintf("%s: a counter read without rate/irate/increase (in %q)", name, expr))
			case f.kind == dto.MetricType_GAUGE && wrapped:
				problems = append(problems, fmt.Sprintf("%s: a gauge inside rate/irate/increase (in %q)", name, expr))
			case f.kind == dto.MetricType_HISTOGRAM && derived == "":
				problems = append(problems, fmt.Sprintf("%s: the bare name of a histogram exports no series (in %q)", name, expr))
			}
			for _, l := range selectorLabel.FindAllStringSubmatch(sel, -1) {
				if !f.labels[l[1]] && !scrapeLabels[l[1]] {
					problems = append(problems, fmt.Sprintf("%s: selects the label %q, which its series do not carry (in %q)", name, l[1], expr))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems, used
}

func readDashboard(t *testing.T) []byte {
	t.Helper()
	doc, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("read the dashboard: %v", err)
	}
	return doc
}

// Every query on the dashboard names a series the agent exports, with the right type and
// labels.
func TestEdgeDashboardQueriesOnlyExportedSeries(t *testing.T) {
	fams := registeredFamilies(t)
	problems, _ := dashboardProblems(readDashboard(t), fams)
	if len(problems) > 0 {
		t.Fatalf("dashboards/edge-agent.json has %d problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

// The other direction: every series the agent exports under its own namespace is on the
// dashboard, so a metric added without a panel fails here and gets one. The Go and process
// collectors are not required, only allowed.
func TestEdgeDashboardShowsEveryEdgeSeries(t *testing.T) {
	fams := registeredFamilies(t)
	_, used := dashboardProblems(readDashboard(t), fams)
	var missing []string
	for name := range fams {
		if strings.HasPrefix(name, "devicechain_edge_") && !used[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the agent exports series no panel reads: %s", strings.Join(missing, ", "))
	}
}

// Every panel says what it counts and carries a unit, and the dashboard picks its data source
// through a variable so it imports into any Grafana.
func TestEdgeDashboardPanelConventions(t *testing.T) {
	var doc struct {
		Templating struct {
			List []struct {
				Name string `json:"name"`
			} `json:"list"`
		} `json:"templating"`
		Panels []struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description"`
			FieldConfig struct {
				Defaults struct {
					Unit string `json:"unit"`
				} `json:"defaults"`
			} `json:"fieldConfig"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(readDashboard(t), &doc); err != nil {
		t.Fatal(err)
	}
	vars := map[string]bool{}
	for _, v := range doc.Templating.List {
		vars[v.Name] = true
	}
	for _, want := range []string{"datasource", "job", "instance"} {
		if !vars[want] {
			t.Errorf("missing template variable %q", want)
		}
	}
	if len(doc.Panels) < 10 {
		t.Fatalf("only %d panels; the check would pass over nothing", len(doc.Panels))
	}
	for _, p := range doc.Panels {
		if p.Type == "row" {
			continue
		}
		if p.Description == "" {
			t.Errorf("panel %q has no description", p.Title)
		}
		if (p.Type == "stat" || p.Type == "timeseries") && p.FieldConfig.Defaults.Unit == "" {
			t.Errorf("panel %q has no unit", p.Title)
		}
	}
}

// The checker has to be shown to fail: each defect below is planted ALONE in a copy of the
// real dashboard and must be reported. Without this, a checker that found nothing wrong
// with anything would read as a clean dashboard.
func TestEdgeDashboardCheckerCatchesEachDefect(t *testing.T) {
	fams := registeredFamilies(t)
	base := string(readDashboard(t))
	if problems, _ := dashboardProblems([]byte(base), fams); len(problems) > 0 {
		t.Fatalf("the unmodified dashboard must be clean first: %v", problems)
	}
	cases := []struct {
		name, old, new, want string
	}{
		{"a misspelled series", "devicechain_edge_forwarded_total", "devicechain_edge_forwarded_totl", "no such series"},
		{"a series from another area", "devicechain_edge_dropped_total", "devicechain_eventsources_dropped_total", "no such series"},
		{"a counter without rate", "rate(devicechain_edge_received_total", "sum(devicechain_edge_received_total", "counter read without"},
		{"a gauge inside rate", "devicechain_edge_uplink_inflight{job=~\\\"$job\\\", instance=~\\\"$instance\\\"}", "rate(devicechain_edge_uplink_inflight{job=~\\\"$job\\\", instance=~\\\"$instance\\\"}[5m])", "gauge inside rate"},
		{"a _bucket suffix on a counter", "devicechain_edge_forward_errors_total", "devicechain_edge_forward_errors_total_bucket", "no such series"},
		{"a label the series do not carry", "sum by (reason) (rate(devicechain_edge_refused_total", "sum by (reason) (rate(devicechain_edge_refused_total{rsn=\\\"x\\\"}", "do not carry"},
		{"a Go runtime series that is not registered", "go_goroutines", "go_goroutine_count", "no such series"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(base, c.old) {
				t.Fatalf("the plant site %q is not in the dashboard; the case would test nothing", c.old)
			}
			mutated := strings.Replace(base, c.old, c.new, 1)
			problems, _ := dashboardProblems([]byte(mutated), fams)
			if len(problems) == 0 {
				t.Fatalf("planted %s and nothing was reported", c.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), c.want) {
				t.Fatalf("planted %s; reported %v, want something about %q", c.name, problems, c.want)
			}
		})
	}
	t.Run("an emptied dashboard", func(t *testing.T) {
		if problems, _ := dashboardProblems([]byte(`{"panels":[]}`), fams); len(problems) == 0 {
			t.Fatal("a dashboard with no query passed")
		}
	})
}
