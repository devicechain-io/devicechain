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
//
// EventId is the event's OWN identity, derived from its content (see DeriveEventId).
// It exists because the tuple this table used to be keyed by —
// (tenant_id, device_token, event_type, occurred_time) — is not unique: a device that
// samples two sensors and publishes each as its own message under one shared timestamp
// produces two distinct events with one identical key. Parents are upserted ON CONFLICT
// DO NOTHING, so the second envelope was silently discarded while its payload rows still
// inserted and joined onto the first event. Worse, the discarded envelope's alt_id never
// reached the idempotency index, so the event became permanently un-deduplicable and each
// later redelivery re-inserted its rows.
//
// 🔴 The natural key remains a useful QUERY path and must never be treated as an identity
// again. Anything that needs to address one event addresses it by event_id.
type Event struct {
	rdb.TenantScoped
	EventId       []byte `gorm:"not null"`
	DeviceToken   string `gorm:"type:varchar(128)"`
	EventType     esmodel.EventType
	OccurredTime  time.Time
	Source        string
	AltId         sql.NullString
	ProcessedTime time.Time
}

// DefaultOrder implements rdb.Sortable. The event tables are the exception to the
// registry default: there is no id and no created_at here at all — the base event's
// primary key is the composite (tenant_id, event_id, occurred_time) — so the order is
// time-then-identity rather than created_at-then-id.
//
// occurred_time alone is NOT total. A device that samples two sensors and publishes
// each as its own message under one shared timestamp produces rows that tie, and a tie
// under LIMIT/OFFSET is the repeat-and-skip defect in miniature. event_id is the event's
// own content-derived identity and is unique within (tenant, occurred_time) by
// construction, so it closes the order.
//
// The leading column matches idx_events_tenant_device_type_time — declared
// (tenant_id, device_token, event_type, occurred_time DESC) — so the common per-device
// read stays index-ordered. Neither column is nullable, so no NULLS placement is needed.
func (Event) DefaultOrder() string {
	return "events.occurred_time DESC, events.event_id DESC"
}

// EventAnchor is one anchor of an event: a device's tracked-relationship target
// (ADR-013) denormalized so the event is queryable by that (anchor_type,
// anchor_token) dimension. Both the source device and the anchor target are named
// by their stable per-tenant tokens (ADR-044), not device-management row ids. It
// points back to the base event by EVENT_ID (occurred_time is carried alongside because
// it is the hypertable partition column, and device_token/event_type stay denormalized
// for the query indexes — none of the three identifies the event). One event has zero or
// more anchor rows.
type EventAnchor struct {
	rdb.TenantScoped
	EventId      []byte            `gorm:"not null"`
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
	// EventId names the base event this payload row belongs to. It replaced the
	// (device_token, event_type, occurred_time) join, which could not identify a
	// parent uniquely — two distinct events sharing that tuple made this row's
	// parentage ambiguous, and it silently resolved to whichever envelope won.
	EventId []byte `gorm:"not null"`
	// PayloadId is this ROW's identity, derived from its parent event and its own
	// content (see DerivePayloadId). It backs the per-row idempotency index: the
	// base event's key cannot cover payload rows, so without it a redelivery of an
	// event carrying no alternateId — which is every event lwm2m-ingest and
	// sparkplug-ingest produce — left one envelope owning N copies of its own rows.
	PayloadId    []byte            `gorm:"not null"`
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
	// Accuracy (horizontal, metres), Speed (metres per second) and Heading
	// (degrees clockwise from true north, [0, 360)) complete the GPS fix. They are
	// LAST in the struct because they arrived in an appended migration and
	// AutoMigrate appends — physical column order is asserted by the golden schema
	// diff, so moving them up here would make a fresh install disagree with an
	// upgraded one.
	//
	// Speed and heading are stored as REPORTED. They are derivable from
	// consecutive fixes and the two can legitimately disagree — a stationary
	// device with a noisy compass still reports a heading, and a device that
	// batches or drops fixes reports a speed no pair of stored positions would
	// reproduce — so nothing here recomputes or reconciles them.
	Accuracy sql.NullFloat64 `gorm:"type:decimal(12,4);"`
	Speed    sql.NullFloat64 `gorm:"type:decimal(12,4);"`
	Heading  sql.NullFloat64 `gorm:"type:decimal(7,4);"`
}

// DefaultOrder implements rdb.Sortable. Same time-then-identity shape as Event, but
// the payload tables have no primary key at all, so the tiebreak is payload_id — this
// ROW's content-derived identity, which is what makes the order total when a batch of
// samples shares one occurred_time. Both columns are NOT NULL, so NULLS placement does
// not arise.
func (LocationEvent) DefaultOrder() string {
	return "location_events.occurred_time DESC, location_events.payload_id DESC"
}

// Information required to create a location event.
type LocationEventCreateRequest struct {
	Event
	// EntryOccurredTime is THIS row's own instant — the sample's time, which the
	// resolver already decided (its own when the device reported one, else the
	// envelope's) and already bounded against the server clock.
	//
	// 🔴 IT IS A SEPARATE FIELD BECAUSE THE EMBEDDED Event MUST KEEP THE ENVELOPE'S
	// TIME. The embedded Event is the PARENT row, and every request in a batch carries
	// the same one: CreateXEvents upserts the parents deduplicated by event_id, so
	// moving a per-sample time onto the embedded Event would make N samples describe N
	// different parents that all share one identity — one of them would win, silently,
	// and it would carry a time its own derived event_id was not computed from.
	//
	// Zero is rejected at create rather than defaulted. A caller that forgets this
	// field would otherwise write a row at the zero instant, which reads as a real
	// (very old) reading and sorts before every genuine one.
	//
	// Two knock-on effects, both intended. A payload row can now land in an OLDER
	// hypertable chunk than its parent event, so retention (which drops chunks per
	// table) can reclaim a deeply-buffered reading before the envelope that delivered
	// it — correct, since retention is a statement about how much history to keep and
	// the reading IS the history. And an anchor row records the message's instant, not
	// the reading's, which is why the anchor join is keyed on event_id rather than on
	// (device_token, event_type, occurred_time) — see anchorKeySubquery.
	EntryOccurredTime time.Time
	Latitude          *float64
	Longitude         *float64
	Elevation         *float64
	Accuracy          *float64
	Speed             *float64
	Heading           *float64
}

// Measurement event fields. Unit and DataType are denormalized from the bound
// metric definition at resolve time (ADR-016), so a stored measurement is
// self-describing on read — a consumer resolves its unit/type off the row rather
// than joining back into device-management (a cross-service hop, ADR-044). They are
// null for an undeclared (unbound) measurement, and are snapshot-consistent: a
// later profile republish does not rewrite the unit/type of already-stored rows.
type MeasurementEvent struct {
	rdb.TenantScoped
	// EventId names the base event this payload row belongs to. It replaced the
	// (device_token, event_type, occurred_time) join, which could not identify a
	// parent uniquely — two distinct events sharing that tuple made this row's
	// parentage ambiguous, and it silently resolved to whichever envelope won.
	EventId []byte `gorm:"not null"`
	// PayloadId is this ROW's identity, derived from its parent event and its own
	// content (see DerivePayloadId). It backs the per-row idempotency index: the
	// base event's key cannot cover payload rows, so without it a redelivery of an
	// event carrying no alternateId — which is every event lwm2m-ingest and
	// sparkplug-ingest produce — left one envelope owning N copies of its own rows.
	PayloadId    []byte            `gorm:"not null"`
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

// DefaultOrder implements rdb.Sortable. Time-then-identity, tiebroken on payload_id
// for the reason given on LocationEvent — and this is the table where the tie is the
// common case rather than the corner one: a multi-metric report writes every named
// measurement at one shared occurred_time.
func (MeasurementEvent) DefaultOrder() string {
	return "measurement_events.occurred_time DESC, measurement_events.payload_id DESC"
}

// Information required to create a measurement event.
type MeasurementEventCreateRequest struct {
	Event
	// EntryOccurredTime is THIS row's own instant, kept apart from the embedded
	// (parent) Event's for the reason set out on LocationEventCreateRequest.
	EntryOccurredTime time.Time
	Name              string
	Value             *float64
	Classifier        *uint
	Unit              *string
	DataType          *string
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
	// EventId names the base event this payload row belongs to. It replaced the
	// (device_token, event_type, occurred_time) join, which could not identify a
	// parent uniquely — two distinct events sharing that tuple made this row's
	// parentage ambiguous, and it silently resolved to whichever envelope won.
	EventId []byte `gorm:"not null"`
	// PayloadId is this ROW's identity, derived from its parent event and its own
	// content (see DerivePayloadId). It backs the per-row idempotency index: the
	// base event's key cannot cover payload rows, so without it a redelivery of an
	// event carrying no alternateId — which is every event lwm2m-ingest and
	// sparkplug-ingest produce — left one envelope owning N copies of its own rows.
	PayloadId    []byte            `gorm:"not null"`
	DeviceToken  string            `gorm:"type:varchar(128);not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Type         string            `gorm:"not null"`
	Level        uint32            `gorm:"not null"`
	Message      string
	Source       string
}

// DefaultOrder implements rdb.Sortable. Time-then-identity, tiebroken on payload_id
// like the other two payload tables. Newest-first is the read this table is actually
// used for — an alert list is a "what just fired" surface — so the default and the
// operator's expectation are the same order.
func (AlertEvent) DefaultOrder() string {
	return "alert_events.occurred_time DESC, alert_events.payload_id DESC"
}

// Information required to create an alert event.
type AlertEventCreateRequest struct {
	Event
	// EntryOccurredTime is THIS row's own instant, kept apart from the embedded
	// (parent) Event's for the reason set out on LocationEventCreateRequest.
	EntryOccurredTime time.Time
	Type              string
	Level             uint32
	Message           string
	Source            string
}

// StateChangeEvent is the append-only history of an authoritative presence
// transition (ADR-067 decision 5): one row per resolved presence edge, so a device's
// connectivity timeline is queryable alongside its telemetry (the live device-state
// projection holds only the LATEST presence — this table is the history DETECT/audit
// reads). State is the wire enum (CONNECTED|DISCONNECTED|DEMOTED); a DEMOTED row is a
// custody release, not a connectivity edge, and is the audit trail of WHY a device
// stopped having authoritative presence at all. SessionId is
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
	// EventId names the base event this payload row belongs to. It replaced the
	// (device_token, event_type, occurred_time) join, which could not identify a
	// parent uniquely — two distinct events sharing that tuple made this row's
	// parentage ambiguous, and it silently resolved to whichever envelope won.
	EventId      []byte            `gorm:"not null"`
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
