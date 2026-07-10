// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"
	"testing"
)

// satisfiesOperator implements each comparison and fails closed on an unknown op.
func TestSatisfiesOperator(t *testing.T) {
	cases := []struct {
		op            AlarmOperator
		value, thresh float64
		want          bool
	}{
		{AlarmOpGreater, 101, 100, true},
		{AlarmOpGreater, 100, 100, false},
		{AlarmOpGreaterEqual, 100, 100, true},
		{AlarmOpGreaterEqual, 99, 100, false},
		{AlarmOpLess, 99, 100, true},
		{AlarmOpLess, 100, 100, false},
		{AlarmOpLessEqual, 100, 100, true},
		{AlarmOpLessEqual, 101, 100, false},
		{AlarmOpEqual, 42, 42, true},
		{AlarmOpEqual, 42, 43, false},
		{AlarmOpNotEqual, 42, 43, true},
		{AlarmOpNotEqual, 42, 42, false},
		{AlarmOperator("BOGUS"), 1, 0, false},
	}
	for _, c := range cases {
		if got := satisfiesOperator(c.op, c.value, c.thresh); got != c.want {
			t.Errorf("satisfiesOperator(%s, %v, %v) = %v, want %v", c.op, c.value, c.thresh, got, c.want)
		}
	}
}

// staticTier builds a SIMPLE tier with a static threshold for evaluator tests.
func staticTier(severity string, op AlarmOperator, threshold float64) *AlarmDefinition {
	return &AlarmDefinition{
		ConditionType: string(AlarmConditionSimple),
		Operator:      string(op),
		Severity:      severity,
		Threshold:     sql.NullFloat64{Float64: threshold, Valid: true},
		Enabled:       true,
	}
}

// staticThreshold is the threshold resolver used in the pure-logic tests.
func staticThreshold(t *AlarmDefinition) (float64, bool, error) {
	if t.Threshold.Valid {
		return t.Threshold.Float64, true, nil
	}
	return 0, false, nil
}

// highestSatisfiedSeverity returns the most-severe satisfied tier — the crux of
// escalate-in-place — and reports "none satisfied" when the value is below every
// tier (which drives the auto-clear path).
func TestHighestSatisfiedSeverity(t *testing.T) {
	// Two tiers on one key: temp>80 → MAJOR, temp>100 → CRITICAL.
	tiers := []*AlarmDefinition{
		staticTier(string(AlarmSeverityMajor), AlarmOpGreater, 80),
		staticTier(string(AlarmSeverityCritical), AlarmOpGreater, 100),
	}

	if sev, ok, err := highestSatisfiedSeverity(tiers, 120, "", staticThreshold); err != nil || !ok || sev != string(AlarmSeverityCritical) {
		t.Errorf("value 120: got (%q, %v, %v), want (CRITICAL, true, nil)", sev, ok, err)
	}
	if sev, ok, err := highestSatisfiedSeverity(tiers, 90, "", staticThreshold); err != nil || !ok || sev != string(AlarmSeverityMajor) {
		t.Errorf("value 90: got (%q, %v, %v), want (MAJOR, true, nil)", sev, ok, err)
	}
	if sev, ok, err := highestSatisfiedSeverity(tiers, 50, "", staticThreshold); err != nil || ok {
		t.Errorf("value 50: got (%q, %v, %v), want (\"\", false, nil)", sev, ok, err)
	}
}

// A tier whose threshold can't be resolved, or whose severity is unknown, or that
// watches a different metric than the key, is skipped rather than treated as
// satisfied.
func TestHighestSatisfiedSeveritySkips(t *testing.T) {
	// Dynamic-threshold tier with no resolver value → skipped even though the op
	// would trivially match.
	dyn := &AlarmDefinition{
		ConditionType: string(AlarmConditionSimple),
		Operator:      string(AlarmOpGreater),
		Severity:      string(AlarmSeverityMajor),
		ThresholdAttr: sql.NullString{String: "limit", Valid: true},
		Enabled:       true,
	}
	if sev, ok, err := highestSatisfiedSeverity([]*AlarmDefinition{dyn}, 1000, "", staticThreshold); err != nil || ok {
		t.Errorf("unresolvable threshold: got (%q, %v, %v), want (\"\", false, nil)", sev, ok, err)
	}

	// Unknown severity → skipped.
	bad := staticTier("SEVERE", AlarmOpGreater, 0)
	if sev, ok, err := highestSatisfiedSeverity([]*AlarmDefinition{bad}, 5, "", staticThreshold); err != nil || ok {
		t.Errorf("unknown severity: got (%q, %v, %v), want (\"\", false, nil)", sev, ok, err)
	}

	// A tier watching a different metric than the key must not fire on this value.
	rogue := staticTier(string(AlarmSeverityCritical), AlarmOpGreater, 0)
	rogue.MetricKey = "humidity"
	if sev, ok, err := highestSatisfiedSeverity([]*AlarmDefinition{rogue}, 100, "temp", staticThreshold); err != nil || ok {
		t.Errorf("mismatched metric: got (%q, %v, %v), want (\"\", false, nil)", sev, ok, err)
	}
}

// resolveThreshold's eligibility is TYPE-DRIVEN and identical to the DETECT fact producer:
// only a DOUBLE/LONG attribute is a numeric threshold; a STRING that merely parses as a number
// is ineligible (the regression this closes), and a non-eligible SERVER value falls through to a
// SHARED value exactly as DETECT's flatten does.
func TestResolveThresholdTypeEligibility(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)
	devs, err := api.DevicesByToken(ctx, []string{"d1"})
	if err != nil || len(devs) != 1 {
		t.Fatalf("resolve device: %v", err)
	}
	devId := devs[0].ID
	ruleFor := func(key string) *AlarmDefinition {
		return &AlarmDefinition{ThresholdAttr: sql.NullString{String: key, Valid: true}}
	}

	// Static threshold short-circuits, no attribute read.
	if v, ok, err := api.resolveThreshold(ctx, devId, &AlarmDefinition{Threshold: sql.NullFloat64{Float64: 7, Valid: true}}); err != nil || !ok || v != 7 {
		t.Fatalf("static threshold: got (%v,%v,%v), want (7,true,nil)", v, ok, err)
	}

	// DOUBLE and LONG are eligible.
	setAttr(t, api, ctx, string(AttributeScopeServer), "d", string(AttributeValueDouble), "50")
	if v, ok, err := api.resolveThreshold(ctx, devId, ruleFor("d")); err != nil || !ok || v != 50 {
		t.Fatalf("DOUBLE threshold: got (%v,%v,%v), want (50,true,nil)", v, ok, err)
	}
	setAttr(t, api, ctx, string(AttributeScopeServer), "l", string(AttributeValueLong), "3000")
	if v, ok, err := api.resolveThreshold(ctx, devId, ruleFor("l")); err != nil || !ok || v != 3000 {
		t.Fatalf("LONG threshold: got (%v,%v,%v), want (3000,true,nil)", v, ok, err)
	}

	// STRING "50" parses as a number but is NOT type-eligible — the regression this fix closes.
	setAttr(t, api, ctx, string(AttributeScopeServer), "s", string(AttributeValueString), "50")
	if v, ok, err := api.resolveThreshold(ctx, devId, ruleFor("s")); err != nil || ok {
		t.Fatalf("STRING threshold must be ineligible: got (%v,%v,%v), want (_,false,nil)", v, ok, err)
	}

	// A non-numeric SERVER value falls through to a numeric SHARED value (matches DETECT flatten).
	setAttr(t, api, ctx, string(AttributeScopeServer), "f", string(AttributeValueString), "10")
	setAttr(t, api, ctx, string(AttributeScopeShared), "f", string(AttributeValueDouble), "80")
	if v, ok, err := api.resolveThreshold(ctx, devId, ruleFor("f")); err != nil || !ok || v != 80 {
		t.Fatalf("SERVER-string falls through to SHARED-double: got (%v,%v,%v), want (80,true,nil)", v, ok, err)
	}

	// A numeric SERVER value shadows a numeric SHARED value.
	setAttr(t, api, ctx, string(AttributeScopeServer), "p", string(AttributeValueDouble), "50")
	setAttr(t, api, ctx, string(AttributeScopeShared), "p", string(AttributeValueDouble), "80")
	if v, ok, err := api.resolveThreshold(ctx, devId, ruleFor("p")); err != nil || !ok || v != 50 {
		t.Fatalf("SERVER-double shadows SHARED-double: got (%v,%v,%v), want (50,true,nil)", v, ok, err)
	}
}

// A threshold-resolution error is propagated (not swallowed as "no threshold"), so
// the caller retries instead of spuriously clearing a live alarm.
func TestHighestSatisfiedSeverityPropagatesError(t *testing.T) {
	boom := errors.New("db blip")
	failing := func(*AlarmDefinition) (float64, bool, error) { return 0, false, boom }
	tier := staticTier(string(AlarmSeverityMajor), AlarmOpGreater, 0)
	if _, _, err := highestSatisfiedSeverity([]*AlarmDefinition{tier}, 5, "", failing); !errors.Is(err, boom) {
		t.Errorf("expected propagated error, got %v", err)
	}
}
