// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// The projection's hot upserts, as column-array statements (rdb.NewColumnUpsert): each batch of
// rows is ONE statement that binds one array per column, where the gorm Create it replaces
// bound one parameter per value and paid gorm's statement building and reflection for every
// one of them. The SQL EFFECT is unchanged — the same conflict target, the same update arm, the
// same guard — and so is the order the statement takes row locks in (the arrays are unnested
// in order, exactly as the VALUES list was walked); only the write statement changed. That is
// what keeps a mixed-version rollout safe, and the lock walk in the MergeProjectionBatch
// comment true.
//
// 🔴 THREE OF THE TABLES BELOW ARE device_states WITH DIFFERENT UPDATE ARMS: the lock (a no-op),
// the heartbeat (the data-event rule in SQL) and the write-back of a folded row. The columns
// are the same for all of them, because an INSERT arm writes every column.
//
// Audit: DeviceState, LatestMeasurement and LatestLocation are all AuditExempt (model.go) —
// derived, high-volume projections, not control-plane mutations — which the column-array
// insert requires of a model, since it leaves no audit-journal row. The erasure fence, the
// tenant stamp (bound as $1, and in every conflict target and update arm) and the refusal of a
// scoped or tenant-less context are done by the table itself.
//
// A batch must name each conflict key ONCE: PostgreSQL refuses an INSERT … ON CONFLICT DO
// UPDATE that touches one row twice. The callers' own shapes already guarantee it (a batch is
// grouped by device token, readings are coalesced to one per (device, name), fixes to one per
// device), and the equivalence test drives duplicates through them.

func deviceStateTenant(r *DeviceState) *string             { return &r.TenantId }
func latestMeasurementTenant(r *LatestMeasurement) *string { return &r.TenantId }
func latestLocationTenant(r *LatestLocation) *string       { return &r.TenantId }

// deviceStateColumns is every column a device_states insert writes but the tenant (bound from
// the context) and the generated id / soft-delete marker (the sequence and NULL apply). A
// column added to DeviceState and not listed here is refused when the first batch is written
// (the table checks it covers the model exactly), and TestDeviceStateUpdateArmCoversTheFold
// pins the folded arm to foldColumns.
var deviceStateColumns = []rdb.Column[DeviceState]{
	rdb.TimeColumn("created_at", func(r *DeviceState) time.Time { return r.CreatedAt }),
	rdb.TimeColumn("updated_at", func(r *DeviceState) time.Time { return r.UpdatedAt }),
	rdb.TextColumn("device_token", func(r *DeviceState) string { return r.DeviceToken }),
	rdb.TextColumn("external_id", func(r *DeviceState) string { return r.ExternalId }),
	rdb.TextColumn("source", func(r *DeviceState) string { return r.Source }),
	rdb.BoolColumn("active", func(r *DeviceState) bool { return r.Active }),
	rdb.NullTimeColumn("last_connect_time", func(r *DeviceState) sql.NullTime { return r.LastConnectTime }),
	rdb.NullTimeColumn("last_disconnect_time", func(r *DeviceState) sql.NullTime { return r.LastDisconnectTime }),
	rdb.NullTimeColumn("last_activity_time", func(r *DeviceState) sql.NullTime { return r.LastActivityTime }),
	rdb.NullTimeColumn("inactivity_alarm_time", func(r *DeviceState) sql.NullTime { return r.InactivityAlarmTime }),
	rdb.TextColumn("presence_source", func(r *DeviceState) string { return r.PresenceSource }),
	rdb.Uint64Column("session_id", func(r *DeviceState) uint64 { return r.SessionId }),
	rdb.NullTimeColumn("presence_time", func(r *DeviceState) sql.NullTime { return r.PresenceTime }),
	rdb.AnchorsColumn("scope_anchors", func(r *DeviceState) rdb.Anchors { return r.ScopeAnchors }),
	rdb.NullTimeColumn("scope_anchors_at", func(r *DeviceState) sql.NullTime { return r.ScopeAnchorsAt }),
}

// deviceStateLock is lockOrCreate's statement: a device with no row gets the blank row, and a
// device with one takes the conflict arm, whose WHERE is never true — so it writes nothing, but
// PostgreSQL locks a conflicting row whether or not the condition holds, which is the point.
// updated_at is the one assignment the arm must carry (an empty SET is refused), and it assigns
// the stored value to itself.
var deviceStateLock = rdb.NewColumnUpsert(deviceStateTenant, deviceStateKey,
	rdb.Upsert{
		Set:   []rdb.Assignment{rdb.SetTo("updated_at", "t.updated_at")},
		Where: "1 = 0",
	}, deviceStateColumns...)

// deviceStateFolded is writeFoldedStates' statement: on the device's key, overwrite the columns
// the fold reads and writes (foldColumns) from the incoming row, and stamp updated_at. The
// identity (id, created_at, the key) is never rewritten.
var deviceStateFolded = rdb.NewColumnUpsert(deviceStateTenant, deviceStateKey,
	rdb.Upsert{Set: []rdb.Assignment{
		rdb.SetTo("external_id", "excluded.external_id"),
		rdb.SetTo("source", "excluded.source"),
		rdb.SetTo("active", "excluded.active"),
		rdb.SetTo("last_connect_time", "excluded.last_connect_time"),
		rdb.SetTo("last_disconnect_time", "excluded.last_disconnect_time"),
		rdb.SetTo("last_activity_time", "excluded.last_activity_time"),
		rdb.SetTo("inactivity_alarm_time", "excluded.inactivity_alarm_time"),
		rdb.SetTo("presence_source", "excluded.presence_source"),
		rdb.SetTo("session_id", "excluded.session_id"),
		rdb.SetTo("presence_time", "excluded.presence_time"),
		rdb.SetTo("scope_anchors", "excluded.scope_anchors"),
		rdb.SetTo("scope_anchors_at", "excluded.scope_anchors_at"),
		rdb.SetTo("updated_at", "excluded.updated_at"),
	}}, deviceStateColumns...)

// heartbeatGuard is the DO UPDATE's WHERE: it EXCLUDES the one row a plain data event cannot
// be folded into in SQL — an INFERRED, inactive device the event is fresh for, which the
// event resurrects (Active, LastConnectTime and InactivityAlarmTime change, and which event
// supplies LastConnectTime depends on arrival order). The condition is applyEvent's test, in
// SQL: `fresh` is "no activity recorded, or the event is NOT OLDER" (>=, the frozen-clock
// rule), and the flip needs presence_source <> ASSERTED and not active. excluded holds the
// batch's folded row, whose last_activity_time is the latest time in the batch; a batch flips
// the row exactly when its latest event is fresh, because activity only ever rises through
// fresh events.
//
// A row this excludes is still LOCKED by the conflict arm, and is not counted in the rows
// written: that shortfall is how mergeHeartbeats finds the rows it must fold in Go.
const heartbeatGuard = "NOT (t.presence_source <> '" + PresenceSourceAsserted + "' AND NOT t.active AND " +
	"(t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time))"

// ownersNewer is applyOwners' test in SQL: the batch carries a stamp, and the stored row has
// none or an older one.
const ownersNewer = "excluded.scope_anchors_at IS NOT NULL AND (t.scope_anchors_at IS NULL " +
	"OR excluded.scope_anchors_at > t.scope_anchors_at)"

// deviceStateHeartbeat is mergeHeartbeats' statement. Its SET is applyEvent's data-event rule
// for a row heartbeatGuard let through, which cannot flip. There each event's only effects are
// the identity overwrite (the last non-empty value), the monotone advance of last_activity_time
// when the event is not older, and the owners stamp's strictly-later replacement — effects that
// commute, so one SET over the batch's folded row (excluded) equals folding its events in any
// order. updated_at is stamped as Save stamped it.
//
// applyEvent's data branch is the Go twin of this; model/projection_equivalence_test.go pins
// the two together.
var deviceStateHeartbeat = rdb.NewColumnUpsert(deviceStateTenant, deviceStateKey,
	rdb.Upsert{
		Set: []rdb.Assignment{
			rdb.SetTo("external_id", "CASE WHEN excluded.external_id <> '' "+
				"THEN excluded.external_id ELSE t.external_id END"),
			rdb.SetTo("source", "CASE WHEN excluded.source <> '' "+
				"THEN excluded.source ELSE t.source END"),
			rdb.SetTo("last_activity_time", "CASE WHEN "+
				"t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time "+
				"THEN excluded.last_activity_time ELSE t.last_activity_time END"),
			// The owners stamp (applyOwners): the batch's latest stamp replaces the stored one when
			// strictly later. Both arms test the OLD row's precedence — a SET reads the row as it was
			// before the statement on both databases — so they agree on whether to move.
			rdb.SetTo("scope_anchors", "CASE WHEN "+ownersNewer+
				" THEN excluded.scope_anchors ELSE t.scope_anchors END"),
			rdb.SetTo("scope_anchors_at", "CASE WHEN "+ownersNewer+
				" THEN excluded.scope_anchors_at ELSE t.scope_anchors_at END"),
			rdb.SetTo("updated_at", "excluded.updated_at"),
		},
		Where: heartbeatGuard,
	}, deviceStateColumns...)

// latestMeasurementGuard applies the DO UPDATE only when the incoming reading is STRICTLY
// newer than the stored one.
//
// 🔴 THE PROJECTION MUST NOT GO BACKWARDS — see the file comment of projection_merge.go.
const latestMeasurementGuard = "t.occurred_time < excluded.occurred_time"

// latestLocationGuard is latestMeasurementGuard for positions.
const latestLocationGuard = "t.occurred_time < excluded.occurred_time"

var latestMeasurementUpsert = rdb.NewColumnUpsert(latestMeasurementTenant,
	[]string{"tenant_id", "device_token", "name"},
	rdb.Upsert{
		Set: []rdb.Assignment{
			rdb.SetTo("value", "excluded.value"),
			rdb.SetTo("classifier", "excluded.classifier"),
			rdb.SetTo("unit", "excluded.unit"),
			rdb.SetTo("data_type", "excluded.data_type"),
			rdb.SetTo("occurred_time", "excluded.occurred_time"),
			rdb.SetTo("updated_at", "excluded.updated_at"),
		},
		Where: latestMeasurementGuard,
	},
	rdb.TimeColumn("created_at", func(r *LatestMeasurement) time.Time { return r.CreatedAt }),
	rdb.TimeColumn("updated_at", func(r *LatestMeasurement) time.Time { return r.UpdatedAt }),
	rdb.TextColumn("device_token", func(r *LatestMeasurement) string { return r.DeviceToken }),
	rdb.TextColumn("name", func(r *LatestMeasurement) string { return r.Name }),
	rdb.NumericColumn("value", func(r *LatestMeasurement) sql.NullFloat64 { return r.Value }),
	rdb.NullUintColumn("classifier", func(r *LatestMeasurement) *uint { return r.Classifier }),
	rdb.NullTextColumn("unit", func(r *LatestMeasurement) *string { return r.Unit }),
	rdb.NullTextColumn("data_type", func(r *LatestMeasurement) *string { return r.DataType }),
	rdb.TimeColumn("occurred_time", func(r *LatestMeasurement) time.Time { return r.OccurredTime }),
)

var latestLocationUpsert = rdb.NewColumnUpsert(latestLocationTenant,
	[]string{"tenant_id", "device_token"},
	rdb.Upsert{
		Set: []rdb.Assignment{
			rdb.SetTo("latitude", "excluded.latitude"),
			rdb.SetTo("longitude", "excluded.longitude"),
			rdb.SetTo("elevation", "excluded.elevation"),
			rdb.SetTo("accuracy", "excluded.accuracy"),
			rdb.SetTo("speed", "excluded.speed"),
			rdb.SetTo("heading", "excluded.heading"),
			rdb.SetTo("occurred_time", "excluded.occurred_time"),
			rdb.SetTo("updated_at", "excluded.updated_at"),
		},
		Where: latestLocationGuard,
	},
	rdb.TimeColumn("created_at", func(r *LatestLocation) time.Time { return r.CreatedAt }),
	rdb.TimeColumn("updated_at", func(r *LatestLocation) time.Time { return r.UpdatedAt }),
	rdb.TextColumn("device_token", func(r *LatestLocation) string { return r.DeviceToken }),
	rdb.NumericColumn("latitude", func(r *LatestLocation) sql.NullFloat64 { return r.Latitude }),
	rdb.NumericColumn("longitude", func(r *LatestLocation) sql.NullFloat64 { return r.Longitude }),
	rdb.NumericColumn("elevation", func(r *LatestLocation) sql.NullFloat64 { return r.Elevation }),
	rdb.NumericColumn("accuracy", func(r *LatestLocation) sql.NullFloat64 { return r.Accuracy }),
	rdb.NumericColumn("speed", func(r *LatestLocation) sql.NullFloat64 { return r.Speed }),
	rdb.NumericColumn("heading", func(r *LatestLocation) sql.NullFloat64 { return r.Heading }),
	rdb.TimeColumn("occurred_time", func(r *LatestLocation) time.Time { return r.OccurredTime }),
)

// pointers returns &rows[i] for every row: the column tables take rows by pointer, and the
// tenant stamp lands on the caller's own rows, as a Create's did.
func pointers[R any](rows []R) []*R {
	out := make([]*R, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}

// upsertDeviceStates writes rows through table (one of the three device_states tables above)
// and returns the rows written. updated_at is always now (a row read back never has it zero,
// and Save stamped it on every write); created_at only when the row has none — it belongs to
// the insert arm and no update rewrites it. The column table reads no clock, so it is stamped
// here, from the handle's NowFunc as gorm's Create does.
func upsertDeviceStates(tdb *gorm.DB, table *rdb.ColumnTable[DeviceState], rows []DeviceState) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	now := tdb.NowFunc()
	for i := range rows {
		rows[i].UpdatedAt = now
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = now
		}
	}
	return table.Upsert(tdb, pointers(rows))
}

// stampLatest gives a latest-value row its times the way a gorm Create does: each is set to now
// only when the row has none.
func stampLatest(now time.Time, created, updated *time.Time) {
	if created.IsZero() {
		*created = now
	}
	if updated.IsZero() {
		*updated = now
	}
}
