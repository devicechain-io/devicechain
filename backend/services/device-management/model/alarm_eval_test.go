// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
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
func staticThreshold(t *AlarmDefinition) (float64, bool) {
	if t.Threshold.Valid {
		return t.Threshold.Float64, true
	}
	return 0, false
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

	if sev, ok := highestSatisfiedSeverity(tiers, 120, staticThreshold); !ok || sev != string(AlarmSeverityCritical) {
		t.Errorf("value 120: got (%q, %v), want (CRITICAL, true)", sev, ok)
	}
	if sev, ok := highestSatisfiedSeverity(tiers, 90, staticThreshold); !ok || sev != string(AlarmSeverityMajor) {
		t.Errorf("value 90: got (%q, %v), want (MAJOR, true)", sev, ok)
	}
	if sev, ok := highestSatisfiedSeverity(tiers, 50, staticThreshold); ok {
		t.Errorf("value 50: got (%q, %v), want (\"\", false)", sev, ok)
	}
}

// A tier whose threshold can't be resolved, or whose severity is unknown, is skipped
// rather than treated as satisfied.
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
	if sev, ok := highestSatisfiedSeverity([]*AlarmDefinition{dyn}, 1000, staticThreshold); ok {
		t.Errorf("unresolvable threshold: got (%q, %v), want (\"\", false)", sev, ok)
	}

	// Unknown severity → skipped.
	bad := staticTier("SEVERE", AlarmOpGreater, 0)
	if sev, ok := highestSatisfiedSeverity([]*AlarmDefinition{bad}, 5, staticThreshold); ok {
		t.Errorf("unknown severity: got (%q, %v), want (\"\", false)", sev, ok)
	}
}
