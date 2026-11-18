// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// These counters carry governance.ShedClassOf's four bounded classes, NOT the
// operator-defined tier name or tenant ID. Keep totals, but never let a board
// querying one of them lose its companion breakdown by class (ADR-063 P4).
var tierMetrics = []string{
	"devicechain_eventsources_total_msg_rate_limited",
	"devicechain_eventsources_total_msg_reading_limited",
	"devicechain_eventsources_total_readings_rate_limited",
}

type tierTarget struct {
	Expr         string `json:"expr"`
	LegendFormat string `json:"legendFormat"`
	Hide         bool   `json:"hide"`
}

type tierPanel struct {
	Type    string       `json:"type"`
	Targets []tierTarget `json:"targets"`
	Panels  []tierPanel  `json:"panels"`
	Options struct {
		Legend struct {
			DisplayMode string `json:"displayMode"`
			ShowLegend  *bool  `json:"showLegend"`
		} `json:"legend"`
	} `json:"options"`
}

// Recognize the deliberately narrow board convention, rather than guessing
// whether arbitrary PromQL preserves labels. In particular, an outer sum or a
// label matcher alone must not count as a breakdown. New query forms require an
// explicit extension here with negative controls.
func tierBreakdown(target tierTarget, metric string) bool {
	pattern := `^\s*sum\s+by\s*\(([^()]*)\)\s*\(\s*rate\s*\(\s*` + regexp.QuoteMeta(metric) +
		`\{namespace="\$namespace"\}\[\$__rate_interval\]\s*\)\s*\)\s*$`
	match := regexp.MustCompile(pattern).FindStringSubmatch(target.Expr)
	if match == nil {
		return false
	}
	labels := map[string]bool{}
	for _, label := range strings.Split(match[1], ",") {
		label = strings.TrimSpace(label)
		if labels[label] || (label != "source" && label != "shed_class") {
			return false
		}
		labels[label] = true
	}
	return labels["shed_class"] && strings.Contains(target.LegendFormat, "{{shed_class}}")
}

func tierCoverage(panels []tierPanel) error {
	used, covered := map[string]bool{}, map[string]bool{}
	var walk func([]tierPanel)
	walk = func(panels []tierPanel) {
		for _, panel := range panels {
			legend := panel.Options.Legend
			legendVisible := legend.DisplayMode != "hidden" && (legend.ShowLegend == nil || *legend.ShowLegend)
			for _, target := range panel.Targets {
				for _, metric := range tierMetrics {
					if regexp.MustCompile(`\b` + metric + `\b`).MatchString(target.Expr) {
						used[metric] = true
					}
					if panel.Type == "timeseries" && legendVisible && !target.Hide && tierBreakdown(target, metric) {
						covered[metric] = true
					}
				}
			}
			walk(panel.Panels)
		}
	}
	walk(panels)
	for _, metric := range tierMetrics {
		if used[metric] && !covered[metric] {
			return fmt.Errorf("%s needs a visible timeseries breakdown by shed_class with a class legend on this board", metric)
		}
	}
	return nil
}

func TestDashboardTierCoverage(t *testing.T) {
	chart := HelmChart()
	paths, err := fs.Glob(chart, "dashboards/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("discover dashboards: %v (%d files)", err, len(paths))
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := fs.ReadFile(chart, path)
			if err != nil {
				t.Fatal(err)
			}
			var board struct {
				Panels []tierPanel `json:"panels"`
			}
			if err := json.Unmarshal(raw, &board); err != nil {
				t.Fatal(err)
			}
			if err := tierCoverage(board.Panels); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTierCoverageNegativeControls(t *testing.T) {
	metric := tierMetrics[0]
	expr := `sum by (source, shed_class) (rate(` + metric + `{namespace="$namespace"}[$__rate_interval]))`
	for _, tc := range []struct {
		name   string
		change func(*tierPanel)
	}{
		{"class aggregated away", func(p *tierPanel) { p.Targets[0].Expr = strings.ReplaceAll(expr, "source, shed_class", "source") }},
		{"outer sum", func(p *tierPanel) { p.Targets[0].Expr = "sum(" + expr + ")" }},
		{"class absent from legend", func(p *tierPanel) { p.Targets[0].LegendFormat = "{{source}}" }},
		{"hidden target", func(p *tierPanel) { p.Targets[0].Hide = true }},
		{"hidden legend", func(p *tierPanel) { p.Options.Legend.DisplayMode = "hidden" }},
		{"disabled legend", func(p *tierPanel) { disabled := false; p.Options.Legend.ShowLegend = &disabled }},
		{"stat instead of breakdown", func(p *tierPanel) { p.Type = "stat" }},
		{"tenant grouping", func(p *tierPanel) { p.Targets[0].Expr = strings.ReplaceAll(expr, "source,", "tenant_id,") }},
		{"literal namespace", func(p *tierPanel) { p.Targets[0].Expr = strings.ReplaceAll(expr, "$namespace", "other-instance") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panel := tierPanel{Type: "timeseries", Targets: []tierTarget{{Expr: expr, LegendFormat: "{{source}} / {{shed_class}}"}}}
			if err := tierCoverage([]tierPanel{panel}); err != nil {
				t.Fatalf("valid breakdown rejected: %v", err)
			}
			tc.change(&panel)
			if err := tierCoverage([]tierPanel{panel}); err == nil {
				t.Fatal("broken breakdown accepted")
			}
		})
	}
}

func TestTierCoverageCompanions(t *testing.T) {
	for _, metric := range tierMetrics {
		t.Run(metric, func(t *testing.T) {
			total := tierPanel{Type: "stat", Targets: []tierTarget{{Expr: "sum(" + metric + ")"}}}
			breakdown := tierPanel{Type: "timeseries", Targets: []tierTarget{{
				Expr: `sum by (shed_class, source) (rate(` + metric + `{namespace="$namespace"}[$__rate_interval]))`, LegendFormat: "{{shed_class}}",
			}}}
			if err := tierCoverage([]tierPanel{total}); err == nil {
				t.Fatal("total without a breakdown accepted")
			}
			if err := tierCoverage([]tierPanel{total, {Type: "row", Panels: []tierPanel{breakdown}}}); err != nil {
				t.Fatalf("total with a nested breakdown rejected: %v", err)
			}
			// A different metric's breakdown cannot cover this metric's total.
			breakdown.Targets[0].Expr = strings.ReplaceAll(breakdown.Targets[0].Expr, metric, metric+"_other")
			if err := tierCoverage([]tierPanel{total, breakdown}); err == nil {
				t.Fatal("another metric's breakdown accepted")
			}
		})
	}
	if err := tierCoverage([]tierPanel{{Type: "timeseries", Targets: []tierTarget{{Expr: "sum(up)", LegendFormat: "up"}}}}); err != nil {
		t.Fatalf("board with no class-labelled metrics rejected: %v", err)
	}
}
