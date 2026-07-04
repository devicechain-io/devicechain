// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file holds the pure decision logic of the SIMPLE alarm evaluator (ADR-041):
// given a measurement value and a set of rule tiers, decide whether an alarm should
// be active and at what severity. It has no I/O so it is unit-testable in isolation;
// the DB glue (loading rules, resolving dynamic thresholds, upserting Alarm rows)
// lives in api_alarm_eval.go.

// satisfiesOperator reports whether value, compared to threshold under op, meets the
// condition. An unknown operator is treated as unmet (fail-closed). EQ/NEQ compare
// float64s exactly, which is inherent to comparing a measured value against a
// declared threshold — a rule author choosing EQ on a continuous metric owns that.
func satisfiesOperator(op AlarmOperator, value, threshold float64) bool {
	switch op {
	case AlarmOpGreater:
		return value > threshold
	case AlarmOpGreaterEqual:
		return value >= threshold
	case AlarmOpLess:
		return value < threshold
	case AlarmOpLessEqual:
		return value <= threshold
	case AlarmOpEqual:
		return value == threshold
	case AlarmOpNotEqual:
		return value != threshold
	default:
		return false
	}
}

// highestSatisfiedSeverity evaluates every tier of one alarm key against value and
// returns the severity of the most-severe (lowest Rank) tier whose condition is met,
// plus whether any tier was met. thresholdOf resolves a tier's threshold (static or
// dynamic); a tier whose threshold is genuinely absent/non-numeric, or whose severity
// is unknown, is skipped, but a threshold-resolution *error* (e.g. a DB failure) is
// propagated so the caller can retry rather than mistake it for "no threshold" and
// spuriously clear a live alarm. Only tiers watching metricKey are considered — a
// mis-declared tier bound to a different metric (the #202 same-key coherence check is
// best-effort) must not fire on this metric's value. Tiers are the rows sharing a
// (device_type, alarm_key) — e.g. temp>80→MAJOR and temp>100→CRITICAL — so this is
// where escalate-in-place is decided: at value 120 both fire and CRITICAL wins; at 90
// only MAJOR; at 50 none (→ the caller auto-clears).
func highestSatisfiedSeverity(tiers []*AlarmDefinition, value float64, metricKey string,
	thresholdOf func(*AlarmDefinition) (float64, bool, error)) (string, bool, error) {
	bestRank := -1
	best := ""
	for _, t := range tiers {
		if t.MetricKey != metricKey {
			continue
		}
		threshold, ok, err := thresholdOf(t)
		if err != nil {
			return "", false, err
		}
		if !ok {
			continue
		}
		if !satisfiesOperator(AlarmOperator(t.Operator), value, threshold) {
			continue
		}
		rank := AlarmSeverity(t.Severity).Rank()
		if rank < 0 {
			continue
		}
		if bestRank == -1 || rank < bestRank {
			bestRank = rank
			best = t.Severity
		}
	}
	return best, bestRank != -1, nil
}
