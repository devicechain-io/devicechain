// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "strings"

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

// alarmSeveritiesByRank is the tier order, most severe first. It is the SINGLE
// declaration of the vocabulary: Rank is its index and Valid is membership in it, so a
// tier added to the const block above but not to this slice has no rank and is not valid
// — it cannot be half-added. Anything ranging over the severities (a test, a UI list)
// gets it from AllAlarmSeverities rather than re-listing them and drifting.
var alarmSeveritiesByRank = []AlarmSeverity{
	AlarmSeverityCritical,
	AlarmSeverityMajor,
	AlarmSeverityMinor,
	AlarmSeverityWarning,
	AlarmSeverityIndeterminate,
}

// AllAlarmSeverities returns the known severities, most severe first. The returned slice
// is a copy, so a caller cannot reorder the ranking by mutating it.
func AllAlarmSeverities() []AlarmSeverity {
	return append([]AlarmSeverity(nil), alarmSeveritiesByRank...)
}

// Valid reports whether the severity names one of the known levels.
func (s AlarmSeverity) Valid() bool {
	return s.Rank() >= 0
}

// Rank orders severities from most (CRITICAL) to least (INDETERMINATE) severe,
// with 0 = most severe. An unknown severity returns -1. Used for upgrade-in-place:
// a lower rank escalates an active alarm (ADR-041 decision 3).
func (s AlarmSeverity) Rank() int {
	for i, known := range alarmSeveritiesByRank {
		if s == known {
			return i
		}
	}
	return -1
}

// String returns the underlying string value.
func (s AlarmSeverity) String() string { return string(s) }

// AlarmSeverityFromRuleSeverity maps a DETECT rule's AUTHORING severity to the alarm tier
// it lands at on the durable row.
//
// The two vocabularies are the same ADR-041 tiers in different case, and that is a real
// contract rather than an accident: event-processing's rules.Severity is lowercase (matching
// its schema's JSON-value style) and rejects "MAJOR" outright at compile, while AlarmSeverity
// is uppercase and rejects "major". Every raise crosses that boundary, so the conversion is a
// named, single-homed step here instead of an anonymous strings.ToUpper at the one call site —
// it is mirrored by the sim scenarios (which speak only the wire and cannot import either
// module) and pinned from both sides by their fixture tests.
//
// The result is NOT assumed valid: an unknown authoring severity uppercases to an unknown
// tier, which Valid() rejects. Callers must check.
func AlarmSeverityFromRuleSeverity(authoring string) AlarmSeverity {
	return AlarmSeverity(strings.ToUpper(authoring))
}
