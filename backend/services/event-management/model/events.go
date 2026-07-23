// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Event with token references resolved. DeviceToken names the originating device
// by its stable per-tenant token (ADR-044): event-management never stores
// device-management's numeric row ids, which are meaningless across the seam and
// break under id reuse. The device's tracked-relationship targets are recorded as
// a *set* of anchors in the sibling EventAnchor table (ADR-013 addendum
// 2026-07-01) rather than a single denormalized pair, so an event assigned to
// several targets is queryable by each; an unassigned device's event simply has no
// anchor rows.
type Event struct {
	rdb.TenantScoped
	DeviceToken   string `gorm:"type:varchar(128)"`
	EventType     esmodel.EventType
	OccurredTime  time.Time
	Source        string
	AltId         sql.NullString
	ProcessedTime time.Time
}

// EventAnchor is one anchor of an event: a device's tracked-relationship target
// (ADR-013) denormalized so the event is queryable by that (anchor_type,
// anchor_token) dimension. Both the source device and the anchor target are named
// by their stable per-tenant tokens (ADR-044), not device-management row ids. It
// points back to the base event by its natural key (device_token, event_type,
// occurred_time); occurred_time is also the hypertable partition column. One event
// has zero or more anchor rows.
type EventAnchor struct {
	rdb.TenantScoped
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	AnchorType   string            `gorm:"not null"`
	AnchorToken  string            `gorm:"type:varchar(128);not null"`
}

// The three payload tables (location/measurement/alert) are hypertables in their
// own right, partitioned on occurred_time alongside the base events hypertable
// (ADR-026 amd). They relate to the base event by the natural key (device_id,
// event_type, occurred_time) — a plain app-level join, not a DB foreign key: an FK
// referencing a hypertable blocks drop_chunks on the parent, so the data-lifecycle
// (retention/compression) work would be un-droppable. Insert order is enforced by
// upsertParentEvents (parent first), not by a constraint (see model/api.go).

// Location event fields.
type LocationEvent struct {
	rdb.TenantScoped
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	// Latitude/Longitude are degrees, so 8 fractional digits (~1.1mm) with just
	// enough integer room for ±90 / ±180. Elevation is metres, not degrees: it
	// needs integer range (mountains, aircraft, orbit), not sub-degree precision —
	// decimal(12,4) holds ±99,999,999.9999 m at 0.1mm resolution. A decimal(10,8)
	// here would overflow above 99.99 m.
	Latitude  sql.NullFloat64 `gorm:"type:decimal(10,8);"`
	Longitude sql.NullFloat64 `gorm:"type:decimal(11,8);"`
	Elevation sql.NullFloat64 `gorm:"type:decimal(12,4);"`
}

// Information required to create a location event.
type LocationEventCreateRequest struct {
	Event
	Latitude  *float64
	Longitude *float64
	Elevation *float64
}

// Measurement event fields. Unit and DataType are denormalized from the bound
// metric definition at resolve time (ADR-016), so a stored measurement is
// self-describing on read — a consumer resolves its unit/type off the row rather
// than joining back into device-management (a cross-service hop, ADR-044). They are
// null for an undeclared (unbound) measurement, and are snapshot-consistent: a
// later profile republish does not rewrite the unit/type of already-stored rows.
type MeasurementEvent struct {
	rdb.TenantScoped
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Name         string            `gorm:"not null"`
	Value        sql.NullFloat64   `gorm:"type:decimal(20,8);"`
	Classifier   *uint
	// Unit is unbounded text to match the source MetricDefinition.Unit (an unbounded
	// column with no length validation) — a bound here could reject a long unit at
	// INSERT as a non-deterministic (poison-retried) failure. DataType is a closed
	// enum (max "BOOLEAN"), so a tight bound is safe.
	Unit     *string `gorm:"type:text"`
	DataType *string `gorm:"type:varchar(32)"`
}

// Information required to create a measurement event.
type MeasurementEventCreateRequest struct {
	Event
	Name       string
	Value      *float64
	Classifier *uint
	Unit       *string
	DataType   *string
}

// MeasurementRollup is one row of the measurement_rollups continuous aggregate
// (ADR-026): for a single (tenant, device, event type, measurement name), the
// partial aggregates of one fixed base bucket of measurement_events, so bucketed
// dashboard reads hit pre-computed rollups instead of scanning raw rows. avg is
// deliberately NOT stored — an average does not roll up (an average of averages is
// wrong); the read derives avg = sum/count when re-bucketing to a coarser interval,
// which is exact. min/max/sum/count all roll up directly. The table name pluralizes
// to measurement_rollups (matching the continuous-aggregate view) and tenant_id
// (via TenantScoped) makes reads fail-closed tenant-scoped exactly like the raw path.
type MeasurementRollup struct {
	rdb.TenantScoped
	DeviceToken string `gorm:"type:varchar(128)"`
	EventType   esmodel.EventType
	Name        string
	Bucket      time.Time
	SumValue    sql.NullFloat64
	MinValue    sql.NullFloat64
	MaxValue    sql.NullFloat64
	CountValue  int64
}

// Alert event fields.
type AlertEvent struct {
	rdb.TenantScoped
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Type         string            `gorm:"not null"`
	Level        uint32            `gorm:"not null"`
	Message      string
	Source       string
}

// Information required to create an alert event.
type AlertEventCreateRequest struct {
	Event
	Type    string
	Level   uint32
	Message string
	Source  string
}

// StateChangeEvent is the append-only history of an authoritative presence
// transition (ADR-067 decision 5): one row per resolved connect/disconnect edge, so
// a device's connectivity timeline is queryable alongside its telemetry (the live
// device-state projection holds only the LATEST presence — this table is the history
// DETECT/audit reads). State is the wire enum (CONNECTED|DISCONNECTED); SessionId is
// the producer's monotonic connect epoch (a host-observed session id, not a raw
// bdSeq). PresenceSource is deliberately NOT recorded — it is a projection-derived
// classification, not a fact of the resolved event. Like the other event tables it is
// a hypertable partitioned on occurred_time.
//
// Because a StateChange carries no AltId (the base-event dedup key), the child insert
// dedups against redelivery on an idempotency unique index
// (tenant_id, device_token, occurred_time, state, session_id): a birth+death at one
// instant differ by state and both survive, and a late higher-session echo differs by
// session_id and is retained for audit, but a genuinely redelivered row collides and
// is dropped.
type StateChangeEvent struct {
	rdb.TenantScoped
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	State        string            `gorm:"type:varchar(16);not null"`
	Reason       string
	SessionId    uint64 `gorm:"not null;default:0"`
}

// Information required to create a state change event.
type StateChangeEventCreateRequest struct {
	Event
	State     string
	Reason    string
	SessionId uint64
}

// AuditExempt opts the event tables out of the audit journal (ADR-019): they are
// the high-volume, append-only telemetry data plane — immutable facts, not the
// control-plane entity mutations the journal records.
func (Event) AuditExempt() bool            { return true }
func (EventAnchor) AuditExempt() bool      { return true }
func (LocationEvent) AuditExempt() bool    { return true }
func (MeasurementEvent) AuditExempt() bool { return true }
func (AlertEvent) AuditExempt() bool       { return true }
func (StateChangeEvent) AuditExempt() bool { return true }
