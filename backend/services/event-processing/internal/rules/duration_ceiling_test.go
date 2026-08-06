// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"errors"
	"testing"
	"time"
)

// ceilingCases covers every authored duration field on a rule kind that actually uses it, so
// a kind that gains a new temporal field cannot quietly escape the ceiling. Each rule is
// otherwise VALID: the only thing under test is its duration, which means a rejection here is
// attributable to the ceiling and nothing else.
func ceilingCases(d time.Duration) map[string]struct {
	rule  Rule
	field string
} {
	return map[string]struct {
		rule  Rule
		field string
	}{
		"repeating window": {Rule{ID: "rp", Name: "flap", Type: TypeRepeating, Count: 3, Window: Duration(d),
			When: Condition{Metric: "open", Op: OpGe, Threshold: ptr(1)}}, "window"},
		"sliding aggregate window": {Rule{ID: "sl", Name: "slide", Type: TypeAggregate, Mode: ModeSliding,
			Metric: "t", Agg: AggMax, Op: OpGt, Threshold: ptr(50), Window: Duration(d)}, "window"},
		"tumbling aggregate window": {Rule{ID: "ag", Name: "avg", Type: TypeAggregate, Mode: ModeTumbling,
			Metric: "t", Agg: AggAvg, Op: OpGt, Threshold: ptr(50), Window: Duration(d)}, "window"},
		"correlation window": {Rule{ID: "co", Name: "area", Type: TypeCorrelation,
			AnchorType: "site", Count: 3, Window: Duration(d), MemberCap: 10}, "window"},
		"duration hold": {Rule{ID: "du", Name: "stuck", Type: TypeDuration, Hold: Duration(d),
			When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(30)}}, "hold"},
		"absence timeout": {Rule{ID: "ab", Name: "dead", Type: TypeAbsence, Ttl: Duration(d)}, "timeout"},
		"session gap": {Rule{ID: "se", Name: "sess", Type: TypeAggregate, Mode: ModeSession,
			Gap: Duration(d), Agg: AggCount, Op: OpGe, Threshold: ptr(3)}, "gap"},
	}
}

// TestRuleDurationCeilingRejectsOverlongRule is the FIRST negative control: the gate must say
// "no". Before this change every one of these rules compiled — nothing anywhere bounded a
// window, hold, timeout or gap from above — so a rule retaining a month of samples per series
// was publishable.
func TestRuleDurationCeilingRejectsOverlongRule(t *testing.T) {
	limits := Limits{PredicateCostCeiling: 1_000_000, MaxRuleDuration: time.Hour}
	for name, tc := range ceilingCases(time.Hour + time.Second) {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(tc.rule, limits)
			if err == nil {
				t.Fatal("want the duration ceiling to reject this rule, got a successful compile")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want a ValidationError the console can anchor, got %T: %v", err, err)
			}
			// The error must point at the field the author typed, not at some lowered
			// internal name — otherwise the console highlights the wrong input.
			if ve.Field != tc.field {
				t.Fatalf("want the error anchored to %q, got %q (%v)", tc.field, ve.Field, err)
			}
		})
	}
}

// TestRuleDurationCeilingAcceptsRuleAtTheCeiling is the counterweight: the ceiling must be
// inclusive and must not reject well-formed rules. A gate that refuses everything would pass
// the test above while being useless.
func TestRuleDurationCeilingAcceptsRuleAtTheCeiling(t *testing.T) {
	limits := Limits{PredicateCostCeiling: 1_000_000, MaxRuleDuration: time.Hour}
	for name, tc := range ceilingCases(time.Hour) {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(tc.rule, limits); err != nil {
				t.Fatalf("a rule exactly at the ceiling must compile, got: %v", err)
			}
		})
	}
}

// TestUnsetMaxRuleDurationFloorsToADayNeverUnlimited pins the ADR-023 fail-safe cascade: a
// zero (or negative) budget field means UNSET, and unset resolves to the platform default —
// never to "no limit". A caller that forgets to resolve limits gets a capped compile, which is
// the whole point of the floor living inside Compile rather than at the call site.
func TestUnsetMaxRuleDurationFloorsToADayNeverUnlimited(t *testing.T) {
	for name, zero := range map[string]time.Duration{"unset": 0, "negative": -time.Hour} {
		t.Run(name, func(t *testing.T) {
			limits := Limits{PredicateCostCeiling: 1_000_000, MaxRuleDuration: zero}
			if got := limits.WithDefaults().MaxRuleDuration; got != defaultMaxRuleDuration {
				t.Fatalf("want the built-in floor %s, got %s", defaultMaxRuleDuration, got)
			}
			// And prove the floor is actually ENFORCED, not merely reported by WithDefaults:
			// a rule one second past the day must be refused by a Compile given zero limits.
			over := Rule{ID: "rp", Name: "flap", Type: TypeRepeating, Count: 3,
				Window: Duration(defaultMaxRuleDuration + time.Second),
				When:   Condition{Metric: "open", Op: OpGe, Threshold: ptr(1)}}
			if _, err := Compile(over, limits); err == nil {
				t.Fatal("an unset ceiling must floor to a day and REJECT a longer rule, not run uncapped")
			}
		})
	}
}

// TestDefaultLimitsCarriesTheConfiguredCeiling proves the operator knob reaches the ONE budget
// both compile sites read. rules.DefaultLimits is what the ADR-044 publish gate
// (graphql.ValidateDetectionRules) and the runtime fact consumer (runtime.CompilePublishedRules)
// each call, so a ceiling that did not land here would be enforced by neither.
func TestDefaultLimitsCarriesTheConfiguredCeiling(t *testing.T) {
	t.Cleanup(func() { SetPlatformMaxRuleDuration(0) })

	// Unconfigured: DefaultLimits reports zero, which Compile floors to a day.
	SetPlatformMaxRuleDuration(0)
	if got := DefaultLimits().WithDefaults().MaxRuleDuration; got != defaultMaxRuleDuration {
		t.Fatalf("unconfigured DefaultLimits must resolve to the day floor, got %s", got)
	}

	SetPlatformMaxRuleDuration(72 * time.Hour)
	if got := DefaultLimits().MaxRuleDuration; got != 72*time.Hour {
		t.Fatalf("want the configured 72h ceiling, got %s", got)
	}
	// An operator RAISING the ceiling must actually admit the longer rule — otherwise the knob
	// is decorative and a tenant with a legitimate long window has no escape hatch.
	long := Rule{ID: "sl", Name: "slide", Type: TypeAggregate, Mode: ModeSliding,
		Metric: "t", Agg: AggMax, Op: OpGt, Threshold: ptr(50), Window: Duration(48 * time.Hour)}
	if _, err := Compile(long, DefaultLimits()); err != nil {
		t.Fatalf("a 48h rule must compile under a 72h operator ceiling, got: %v", err)
	}
	// ...and still refuse one past the raised ceiling.
	tooLong := long
	tooLong.Window = Duration(96 * time.Hour)
	if _, err := Compile(tooLong, DefaultLimits()); err == nil {
		t.Fatal("a 96h rule must still be refused under a 72h ceiling")
	}
}
