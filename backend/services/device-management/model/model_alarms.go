// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// AlarmConditionType is the shape of an alarm rule's trigger (ADR-041). SIMPLE
// fires on a single measurement crossing the threshold; DURATION requires the
// condition to hold for a minimum time; REPEATING requires a count of occurrences
// within a window. Evaluation of DURATION/REPEATING is staged after SIMPLE, but
// the model carries all three so a profile can declare them now.
type AlarmConditionType string

const (
	AlarmConditionSimple    AlarmConditionType = "SIMPLE"
	AlarmConditionDuration  AlarmConditionType = "DURATION"
	AlarmConditionRepeating AlarmConditionType = "REPEATING"
)

// Valid reports whether the type names one of the known condition types.
func (t AlarmConditionType) Valid() bool {
	switch t {
	case AlarmConditionSimple, AlarmConditionDuration, AlarmConditionRepeating:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t AlarmConditionType) String() string { return string(t) }

// AlarmOperator is the comparison an alarm condition applies between a measurement
// value and the (static or dynamic) threshold.
type AlarmOperator string

const (
	AlarmOpGreater      AlarmOperator = "GT"
	AlarmOpGreaterEqual AlarmOperator = "GTE"
	AlarmOpLess         AlarmOperator = "LT"
	AlarmOpLessEqual    AlarmOperator = "LTE"
	AlarmOpEqual        AlarmOperator = "EQ"
	AlarmOpNotEqual     AlarmOperator = "NEQ"
)

// Valid reports whether the operator names one of the known comparisons.
func (o AlarmOperator) Valid() bool {
	switch o {
	case AlarmOpGreater, AlarmOpGreaterEqual, AlarmOpLess, AlarmOpLessEqual, AlarmOpEqual, AlarmOpNotEqual:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (o AlarmOperator) String() string { return string(o) }

// AlarmSeverity is the severity of an alarm (ADR-041). Ordered so the evaluator can
// escalate an active alarm in place when a higher-severity rule fires, rather than
// spawning a second alarm.
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

// AlarmDefinition is an alarm rule declared on a DeviceProfile (ADR-041/ADR-045),
// structurally parallel to MetricDefinition and CommandDefinition.
// It names a metric to watch and a condition that, when met, raises an alarm of the
// given severity keyed by (originator, AlarmKey). Hanging the rule off the profile
// keeps alarm config travelling with the fleet definition, not as free-floating
// objects. The condition is flat-relational (not a nested document) — Simple is a
// threshold comparison; Duration/Repeating add a hold time / occurrence window.
//
// One AlarmKey may declare several rows, one per Severity tier (e.g. temp>80 → MAJOR
// and temp>100 → CRITICAL both key "over-temp"): the row is unique by
// (device_profile_id, AlarmKey, Severity), and the evaluator escalates a single active
// alarm in place to the highest satisfied tier (ADR-041 dec 3). All tiers of one
// AlarmKey watch the same MetricKey (enforced at declaration) so they describe one
// escalating condition rather than unrelated alarms sharing a name.
//
// The threshold is either static (Threshold) or dynamic (ThresholdAttr names an
// entity attribute resolved at evaluation, ADR-041); exactly one is set. MinValue/
// MaxValue on the referenced MetricDefinition are ingest-validation bounds, not
// alarm thresholds — the two are deliberately independent.
type AlarmDefinition struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceProfileId uint
	DeviceProfile   *DeviceProfile

	AlarmKey      string // stable alarm identifier, unique per profile (token grammar)
	MetricKey     string // the measurement this rule evaluates (a MetricDefinition key)
	ConditionType string // one of AlarmConditionType
	Operator      string // one of AlarmOperator
	Severity      string // one of AlarmSeverity

	Threshold     sql.NullFloat64 // static threshold; unset when ThresholdAttr is used
	ThresholdAttr sql.NullString  // dynamic threshold: an entity-attribute key (ADR-041)

	DurationSeconds     sql.NullInt64 // DURATION: the condition must hold at least this long
	RepeatCount         sql.NullInt64 // REPEATING: number of occurrences required
	RepeatWindowSeconds sql.NullInt64 // REPEATING: window the occurrences are counted over

	Enabled bool // a disabled rule is retained but not evaluated
}
