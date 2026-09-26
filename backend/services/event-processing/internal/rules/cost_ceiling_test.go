// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// The expression cost ceiling is a platform constant: one value for every tenant, with no setting
// that changes it. These tests pin that value at every place a rule carries a cost-bearing CEL
// expression. The ceiling is written as the literal 100 rather than predicate.CostCeiling, so
// changing the platform value is a deliberate edit in two places and not one the tests follow.

// TestTheLeafIsGatedAtThePlatformCeiling: a rule's condition is refused above 100, with the typed
// error an author's tooling can tell apart from a type error, reporting the ceiling it was held to.
func TestTheLeafIsGatedAtThePlatformCeiling(t *testing.T) {
	_, err := Compile(Rule{ID: "r", Name: "every", Type: TypeThreshold,
		When: Condition{CEL: `m.all(k, m[k] > 0.0)`}}, Limits{})
	var ce *predicate.CostError
	if !errors.As(err, &ce) {
		t.Fatalf("want a *predicate.CostError for a condition iterating every measurement, got %v", err)
	}
	if ce.Ceiling != 100 {
		t.Fatalf("the condition was gated at a ceiling of %d, want the platform's 100", ce.Ceiling)
	}
	if ce.EstimatedMax <= 100 {
		t.Fatalf("the refusal reports an estimate of %d, which is within the ceiling it was refused at", ce.EstimatedMax)
	}
}

// TestEveryCELSiteInARuleIsGatedAtThePlatformCeiling covers the other three cost-bearing
// expressions a rule can carry: an action guard, a connector payload template and an alarm-key
// template. Each is gated separately inside Compile, so each can drift separately.
//
// No estimator figure is pinned. For each site the test grows an expression one term at a time
// until the site's own compiler, ungated, estimates it above the ceiling; that expression must be
// refused by Compile and the one a term shorter must compile. The step must also land below the
// runtime backstop, or the test could not tell the ceiling from the backstop.
func TestEveryCELSiteInARuleIsGatedAtThePlatformCeiling(t *testing.T) {
	for _, site := range []struct {
		name string
		// source builds the site's expression from n terms.
		source func(n int) string
		// cost is the site's own compiler with the gate out of the way.
		cost func(src string) (uint64, error)
		// rule places the expression in an otherwise valid rule.
		rule func(src string) Rule
	}{
		{
			name:   "action guard",
			source: func(n int) string { return "size(" + repeatSeries(n) + ") > 0" },
			cost:   func(src string) (uint64, error) { return compileGuard(src, math.MaxUint64) },
			rule: func(src string) Rule {
				return thresholdWith(SeverityCritical, Action{Type: ActionRaiseAlarm, Guard: src,
					RaiseAlarm: &RaiseAlarmAction{AlarmKey: "over-temp"}})
			},
		},
		{
			name:   "payload template",
			source: repeatSeries,
			cost:   func(src string) (uint64, error) { return compileTemplate(src, math.MaxUint64) },
			rule: func(src string) Rule {
				return thresholdWith(SeverityCritical, Action{Type: ActionHTTPCall,
					HTTPCall: &HTTPCallAction{URL: "https://hooks.example/x", BodyTemplate: src}})
			},
		},
		{
			name:   "alarm-key template",
			source: repeatSeries,
			cost:   func(src string) (uint64, error) { return compileAlarmKeyTemplate(src, math.MaxUint64) },
			rule:   func(src string) Rule { return alarmTemplateRule("", src) },
		},
	} {
		t.Run(site.name, func(t *testing.T) {
			n := 1
			var cost uint64
			for ; n <= 64; n++ {
				c, err := site.cost(site.source(n))
				if err != nil {
					t.Fatalf("%d terms: the site's own compiler refused the expression: %v", n, err)
				}
				if c > 100 {
					cost = c
					break
				}
			}
			if cost == 0 {
				t.Fatal("no expression of up to 64 terms estimated above the ceiling; the step is wrong")
			}
			if cost > runtimeCostBackstop {
				t.Fatalf("the first expression over the ceiling estimates %d, above the runtime backstop %d; "+
					"this test could not tell the two apart", cost, runtimeCostBackstop)
			}
			if n == 1 {
				t.Fatal("a single term is already over the ceiling, so there is no shorter expression to accept")
			}

			over := site.source(n)
			_, err := Compile(site.rule(over), Limits{})
			if err == nil {
				t.Fatalf("%q (estimated cost %d) was accepted; the platform ceiling is 100", over, cost)
			}
			if !strings.Contains(err.Error(), "exceeds the ceiling 100") {
				t.Fatalf("%q was refused, but not at the platform ceiling of 100: %v", over, err)
			}

			under := site.source(n - 1)
			if _, err := Compile(site.rule(under), Limits{}); err != nil {
				t.Fatalf("%q is within the ceiling and was refused: %v", under, err)
			}
		})
	}
}

// repeatSeries is `series + series + ...` with n terms.
func repeatSeries(n int) string {
	return strings.TrimSuffix(strings.Repeat("series + ", n), " + ")
}
