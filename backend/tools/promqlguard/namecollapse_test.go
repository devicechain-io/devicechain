// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package promqlguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	promqlguard "github.com/devicechain-io/dc-promqlguard"
)

// TestCollapsedNamesAreCaught pins the shapes that must be REJECTED.
//
// 🔴 THE FIRST CASE IS THE REAL ONE, verbatim from v1.8.0-rc.1: ProcessorLoopDropping,
// which errored with "vector cannot contain metrics with the same labelset" on every
// evaluation on a real device-management, never fired, and kept PrometheusRuleFailures
// firing instead.
func TestCollapsedNamesAreCaught(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string
	}{
		{
			name: "the shipped ProcessorLoopDropping expression",
			expr: `sum by (job) (increase(
  {__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total",__name__!="devicechain_devicestate_state_messages_total",result="dropped",namespace="dci-devicechain"}[15m])) > 0`,
			want: "`increase(...)` drops the metric name",
		},
		{
			name: "rate over an alternation",
			expr: `sum(rate({__name__=~"a_total|b_total"}[5m])) > 0`,
			want: `regex "a_total|b_total"`,
		},
		{
			name: "a selector with no name at all",
			expr: `sum by (job) (increase({job="x"}[5m])) > 0`,
			want: "no exact metric name",
		},
		{
			name: "an over_time function that drops the name",
			expr: `max(max_over_time({__name__=~"devicechain_[a-z0-9]+_.+_inflight"}[5m])) > 1`,
			want: "`max_over_time(...)`",
		},
		{
			name: "a subquery that does not carry the name",
			expr: `sum(increase({__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total"}[15m:1m])) > 0`,
			want: "`increase(...)`",
		},
		{
			name: "a subquery whose label_replace copies some other label",
			expr: `sum(increase(label_replace({__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total"}, "x", "$1", "job", "(.+)")[15m:1m])) > 0`,
			want: "`increase(...)`",
		},
		{
			name: "topk keeps the name and passes the collision through",
			expr: `sum(increase(topk(3, {__name__=~"a|b"})[15m:1m])) > 0`,
			want: "`increase(...)`",
		},
		{
			name: "nested inside a comparison on the right",
			expr: `vector(1) and on() (sum(rate({__name__=~".+_total"}[5m])) > 0)`,
			want: "`rate(...)`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			why, err := promqlguard.CheckNameCollapse(tc.expr)
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if why == "" {
				t.Fatalf("not flagged: %s", tc.expr)
			}
			if !strings.Contains(why, tc.want) {
				t.Errorf("diagnosis %q does not contain %q", why, tc.want)
			}
		})
	}
}

// TestDistinctAfterNameDropPasses is the counterweight: every rejection above is
// satisfied just as well by a guard that refuses every range function, so the shapes
// the chart legitimately uses must pass.
func TestDistinctAfterNameDropPasses(t *testing.T) {
	for _, expr := range []string{
		// The fixed ProcessorLoopDropping: the name is carried into a label first.
		`sum by (job) (increase(label_replace(
  {__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total",__name__!="devicechain_devicestate_state_messages_total",result="dropped",namespace="dci-devicechain"},
  "metric", "$1", "__name__", "(.+)")[15m:1m])) > 0`,
		// label_join from the name works too.
		`sum(increase(label_join({__name__=~"a|b"}, "metric", ",", "__name__")[15m:1m])) > 0`,
		// One subsystem segment, fixed suffix: one name per process.
		`sum(rate({__name__=~"devicechain_[a-z0-9]+_dead_letter_lost_total", namespace="x"}[5m])) > 0`,
		`sum by (job) (increase({__name__=~"devicechain_[a-z0-9]+_ratelimit_overflow_admissions_total"}[10m])) > 0`,
		// Exact names, written either way.
		`sum(rate(devicechain_x_total[5m])) > 0`,
		`sum(rate({__name__="devicechain_x_total", job="y"}[5m])) > 0`,
		// A name kept by the function itself.
		`last_over_time({__name__=~"a|b"}[5m]) > 0`,
		`absent_over_time({__name__=~"a|b", job="x"}[5m])`,
		// An aggregation inside a subquery has already removed the name.
		`max_over_time(sum by (job) ({__name__=~"a|b"})[10m:1m]) > 0`,
		// Instant-vector selectors are not this check's concern.
		`max by (stream) ({__name__=~"devicechain_[a-z0-9]+_jetstream_.+"}) > 0`,
	} {
		why, err := promqlguard.CheckNameCollapse(expr)
		if err != nil {
			t.Fatalf("unexpected parse error for %s: %v", expr, err)
		}
		if why != "" {
			t.Errorf("legitimate expression flagged:\n  %s\n  %s", expr, why)
		}
	}
}

// TestCheckReportsCollapsedNamesAsAFinding drives the shipped expression through the
// file-level entry point the gate runs, so the kind and the message reach the caller.
func TestCheckReportsCollapsedNamesAsAFinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := `groups:
  - name: g
    rules:
      - alert: Collapsing
        expr: sum by (job) (increase({__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total",result="dropped"}[15m])) > 0
      - alert: Fine
        expr: sum by (job) (increase(label_replace({__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total",result="dropped"}, "metric", "$1", "__name__", "(.+)")[15m:1m])) > 0
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, checked, err := promqlguard.Check([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if checked != 2 {
		t.Fatalf("checked %d alerts, want 2", checked)
	}
	if len(findings) != 1 || findings[0].Alert.Name != "Collapsing" || findings[0].Kind != promqlguard.CollapsedNames {
		t.Fatalf("want exactly one collapsed-names finding for Collapsing, got %+v", findings)
	}
	if !strings.Contains(findings[0].String(), "cannot be evaluated because") {
		t.Errorf("finding does not say it cannot be evaluated: %s", findings[0])
	}
}
