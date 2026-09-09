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

// TestAlwaysFiringIsCaught pins the shapes that must be REJECTED.
//
// 🔴 THE FIRST CASE IS THE REAL ONE, verbatim. It is the expression that shipped in
// v0.15.0 and fired permanently on every instance at severity critical, not a synthetic
// probe written to resemble it. A guard tested only against expressions its own author
// invented is tested against that author's model of the defect.
//
// The rest are the respellings that walked past the regex version of this check before
// it was removed. Each is a different surface and the SAME TREE, which is the whole
// argument for parsing rather than matching.
func TestAlwaysFiringIsCaught(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string // a fragment the diagnosis must contain
	}{
		{
			name: "the shipped DeadLetterStoreLosing expression",
			expr: `sum(rate(devicechain_usermanagement_dead_letters_unstored_total{namespace="devicechain"}[5m])) or vector(0) > 0`,
			want: "left-hand side of the top-level `or`",
		},
		{
			name: "parenthesised vector operand",
			expr: `sum(rate(x[5m])) or (vector(0)) > 0`,
			want: "left-hand side of the top-level `or`",
		},
		{
			name: "space before the vector argument list",
			expr: `sum(rate(x[5m])) or vector (0) > 0`,
			want: "left-hand side of the top-level `or`",
		},
		{
			name: "a PromQL comment after the expression",
			expr: "sum(rate(x[5m])) or vector(0) > 0 # defaulting to zero",
			want: "left-hand side of the top-level `or`",
		},
		{
			name: "the comparison carries bool",
			expr: `sum(rate(x[5m])) > bool 0`,
			want: "`bool` modifier",
		},
		{
			name: "or with the comparison on the left instead",
			expr: `sum(rate(x[5m])) > 0 or vector(0)`,
			want: "right-hand side of the top-level `or`",
		},
		{
			name: "the comparison bound to the divisor",
			expr: `a / (b > 0.5)`,
			want: "top-level operator is `/`",
		},
		{
			name: "no comparison at all",
			expr: `sum(rate(x[5m]))`,
			want: "`sum(...)` is empty exactly when what it aggregates is empty",
		},
		{
			name: "a bare selector",
			expr: `up{job="dc"}`,
			want: "nothing is compared to anything",
		},
		{
			name: "a value-returning function with nothing compared to it",
			expr: `changes(x[5m])`,
			want: "`changes(...)` returns a value rather than filtering",
		},
		{
			name: "vector as an operand of an arithmetic sum",
			expr: `(sum(rate(a[5m])) or vector(0)) + (sum(rate(b[5m])) or vector(0))`,
			want: "top-level operator is `+`",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			why, err := promqlguard.CheckExpr(tc.expr)
			if err != nil {
				t.Fatalf("the expression did not parse, so this case proves nothing: %v", err)
			}
			if why == "" {
				t.Fatalf("accepted an always-firing expression: %s", tc.expr)
			}
			// 🔴 THE DIAGNOSIS IS ASSERTED, NOT JUST THE VERDICT. A guard that rejects
			// everything passes every case above; a guard that rejects the right thing
			// for the wrong reason passes them too, and then misleads whoever has to
			// act on it.
			if !strings.Contains(why, tc.want) {
				t.Fatalf("rejected for the wrong reason.\n  expr:   %s\n  want:   %s\n  got:    %s", tc.expr, tc.want, why)
			}
		})
	}
}

// TestLegitimateAlertsPass is the counterweight, and it carries the same weight as the
// kills above: every one of these is a rule shape this repository ships or would
// legitimately ship, and a guard that flags the corpus is a guard nobody keeps.
//
// `(… or vector(0)) > 0` is the load-bearing one. It is not a near-miss of the defect,
// it is its CORRECT form: without the `or vector(0)` an alert summing several services
// goes absent rather than false the moment one stops being scraped, so a gate that
// discouraged the idiom would cause the failure it exists to prevent.
func TestLegitimateAlertsPass(t *testing.T) {
	exprs := []string{
		// The corrected form of the defect.
		`(sum(rate(devicechain_usermanagement_dead_letters_unstored_total[5m])) or vector(0)) > 0`,
		// The four-way sum of the same idiom, where `+` binds tighter than `>`.
		`(sum(rate(a[5m])) or vector(0)) + (sum(rate(b[5m])) or vector(0)) > 0`,
		// The dead-man's switch.
		`vector(1)`,
		`(vector(1))`,
		// Predicates that are not comparisons.
		`absent(up{job="dc"})`,
		`absent_over_time(up{job="dc"}[10m])`,
		`group by (pod) (a) unless group by (pod) (b)`,
		`max(dc_detect_is_leader) == 1 and on() max(dc_detect_live) == 0`,
		// `or` between two predicates: the shape the issue's own literal rule would
		// have rejected, and which this repository ships today.
		`max(dc_detect_is_leader) == 0 or absent(dc_detect_is_leader)`,
		`max(a) > 0 or max(b) > 0 or max(c) > 0`,
		// Ordinary comparisons, including through parentheses and aggregations.
		`sum(increase(x[30m])) > 0`,
		`(cnpg_last_failed_time - cnpg_last_archived_time) > 1`,
		`(kubelet_volume_stats_available_bytes / kubelet_volume_stats_capacity_bytes) < 0.15`,
		`cnpg_seconds_since_last_archival == -1`,
		`max by (stream) (used_bytes / clamp_min(limit_bytes, 1)) > 0.8`,
		// An aggregation OVER a predicate stays a predicate: an aggregation over an
		// empty vector is empty in Prometheus, unlike SQL.
		`count(up == 0) > 2`,
		`count(up == 0)`,
	}

	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			why, err := promqlguard.CheckExpr(expr)
			if err != nil {
				t.Fatalf("the expression did not parse: %v", err)
			}
			if why != "" {
				t.Fatalf("rejected a legitimate alert expression:\n  expr: %s\n  said: %s", expr, why)
			}
		})
	}
}

// TestUnparseableExpressionIsAnError pins that a broken expression is reported as the
// instrument being unable to answer, NOT as a clean pass and not as a finding.
func TestUnparseableExpressionIsAnError(t *testing.T) {
	why, err := promqlguard.CheckExpr(`up{namespace="dc" > 900`)
	if err == nil {
		t.Fatalf("an unparseable expression returned a verdict (%q) instead of an error", why)
	}
	if !strings.Contains(err.Error(), "nothing can be concluded") {
		t.Fatalf("the error does not say the check could not answer: %v", err)
	}
}

// TestLoadFileCountsEveryAlert covers the extraction, which is where the removed regex
// version of this check actually failed: it looked ahead to `for|labels|annotations|
// record` for the end of an expression, so an alert whose `expr` was the LAST key, or
// was followed by `keep_firing_for:`, was skipped in silence — and it then reported a
// count that read like coverage.
//
// Both of those shapes are in this fixture, and the last alert in it is the defective
// one, so a loader with that bug both misses the defect and reports two alerts.
func TestLoadFileCountsEveryAlert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(`groups:
  - name: fixture
    rules:
      - record: fixture:ratio
        expr: a / b
      - alert: HasEverything
        expr: up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "up is zero"
      - alert: FollowedByKeepFiringFor
        expr: up == 1
        keep_firing_for: 10m
      - alert: ExprIsTheLastKey
        expr: sum(rate(x[5m])) or vector(0) > 0
`), 0o600); err != nil {
		t.Fatal(err)
	}

	alerts, err := promqlguard.LoadFile(path)
	if err != nil {
		t.Fatalf("the fixture did not load: %v", err)
	}
	if len(alerts) != 3 {
		t.Fatalf("read %d alert(s), want 3 (the recording rule is not one): %+v", len(alerts), alerts)
	}
	if alerts[2].Name != "ExprIsTheLastKey" || alerts[2].Group != "fixture" {
		t.Fatalf("the last alert was not located correctly: %+v", alerts[2])
	}

	findings, checked, err := promqlguard.Check([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if checked != 3 {
		t.Fatalf("checked %d alert(s), want 3", checked)
	}
	if len(findings) != 1 || findings[0].Alert.Name != "ExprIsTheLastKey" {
		t.Fatalf("want exactly the last alert flagged, got %+v", findings)
	}
	if !strings.Contains(findings[0].String(), "rules.yaml") {
		t.Fatalf("a finding must name the file it came from: %s", findings[0])
	}
}

// TestCountMismatchIsRefused pins the cross-count. The YAML tree here is truncated to
// one group by a second document, which yaml.Unmarshal reads and the raw bytes do not —
// so the walk sees one alert where the file carries two.
func TestCountMismatchIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(`groups:
  - name: first
    rules:
      - alert: One
        expr: up == 0
---
groups:
  - name: second
    rules:
      - alert: Two
        expr: sum(rate(x[5m])) or vector(0) > 0
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := promqlguard.LoadFile(path); err == nil {
		t.Fatal("a file whose second document was never walked was reported as fully read")
	} else if !strings.Contains(err.Error(), "`alert:` key(s)") {
		t.Fatalf("the diagnosis does not name the disagreeing counts: %v", err)
	}
}

// TestMissingFileIsRefused: an unreadable file must not be silently skipped, which
// would spell "nothing to check" the same way as "nothing wrong".
func TestMissingFileIsRefused(t *testing.T) {
	if _, err := promqlguard.LoadFile(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("a file that does not exist was read as containing no problems")
	}
}
