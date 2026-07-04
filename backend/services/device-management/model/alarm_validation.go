// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// ValidateAlarmDefinition checks that an alarm rule is well-formed at declaration
// (ADR-041): known condition type / operator / severity, a metric to watch, exactly
// one threshold source (static value or dynamic attribute), and the fields the
// condition type requires — and only those. It does not require the referenced
// MetricKey to already exist on the profile (rules and metrics may be declared in
// any order; a rule for an absent metric simply never fires).
func ValidateAlarmDefinition(r *AlarmDefinitionCreateRequest) error {
	if !AlarmConditionType(r.ConditionType).Valid() {
		return fmt.Errorf("invalid alarm condition type: %q", r.ConditionType)
	}
	if !AlarmOperator(r.Operator).Valid() {
		return fmt.Errorf("invalid alarm operator: %q", r.Operator)
	}
	if !AlarmSeverity(r.Severity).Valid() {
		return fmt.Errorf("invalid alarm severity: %q", r.Severity)
	}
	if r.MetricKey == "" {
		return fmt.Errorf("alarm definition must name a metric key")
	}

	// A threshold comes from exactly one source: a static value or a dynamic
	// entity-attribute key (ADR-041). Neither or both is a misconfiguration.
	hasStatic := r.Threshold != nil
	hasDynamic := r.ThresholdAttr != nil && *r.ThresholdAttr != ""
	if hasStatic == hasDynamic {
		return fmt.Errorf("alarm definition must set exactly one of threshold or thresholdAttr")
	}

	switch AlarmConditionType(r.ConditionType) {
	case AlarmConditionSimple:
		if r.DurationSeconds != nil || r.RepeatCount != nil || r.RepeatWindowSeconds != nil {
			return fmt.Errorf("SIMPLE alarm must not set duration or repeat fields")
		}
	case AlarmConditionDuration:
		if r.DurationSeconds == nil || *r.DurationSeconds <= 0 {
			return fmt.Errorf("DURATION alarm requires a positive durationSeconds")
		}
		if r.RepeatCount != nil || r.RepeatWindowSeconds != nil {
			return fmt.Errorf("DURATION alarm must not set repeat fields")
		}
	case AlarmConditionRepeating:
		if r.RepeatCount == nil || *r.RepeatCount <= 0 {
			return fmt.Errorf("REPEATING alarm requires a positive repeatCount")
		}
		if r.RepeatWindowSeconds == nil || *r.RepeatWindowSeconds <= 0 {
			return fmt.Errorf("REPEATING alarm requires a positive repeatWindowSeconds")
		}
		if r.DurationSeconds != nil {
			return fmt.Errorf("REPEATING alarm must not set durationSeconds")
		}
	}
	return nil
}
