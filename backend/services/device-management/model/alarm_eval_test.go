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
