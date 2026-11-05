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

	"github.com/prometheus/client_golang/prometheus"
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

var (
	descName   = regexp.MustCompile(`fqName: "([^"]+)"`)
	descLabels = regexp.MustCompile(`variableLabels: \{([^}]*)\}`)
)

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// familiesOf reads a registry two ways and merges them. Gather sees only series that exist,
// and a labelled metric (a Vec) with no child yet gathers nothing, so a new Vec would be
// invisible to the coverage check until something incremented it. Describe lists every
// collector's descriptor whether or not it has a series, and carries the label names. The type
// comes from Gather when there is a series and is inferred from the name otherwise (_total is
// a counter, anything else a gauge), which is the convention this package follows.
func familiesOf(t *testing.T, reg *prometheus.Registry) map[string]family {
	t.Helper()
	got, err := reg.Gather()
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
	ch := make(chan *prometheus.Desc)
	go func() { reg.Describe(ch); close(ch) }()
	for d := range ch {
		text := d.String()
		n := descName.FindStringSubmatch(text)
		if n == nil {
			t.Fatalf("cannot read the name of descriptor %s", text)
		}
		fam, ok := out[n[1]]
		if !ok {
			kind := dto.MetricType_GAUGE
			if strings.HasSuffix(n[1], "_total") {
				kind = dto.MetricType_COUNTER
			}
			fam = family{kind: kind, labels: map[string]bool{}}
		}
		if l := descLabels.FindStringSubmatch(text); l != nil {
			for _, name := range splitList(l[1]) {
				fam.labels[name] = true
			}
		}
		out[n[1]] = fam
	}
	if len(out) < 15 {
		t.Fatalf("the registry described only %d families; the check would pass over nothing", len(out))
	}
	return out
}

// registeredFamilies is familiesOf for the registry newEdgeMetrics builds for an agent.
func registeredFamilies(t *testing.T) map[string]family {
	t.Helper()
	cfg := config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://127.0.0.1:1"},
	}
	a := newAgent(t, cfg, nil)
	return familiesOf(t, newEdgeMetrics(a).reg)
}

// exprs returns every PromQL expression in the document: panel targets, and for a template
// variable both the definition Grafana displays and query.query, the string that actually runs.
func exprs(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			switch c := child.(type) {
			case string:
				if k == "expr" || k == "definition" {
					*out = append(*out, c)
				}
			case map[string]any:
				if q, ok := c["query"].(string); ok && k == "query" {
					*out = append(*out, q)
				}
				exprs(c, out)
			default:
				exprs(c, out)
			}
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

// rateOpen matches the call a counter must sit directly inside, ending where the series starts.
var rateOpen = regexp.MustCompile(`\b(rate|irate|increase)\(\s*$`)

// grouping matches the label lists of by, without, on, ignoring and the group modifiers.
var grouping = regexp.MustCompile(`\b(by|without|on|ignoring|group_left|group_right)\s*\(([^)]*)\)`)

var selectorLabel = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)`)

// dashboardProblems checks every expression in doc against the families and returns what is
// wrong, plus the set of families the dashboard reads. Counters must sit inside
// rate/irate/increase (checked per occurrence), gauges must not, a name must exist (a
// histogram's _bucket, _sum and _count are accepted only for a histogram), and a selector or a
// by/without/on/ignoring list may name only labels the series in that expression carry or the
// ones the scrape adds.
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
	scrapeLabels := map[string]bool{"agent": true, "job": true, "instance": true, "__name__": true}
	for _, expr := range all {
		carried := map[string]bool{}
		for _, m := range metricToken.FindAllStringSubmatchIndex(expr, -1) {
			name := expr[m[2]:m[3]]
			sel := ""
			if m[4] >= 0 {
				sel = expr[m[4]:m[5]]
			}
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
			for l := range f.labels {
				carried[l] = true
			}
			// Per occurrence: a counter read bare beside a wrapped one is still wrong.
			wrapped := rateOpen.MatchString(expr[:m[0]]) && strings.HasPrefix(expr[m[1]:], "[")
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
		for _, g := range grouping.FindAllStringSubmatch(expr, -1) {
			for _, l := range splitList(g[2]) {
				if !carried[l] && !scrapeLabels[l] && l != "le" {
					problems = append(problems, fmt.Sprintf("%s (...): groups by the label %q, which no series in the expression carries (in %q)", g[1], l, expr))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems, used
}

// unreadSeries lists the agent's own series no panel reads.
func unreadSeries(fams map[string]family, used map[string]bool) []string {
	var missing []string
	for name := range fams {
		if strings.HasPrefix(name, "devicechain_edge_") && !used[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
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
// dashboard, so a metric added without a panel fails here and gets one, including a labelled
// metric nothing has incremented yet. The Go and process collectors are not required, only
// allowed.
func TestEdgeDashboardShowsEveryEdgeSeries(t *testing.T) {
	fams := registeredFamilies(t)
	_, used := dashboardProblems(readDashboard(t), fams)
	if missing := unreadSeries(fams, used); len(missing) > 0 {
		t.Fatalf("the agent exports series no panel reads: %s", strings.Join(missing, ", "))
	}
}

// The coverage check has to see a labelled metric that has no series yet; Gather alone does
// not, which is why familiesOf also reads the descriptors.
func TestEdgeDashboardCoverageSeesALabelledMetricWithNoChildren(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Enough ordinary families to clear familiesOf's emptiness floor.
	for i := 0; i < 15; i++ {
		reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: fmt.Sprintf("filler_%d", i), Help: "x"}))
	}
	reg.MustRegister(prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devicechain_edge_new_vec_total", Help: "x",
	}, []string{"reason"}))
	fams := familiesOf(t, reg)
	f, ok := fams["devicechain_edge_new_vec_total"]
	if !ok || !f.labels["reason"] || f.kind != dto.MetricType_COUNTER {
		t.Fatalf("a childless CounterVec was not described with its label and type: %+v", f)
	}
	if got := unreadSeries(fams, map[string]bool{}); len(got) != 1 {
		t.Fatalf("a new labelled metric with no panel was not reported: %v", got)
	}
}

// Every panel says what it counts and carries a unit, and the dashboard picks its data source
// and its agents through variables so it imports into any Grafana.
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
	for _, want := range []string{"datasource", "agent", "lookback"} {
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
// real dashboard and must be reported. Without this, a checker that found nothing wrong with
// anything would read as a clean dashboard.
func TestEdgeDashboardCheckerCatchesEachDefect(t *testing.T) {
	fams := registeredFamilies(t)
	base := string(readDashboard(t))
	if problems, _ := dashboardProblems([]byte(base), fams); len(problems) > 0 {
		t.Fatalf("the unmodified dashboard must be clean first: %v", problems)
	}
	const recv = "sum(rate(devicechain_edge_received_total{agent=~\\\"$agent\\\"}[$__rate_interval]))"
	cases := []struct {
		name, old, new, want string
	}{
		{"a misspelled series", "devicechain_edge_forwarded_total", "devicechain_edge_forwarded_totl", "no such series"},
		{"a series from another area", "devicechain_edge_dropped_total", "devicechain_eventsources_dropped_total", "no such series"},
		{"a counter without rate", "rate(devicechain_edge_received_total", "sum(devicechain_edge_received_total", "counter read without"},
		{"a counter bare beside a wrapped one", recv, recv + " + sum(devicechain_edge_received_total{agent=~\\\"$agent\\\"})", "counter read without"},
		{"a gauge inside rate", "devicechain_edge_uplink_inflight{agent=~\\\"$agent\\\"}", "rate(devicechain_edge_uplink_inflight{agent=~\\\"$agent\\\"}[5m])", "gauge inside rate"},
		{"a _bucket suffix on a counter", "devicechain_edge_forward_errors_total", "devicechain_edge_forward_errors_total_bucket", "no such series"},
		{"a selector label the series do not carry", "sum by (reason) (rate(devicechain_edge_refused_total", "sum by (reason) (rate(devicechain_edge_refused_total{rsn=\\\"x\\\"}", "do not carry"},
		{"a grouping label the series do not carry", "sum by (reason) (rate(devicechain_edge_refused_total", "sum by (rsn) (rate(devicechain_edge_refused_total", "groups by the label \"rsn\""},
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
	// The executed query of a template variable, not just the definition shown beside it.
	t.Run("a misspelled series in a variable's query.query", func(t *testing.T) {
		var root map[string]any
		if err := json.Unmarshal([]byte(base), &root); err != nil {
			t.Fatal(err)
		}
		planted := false
		for _, v := range root["templating"].(map[string]any)["list"].([]any) {
			vm := v.(map[string]any)
			if q, ok := vm["query"].(map[string]any); ok {
				q["query"] = strings.Replace(q["query"].(string), "uplink_connected", "uplink_connectd", 1)
				planted = true
			}
		}
		if !planted {
			t.Fatal("no variable has a query.query; the case would test nothing")
		}
		doc, _ := json.Marshal(root)
		if problems, _ := dashboardProblems(doc, fams); len(problems) == 0 {
			t.Fatal("a misspelled series in query.query was not reported")
		}
	})
	t.Run("an emptied dashboard", func(t *testing.T) {
		if problems, _ := dashboardProblems([]byte(`{"panels":[]}`), fams); len(problems) == 0 {
			t.Fatal("a dashboard with no query passed")
		}
	})
}
