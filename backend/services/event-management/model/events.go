// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Event with token references resolved. DeviceId names the originating device.
// The device's tracked-relationship targets are recorded as a *set* of anchors
// in the sibling EventAnchor table (ADR-013 addendum 2026-07-01) rather than a
// single denormalized pair, so an event assigned to several targets is queryable
// by each; an unassigned device's event simply has no anchor rows.
type Event struct {
	rdb.TenantScoped
	DeviceId      uint
	EventType     esmodel.EventType
	OccurredTime  time.Time
	Source        string
	AltId         sql.NullString
	ProcessedTime time.Time
}

// EventAnchor is one anchor of an event: a device's tracked-relationship target
// (ADR-013) denormalized so the event is queryable by that (anchor_type,
// anchor_id) dimension. It points back to the base event by its natural key
// (device_id, event_type, occurred_time); occurred_time is also the hypertable
// partition column. One event has zero or more anchor rows.
type EventAnchor struct {
	rdb.TenantScoped
	DeviceId     uint              `gorm:"not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	AnchorType   string            `gorm:"not null"`
	AnchorId     uint              `gorm:"not null"`
}

// Location event fields.
type LocationEvent struct {
	rdb.TenantScoped
	DeviceId     uint              `gorm:"not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
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

// Measurement event fields.
type MeasurementEvent struct {
	rdb.TenantScoped
	DeviceId     uint              `gorm:"not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
	Name         string            `gorm:"not null"`
	Value        sql.NullFloat64   `gorm:"type:decimal(20,8);"`
	Classifier   *uint
}

// Information required to create a measurement event.
type MeasurementEventCreateRequest struct {
	Event
	Name       string
	Value      *float64
	Classifier *uint
}

// Alert event fields.
type AlertEvent struct {
	rdb.TenantScoped
	DeviceId     uint              `gorm:"not null"`
	EventType    esmodel.EventType `gorm:"not null"`
	OccurredTime time.Time         `gorm:"not null"`
	Event        Event             `gorm:"foreignKey:DeviceId,EventType,OccurredTime;References:DeviceId,EventType,OccurredTime"`
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

// AuditExempt opts the event tables out of the audit journal (ADR-019): they are
// the high-volume, append-only telemetry data plane — immutable facts, not the
// control-plane entity mutations the journal records.
func (Event) AuditExempt() bool            { return true }
func (EventAnchor) AuditExempt() bool      { return true }
func (LocationEvent) AuditExempt() bool    { return true }
func (MeasurementEvent) AuditExempt() bool { return true }
func (AlertEvent) AuditExempt() bool       { return true }
