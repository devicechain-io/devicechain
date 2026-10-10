// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// The event store's batch writes, as column-array inserts (rdb.ColumnTable): one
// statement per table per call, binding one array per column whatever the number of rows,
// each with the ON CONFLICT … DO NOTHING arbiter on the row's own identity that the write
// has always carried. rdb.ColumnTable checks, the first time each is used, that it writes
// exactly what a gorm Create of the same rows would — every creatable column covered, no
// defaults, hooks, tokens or audit — and stamps and fences the tenant itself.
//
// Each conflict target is a key ON CONFLICT infers by its column SET, so the order here is
// not the key's (NewTimeLeadingKeysSchema).

func nullText(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

var eventColumns = rdb.NewColumnTable(
	func(e *Event) *string { return &e.TenantId },
	[]string{"tenant_id", "event_id", "occurred_time"},
	rdb.BytesColumn("event_id", func(e *Event) []byte { return e.EventId }),
	rdb.TextColumn("device_token", func(e *Event) string { return e.DeviceToken }),
	rdb.Int64Column("event_type", func(e *Event) int64 { return int64(e.EventType) }),
	rdb.TimeColumn("occurred_time", func(e *Event) time.Time { return e.OccurredTime }),
	rdb.TextColumn("source", func(e *Event) string { return e.Source }),
	rdb.NullTextColumn("alt_id", func(e *Event) *string { return nullText(e.AltId) }),
	rdb.TimeColumn("processed_time", func(e *Event) time.Time { return e.ProcessedTime }),
)

// payloadTarget is the arbiter every payload table shares: a row's own identity,
// (tenant_id, occurred_time, payload_id). See payloadConflict.
var payloadTarget = []string{"tenant_id", "payload_id", "occurred_time"}

var measurementColumns = rdb.NewColumnTable(
	func(m *MeasurementEvent) *string { return &m.TenantId },
	payloadTarget,
	rdb.BytesColumn("event_id", func(m *MeasurementEvent) []byte { return m.EventId }),
	rdb.BytesColumn("payload_id", func(m *MeasurementEvent) []byte { return m.PayloadId }),
	rdb.TextColumn("device_token", func(m *MeasurementEvent) string { return m.DeviceToken }),
	rdb.Int64Column("event_type", func(m *MeasurementEvent) int64 { return int64(m.EventType) }),
	rdb.TimeColumn("occurred_time", func(m *MeasurementEvent) time.Time { return m.OccurredTime }),
	rdb.TextColumn("name", func(m *MeasurementEvent) string { return m.Name }),
	rdb.NumericColumn("value", func(m *MeasurementEvent) sql.NullFloat64 { return m.Value }),
	// A uint in a bigint: refused above the bigint range rather than wrapped.
	rdb.NullUintColumn("classifier", func(m *MeasurementEvent) *uint { return m.Classifier }),
	rdb.NullTextColumn("unit", func(m *MeasurementEvent) *string { return m.Unit }),
	rdb.NullTextColumn("data_type", func(m *MeasurementEvent) *string { return m.DataType }),
)

var locationColumns = rdb.NewColumnTable(
	func(l *LocationEvent) *string { return &l.TenantId },
	payloadTarget,
	rdb.BytesColumn("event_id", func(l *LocationEvent) []byte { return l.EventId }),
	rdb.BytesColumn("payload_id", func(l *LocationEvent) []byte { return l.PayloadId }),
	rdb.TextColumn("device_token", func(l *LocationEvent) string { return l.DeviceToken }),
	rdb.Int64Column("event_type", func(l *LocationEvent) int64 { return int64(l.EventType) }),
	rdb.TimeColumn("occurred_time", func(l *LocationEvent) time.Time { return l.OccurredTime }),
	rdb.NumericColumn("latitude", func(l *LocationEvent) sql.NullFloat64 { return l.Latitude }),
	rdb.NumericColumn("longitude", func(l *LocationEvent) sql.NullFloat64 { return l.Longitude }),
	rdb.NumericColumn("elevation", func(l *LocationEvent) sql.NullFloat64 { return l.Elevation }),
	rdb.NumericColumn("accuracy", func(l *LocationEvent) sql.NullFloat64 { return l.Accuracy }),
	rdb.NumericColumn("speed", func(l *LocationEvent) sql.NullFloat64 { return l.Speed }),
	rdb.NumericColumn("heading", func(l *LocationEvent) sql.NullFloat64 { return l.Heading }),
)

var alertColumns = rdb.NewColumnTable(
	func(a *AlertEvent) *string { return &a.TenantId },
	payloadTarget,
	rdb.BytesColumn("event_id", func(a *AlertEvent) []byte { return a.EventId }),
	rdb.BytesColumn("payload_id", func(a *AlertEvent) []byte { return a.PayloadId }),
	rdb.TextColumn("device_token", func(a *AlertEvent) string { return a.DeviceToken }),
	rdb.Int64Column("event_type", func(a *AlertEvent) int64 { return int64(a.EventType) }),
	rdb.TimeColumn("occurred_time", func(a *AlertEvent) time.Time { return a.OccurredTime }),
	rdb.TextColumn("type", func(a *AlertEvent) string { return a.Type }),
	rdb.Int64Column("level", func(a *AlertEvent) int64 { return int64(a.Level) }),
	rdb.TextColumn("message", func(a *AlertEvent) string { return a.Message }),
	rdb.TextColumn("source", func(a *AlertEvent) string { return a.Source }),
)

// anchorColumns conflicts on uq_event_anchors_idem: (tenant_id, occurred_time, event_id,
// anchor_type, anchor_token). See CreateEventAnchors.
var anchorColumns = rdb.NewColumnTable(
	func(a *EventAnchor) *string { return &a.TenantId },
	[]string{"tenant_id", "event_id", "occurred_time", "anchor_type", "anchor_token"},
	rdb.BytesColumn("event_id", func(a *EventAnchor) []byte { return a.EventId }),
	rdb.TextColumn("device_token", func(a *EventAnchor) string { return a.DeviceToken }),
	rdb.Int64Column("event_type", func(a *EventAnchor) int64 { return int64(a.EventType) }),
	rdb.TimeColumn("occurred_time", func(a *EventAnchor) time.Time { return a.OccurredTime }),
	rdb.TextColumn("anchor_type", func(a *EventAnchor) string { return a.AnchorType }),
	rdb.TextColumn("anchor_token", func(a *EventAnchor) string { return a.AnchorToken }),
)
