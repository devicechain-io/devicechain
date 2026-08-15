// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"slices"
	"strings"
)

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
// — it cannot be half-added. Anything needing to range over the severities reads this
// rather than re-listing them and drifting. (Unexported; AlarmSeverities is the
// copy-returning accessor for callers outside the package.)
var alarmSeveritiesByRank = []AlarmSeverity{
	AlarmSeverityCritical,
	AlarmSeverityMajor,
	AlarmSeverityMinor,
	AlarmSeverityWarning,
	AlarmSeverityIndeterminate,
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

// AlarmSeverities returns the tier vocabulary, most severe first.
//
// It exists so that code which must reproduce the vocabulary can be CHECKED against this
// declaration rather than trusted. notification-management validates a policy rule's
// severity against these tiers, and deliberately keeps its own copy of them — importing
// this package at run time would hand a DR tool downstream of it a CEL engine and an MQTT
// client. Its copy is safe only because a test there asserts equality with what this
// returns, so a tier added, retired or reordered here fails that test on the same PR.
//
// Which means: this accessor's callers are allowed to be tests. That is the point of it.
//
// It returns a COPY. alarmSeveritiesByRank is the single declaration Rank and Valid are
// computed from, so a caller that sorted or truncated the slice it was handed would silently
// redefine both — and the corruption would show up as a wrong escalation order in an
// unrelated service. One small allocation on an error path is the right price for that.
func AlarmSeverities() []AlarmSeverity {
	return slices.Clone(alarmSeveritiesByRank)
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
