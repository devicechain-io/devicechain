// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// AlarmSeverity is the severity of an alarm (ADR-041). Ordered so the integrator can
// escalate an active alarm in place when a higher-severity rule fires, rather than
// spawning a second alarm.
//
// The AlarmDefinition rule model (and its AlarmConditionType/AlarmOperator vocabulary) was
// retired with the alarm authoring path (ADR-057) and the 6d cutover; DETECT DetectionRule
// rules now drive alarms. AlarmSeverity survives because it is the raised Alarm object's tier
// and the contributor-set integrator's rank vocabulary.
type AlarmSeverity string

const (
	AlarmSeverityCritical      AlarmSeverity = "CRITICAL"
	AlarmSeverityMajor         AlarmSeverity = "MAJOR"
	AlarmSeverityMinor         AlarmSeverity = "MINOR"
	AlarmSeverityWarning       AlarmSeverity = "WARNING"
	AlarmSeverityIndeterminate AlarmSeverity = "INDETERMINATE"
)

// Valid reports whether the severity names one of the known levels.
func (s AlarmSeverity) Valid() bool {
	return s.Rank() >= 0
}

// Rank orders severities from most (CRITICAL) to least (INDETERMINATE) severe,
// with 0 = most severe. An unknown severity returns -1. Used for upgrade-in-place:
// a lower rank escalates an active alarm (ADR-041 decision 3).
func (s AlarmSeverity) Rank() int {
	switch s {
	case AlarmSeverityCritical:
		return 0
	case AlarmSeverityMajor:
		return 1
	case AlarmSeverityMinor:
		return 2
	case AlarmSeverityWarning:
		return 3
	case AlarmSeverityIndeterminate:
		return 4
	default:
		return -1
	}
}

// String returns the underlying string value.
func (s AlarmSeverity) String() string { return string(s) }
