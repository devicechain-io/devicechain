// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AlarmState is the lifecycle state of a raised alarm (ADR-041). Combined with the
// Acknowledged flag it yields the four-state model {ACTIVE,CLEARED}×{ACK,UNACK}:
// ACTIVE means the condition is currently met, CLEARED means it has resolved. A
// single alarm row transitions between these in place — a re-raise flips a CLEARED
// row back to ACTIVE rather than spawning a new row (dedup by construction).
type AlarmState string

const (
	AlarmStateActive  AlarmState = "ACTIVE"
	AlarmStateCleared AlarmState = "CLEARED"
)

// Valid reports whether the state names one of the known alarm states.
func (s AlarmState) Valid() bool {
	switch s {
	case AlarmStateActive, AlarmStateCleared:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s AlarmState) String() string { return string(s) }

// Alarm is a first-class, raised alarm (ADR-041) — the operational counterpart to
// AlarmDefinition (the rule). The evaluator raises one when a device's measurement
// meets a rule's condition, keyed by (originator, AlarmKey): there is at most one
// live Alarm per that key, so re-crossings dedup into the same row and severity
// escalates in place (ADR-041 dec 3/4). Alarm history is the re-emitted state-change
// event stream (a later slice), NOT one row per occurrence.
//
// The originator is addressed uniformly by (OriginatorType, OriginatorId) — the same
// ADR-013 entity addressing used by EntityAttribute and EntityRelationship — so an
// alarm can in principle be raised on any entity (today the evaluator raises on the
// device); referential integrity for that reference is app-layer (the delete cascade
// removes an entity's alarms).
type Alarm struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	OriginatorType string // entity.Type of the originator (today: "device")
	OriginatorId   uint   // resolved id of the originator within the tenant

	AlarmKey  string // the AlarmDefinition key that raised this alarm
	MetricKey string // the watched measurement, denormalized for display

	State        string // one of AlarmState (ACTIVE | CLEARED)
	Acknowledged bool   // an operator has acknowledged the alarm
	Severity     string // one of AlarmSeverity; escalates in place to the highest satisfied tier

	RaisedTime       time.Time       // when the alarm first went (or most recently returned to) ACTIVE
	ClearedTime      sql.NullTime    // when the condition last resolved; set while CLEARED
	AcknowledgedTime sql.NullTime    // when an operator acknowledged
	AcknowledgedBy   sql.NullString  // identity that acknowledged (opaque operator reference)
	LastValue        sql.NullFloat64 // the measurement value at the most recent evaluation
	Message          sql.NullString  // human-readable summary (rule-provided or generated)

	// Contributors is the ADR-057 contributor set: the JSON-encoded {ruleID → AlarmContributor} map
	// the DETECT+REACT integrator reference-counts to derive State + Severity (raise adds/updates a
	// contributor, resolve tombstones it, the alarm clears when no contributor is active). NULL/empty
	// for a legacy measurement-evaluator-raised alarm (the evaluator is retired at slice 6); the
	// integrator populates it. See alarm_contributor.go for the reduction.
	Contributors datatypes.JSON `gorm:"type:jsonb"`
}

// Search criteria for locating alarms. All facets are optional and AND together;
// Originator restricts to a single entity by its (type, token).
type AlarmSearchCriteria struct {
	rdb.Pagination
	OriginatorType *string // entity type of the originator (e.g. "device")
	Originator     *string // originator entity token (resolved to OriginatorId)
	State          *string // ACTIVE | CLEARED
	Severity       *string
	Acknowledged   *bool
	AlarmKey       *string
}

// Results for an alarm search.
type AlarmSearchResults struct {
	Results    []Alarm
	Pagination rdb.SearchResultsPagination
}
