// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Source commit: 796ecfaaa44846f44752fc9335bcb9c4573a720a (all v1.9.0 citations below).
// SNAPSHOT v1.9.0:backend/services/notification-management/model/model_state.go NotificationState. The embedded
// gorm.Model (gorm v1.31.2) and rdb.TenantScoped are expanded by hand. Never
// replace this with a live service model or a live embedded base.
type notificationStatePrev struct {
	ID                 uint `gorm:"primarykey"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          gorm.DeletedAt `gorm:"index"`
	TenantId           string         `gorm:"index;not null;size:128"`
	AlarmToken         string         `gorm:"not null;size:128"`
	AlarmKey           string         `gorm:"not null;size:256"`
	Severity           string         `gorm:"size:16"`
	PositionDerived    *bool
	FirstNotifiedAt    sql.NullTime
	LastNotifiedAt     sql.NullTime
	NotifyCount        int
	AcknowledgedAt     sql.NullTime
	ClearedAt          sql.NullTime
	EscalationLevel    int
	LastEscalatedAt    sql.NullTime
	EscalationBaseAt   sql.NullTime `gorm:"column:escalation_base_at"`
	CycleRaisedAt      sql.NullTime
	ClearNotifiedAt    sql.NullTime
	ClearNotifiedCycle sql.NullTime
	PagedTargets       *datatypes.JSON
	PagedCycle         sql.NullTime
	OriginatorType     sql.NullString
	OriginatorId       sql.NullInt64
	DeviceToken        sql.NullString
	DeviceName         sql.NullString
	DeviceTypeToken    sql.NullString
	SourceAt           sql.NullTime
}

func (notificationStatePrev) TableName() string { return "notification-management.notification_states" }

func TestPrevReleaseNotificationStateWriters(t *testing.T) {
	db := prevReleaseConn(t)
	// Roll back all replay rows so reruns (including -count) start cleanly.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "prev-release-notification-writer"
	require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(&notificationStatePrev{}).Error)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: true} }
	read := func(want *notificationStatePrev) {
		t.Helper()
		var got notificationStatePrev
		require.NoError(t, tx.Where("tenant_id = ? AND alarm_token = ?", tenant, want.AlarmToken).First(&got).Error)
		// Time.Equal ignores the driver's location representation; compare all other fields exactly.
		require.WithinDuration(t, want.CreatedAt, got.CreatedAt, time.Microsecond)
		require.WithinDuration(t, want.UpdatedAt, got.UpdatedAt, time.Microsecond)
		got.CreatedAt, got.UpdatedAt = want.CreatedAt, want.UpdatedAt
		for _, pair := range [][2]*sql.NullTime{
			{&got.FirstNotifiedAt, &want.FirstNotifiedAt}, {&got.LastNotifiedAt, &want.LastNotifiedAt},
			{&got.AcknowledgedAt, &want.AcknowledgedAt}, {&got.ClearedAt, &want.ClearedAt},
			{&got.LastEscalatedAt, &want.LastEscalatedAt}, {&got.EscalationBaseAt, &want.EscalationBaseAt}, {&got.CycleRaisedAt, &want.CycleRaisedAt},
			{&got.ClearNotifiedAt, &want.ClearNotifiedAt}, {&got.ClearNotifiedCycle, &want.ClearNotifiedCycle},
			{&got.PagedCycle, &want.PagedCycle}, {&got.SourceAt, &want.SourceAt},
		} {
			require.Equal(t, pair[1].Valid, pair[0].Valid)
			if pair[1].Valid {
				require.True(t, pair[1].Time.Equal(pair[0].Time))
				pair[0].Time = pair[1].Time
			}
		}
		if want.PagedTargets != nil {
			require.NotNil(t, got.PagedTargets)
			require.JSONEq(t, string(*want.PagedTargets), string(*got.PagedTargets))
			got.PagedTargets = want.PagedTargets
		}
		require.Equal(t, *want, got)
	}
	// v1.9.0:backend/services/notification-management/model/api_state.go RecordPagedFrom (also RecordPaged/RecordNotification), new-state Create.
	row := notificationStatePrev{TenantId: tenant, AlarmToken: "raised", AlarmKey: "temperature", Severity: "MAJOR",
		FirstNotifiedAt: stamp(at), LastNotifiedAt: stamp(at), NotifyCount: 1, CycleRaisedAt: stamp(at), EscalationBaseAt: stamp(at)}
	// Source facts and the paged document are populated before the first Create.
	pages := datatypes.JSON(`{"cycle":"2026-11-01T12:00:00Z","targets":[{"channelId":7,"channel":"ops","recipients":["oncall"]}]}`)
	row.PagedTargets, row.PagedCycle = &pages, stamp(at)
	row.OriginatorType = sql.NullString{String: "device", Valid: true}
	row.OriginatorId = sql.NullInt64{Int64: 42, Valid: true}
	row.DeviceToken = sql.NullString{String: "device", Valid: true}
	row.DeviceName = sql.NullString{String: "Boiler", Valid: true}
	row.DeviceTypeToken = sql.NullString{String: "sensor", Valid: true}
	row.SourceAt = stamp(at)
	require.NoError(t, tx.Create(&row).Error)
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go RecordPagedFrom: existing-row full Save, including source/paged fields.
	row.NotifyCount++
	row.LastNotifiedAt = stamp(at.Add(time.Minute))
	row.EscalationBaseAt = stamp(at.Add(time.Minute))
	row.Severity = "CRITICAL"
	row.DeviceName = sql.NullString{String: "Boiler renamed", Valid: true}
	row.SourceAt = stamp(at.Add(time.Minute))
	positionFalse := false
	row.PositionDerived = &positionFalse
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go
	// RecordPagedFrom/markTerminal: stale-cycle and already-terminal branches write
	// only position_derived through Model(found).Update, including a nullable pointer.
	// v1.9.0:backend/services/notification-management/model/position_withholding.go
	// applyPosition: false may become unknown; true remains sticky across cycles.
	for _, verdict := range []*bool{nil, &positionFalse} {
		res := tx.Model(&row).Where("tenant_id = ?", tenant).Update("position_derived", verdict)
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		row.PositionDerived = verdict
		read(&row)
	}
	// v1.9.0:backend/services/notification-management/model/position_withholding.go
	// verifiedHistory: the internal cache repair binds SQL NULL, and only cached false
	// matches. A repeat repair must leave an unknown classification untouched.
	for _, wantRows := range []int64{1, 0} {
		res := tx.Model(&notificationStatePrev{}).Where("tenant_id = ?", tenant).
			Where("alarm_token = ? AND position_derived IS FALSE", row.AlarmToken).
			Update("position_derived", nil)
		require.NoError(t, res.Error)
		require.Equal(t, wantRows, res.RowsAffected)
		row.PositionDerived = nil
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
	}
	// v1.9.0:backend/services/notification-management/model/position_withholding.go
	// RecordAlarmPosition: every value kind (false, explicit NULL, true), with the
	// atomic IS NOT TRUE predicate that prevents a later producer clearing true.
	positionTrue := true
	for _, verdict := range []*bool{&positionFalse, nil, &positionTrue, nil, &positionFalse} {
		var value any
		if verdict != nil {
			value = *verdict
		}
		sticky := row.PositionDerived != nil && *row.PositionDerived
		res := tx.Model(&notificationStatePrev{}).Where("tenant_id = ?", tenant).
			Where("alarm_token = ? AND position_derived IS NOT TRUE", row.AlarmToken).
			Update("position_derived", value)
		require.NoError(t, res.Error)
		if sticky {
			require.Zero(t, res.RowsAffected)
		} else {
			require.EqualValues(t, 1, res.RowsAffected)
			row.PositionDerived = verdict
		}
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
	}
	// v1.9.0:backend/services/notification-management/model/api_state.go TouchSeverity uses Update, not UpdateColumn: gorm also stamps updated_at.
	res := tx.Model(&notificationStatePrev{}).Where("tenant_id = ? AND alarm_token = ?", tenant, row.AlarmToken).Update("severity", "MINOR")
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	row.Severity = "MINOR"
	require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go ClaimEscalation: exact map, value kinds and both cycle predicates.
	for _, knownCycle := range []bool{true, false} {
		if !knownCycle {
			row.CycleRaisedAt = sql.NullTime{}
			require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
			read(&row)
		}
		q := tx.Model(&notificationStatePrev{}).Where("tenant_id = ?", tenant).
			Where("alarm_token = ? AND escalation_level = ? AND acknowledged_at IS NULL AND cleared_at IS NULL", row.AlarmToken, row.EscalationLevel)
		if knownCycle {
			q = q.Where("cycle_raised_at = ?", row.CycleRaisedAt.Time)
		} else {
			q = q.Where("cycle_raised_at IS NULL")
		}
		at = at.Add(time.Minute)
		res = q.Updates(map[string]any{
			"escalation_level":   gorm.Expr("escalation_level + 1"),
			"last_escalated_at":  at,
			"escalation_base_at": at,
			"last_notified_at":   at,
			"notify_count":       gorm.Expr("notify_count + 1"),
		})
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		row.EscalationLevel++
		row.NotifyCount++
		row.LastEscalatedAt = stamp(at)
		row.EscalationBaseAt = stamp(at)
		row.LastNotifiedAt = stamp(at)
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
	}
	// v1.9.0:backend/services/notification-management/model/api_state.go markTerminal (MarkAcknowledged and MarkCleared) uses full-row Save in both
	// the stamp branch and the cycle-adoption-only branch; there are no UpdateColumns maps.
	row.AcknowledgedAt = stamp(at)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	row.CycleRaisedAt = stamp(at)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	row.ClearedAt = stamp(at)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go AddPagedTargets:
	// Exact Updates map; it does not increment NotifyCount. The stored document
	// names the earlier cycle, so mergePaged replaces it with this cycle's target.
	row.CycleRaisedAt = stamp(at)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	merged := datatypes.JSON(`{"cycle":"` + at.Format(time.RFC3339Nano) + `","targets":[{"channelId":8,"channel":"backup"}]}`)
	res = tx.Model(&row).Where("tenant_id = ?", tenant).Updates(map[string]any{
		"paged_targets": &merged,
		"paged_cycle":   row.CycleRaisedAt,
	})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	row.PagedTargets, row.PagedCycle = &merged, row.CycleRaisedAt
	read(&row)

	// v1.9.0:backend/services/notification-management/model/api_state.go ClaimClearAnnouncement:
	// both distinct maps and their cycle predicates, including a losing repeat claim.
	for _, knownCycle := range []bool{true, false} {
		if !knownCycle {
			row.CycleRaisedAt = sql.NullTime{}
			row.ClearNotifiedAt = sql.NullTime{}
			row.ClearNotifiedCycle = sql.NullTime{}
			require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
			read(&row)
		}
		claimAt := at.Add(10 * time.Second)
		claim := func() *gorm.DB {
			q := tx.Model(&notificationStatePrev{}).Where("tenant_id = ?", tenant).
				Where("alarm_token = ? AND cleared_at IS NOT NULL", row.AlarmToken)
			set := map[string]any{"clear_notified_at": claimAt}
			if knownCycle {
				q = q.Where("cycle_raised_at = ? AND (clear_notified_cycle IS NULL OR clear_notified_cycle <> ?)", row.CycleRaisedAt.Time, row.CycleRaisedAt.Time)
				set["clear_notified_cycle"] = row.CycleRaisedAt.Time
			} else {
				q = q.Where("cycle_raised_at IS NULL AND clear_notified_at IS NULL")
			}
			return q.Updates(set)
		}
		res = claim()
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		row.ClearNotifiedAt = stamp(claimAt)
		if knownCycle {
			row.ClearNotifiedCycle = row.CycleRaisedAt
		}
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
		res = claim()
		require.NoError(t, res.Error)
		require.Zero(t, res.RowsAffected)
		read(&row)

		// v1.9.0:backend/services/notification-management/model/api_state.go UnclaimClearAnnouncement:
		// both columns explicitly bind NULL, and only the caller's stamp matches.
		res = tx.Model(&notificationStatePrev{}).Where("tenant_id = ?", tenant).
			Where("alarm_token = ? AND clear_notified_at = ?", row.AlarmToken, claimAt).
			Updates(map[string]any{"clear_notified_at": nil, "clear_notified_cycle": nil})
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		row.ClearNotifiedAt, row.ClearNotifiedCycle = sql.NullTime{}, sql.NullTime{}
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
	}
	// Seed a prior claim so startCycle verifies its asymmetric reset: the stamp is
	// cleared while the previous claim's cycle remains stored.
	row.ClearNotifiedAt, row.ClearNotifiedCycle = stamp(at), stamp(at)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go startCycle (via RecordPagedFrom/markTerminal): reset per-cycle fields.
	// ClearNotifiedCycle and source facts are deliberately retained by the tagged code.
	// This replays markTerminal after startCycle: the new cycle has not paged yet.
	row.CycleRaisedAt = stamp(at.Add(time.Hour))
	row.FirstNotifiedAt = sql.NullTime{}
	row.NotifyCount = 0
	row.EscalationLevel = 0
	row.AcknowledgedAt = stamp(at.Add(time.Hour))
	row.ClearedAt = sql.NullTime{}
	row.LastEscalatedAt = sql.NullTime{}
	row.EscalationBaseAt = sql.NullTime{}
	row.ClearNotifiedAt = sql.NullTime{}
	row.PagedTargets = nil
	row.PagedCycle = sql.NullTime{}
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read(&row)
	// v1.9.0:backend/services/notification-management/model/api_state.go markTerminal: tombstone Create, once for each terminal column.
	for _, column := range []string{"acknowledged_at", "cleared_at"} {
		tombstone := notificationStatePrev{TenantId: tenant, AlarmToken: column, AlarmKey: "temperature", Severity: "MAJOR", CycleRaisedAt: stamp(at)}
		if column == "cleared_at" {
			tombstone.ClearedAt = stamp(at)
			tombstone.PositionDerived = &positionFalse
		} else {
			tombstone.AcknowledgedAt = stamp(at)
			tombstone.PositionDerived = &positionTrue
		}
		require.NoError(t, tx.Create(&tombstone).Error)
		read(&tombstone)
	}
	// v1.9.0:backend/services/notification-management/model/api_state.go PruneResolvedStates: exact hard-delete predicate, with explicit tenant scope.
	res = tx.Unscoped().Where("tenant_id = ?", tenant).
		Where("(cleared_at IS NOT NULL OR acknowledged_at IS NOT NULL)").
		Where("(cleared_at IS NULL OR cleared_at < ?)", at.Add(time.Second)).
		Where("(acknowledged_at IS NULL OR acknowledged_at < ?)", at.Add(time.Second)).Delete(&notificationStatePrev{})
	require.NoError(t, res.Error)
	require.EqualValues(t, 2, res.RowsAffected)
	var remaining []notificationStatePrev
	require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, row.ID, remaining[0].ID)
	read(&row)
}

func prevStateJSON(t *testing.T, db *gorm.DB, table, tenant string) map[string]any {
	t.Helper()
	var raw string
	require.NoError(t, db.Raw(fmt.Sprintf(`SELECT to_jsonb(t)::text FROM "device-state".%s t WHERE tenant_id = ? AND device_token = ?`, table), tenant, "device").Row().Scan(&raw))
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &row))
	return row
}

// Frozen builder output: v1.9.0:backend/core/rdb/insert_columns.go build and
// v1.9.0:backend/core/rdb/insert_columns_upsert.go upsertAssignments, with columns/arms from
// v1.9.0:backend/services/device-state/model/columnar_writes.go. These writers have no literal SQL at
// the call site: this is the exact generated Postgres text for one-row arrays.
// The production RdbManager table prefix qualifies the schema (not search_path alone).
// The column definitions, builders and dropped-measurement writers at the source
// commit above are byte-identical to v1.8.0, so all seven frozen SQL constants
// are retained, including the tenant callbacks' conflict and eviction guards.
// v1.9.0:backend/services/device-state/model/projection_merge.go lockOrCreate (via deviceStateLock).
const deviceStateLockPrevSQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","incarnation","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::int8[]), unnest($6::text[]), unnest($7::text[]), unnest($8::bool[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::timestamptz[]), unnest($13::text[]), unnest($14::int8[]), unnest($15::timestamptz[]), unnest($16::text[])::text[], unnest($17::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "updated_at" = t.updated_at WHERE (1 = 0) AND t."tenant_id" = $1::text`

// v1.9.0:backend/services/device-state/model/projection_merge.go writeFoldedStates (via deviceStateFolded).
const deviceStateFoldedPrevSQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","incarnation","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::int8[]), unnest($6::text[]), unnest($7::text[]), unnest($8::bool[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::timestamptz[]), unnest($13::text[]), unnest($14::int8[]), unnest($15::timestamptz[]), unnest($16::text[])::text[], unnest($17::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "external_id" = excluded.external_id, "source" = excluded.source, "active" = excluded.active, "last_connect_time" = excluded.last_connect_time, "last_disconnect_time" = excluded.last_disconnect_time, "last_activity_time" = excluded.last_activity_time, "inactivity_alarm_time" = excluded.inactivity_alarm_time, "presence_source" = excluded.presence_source, "session_id" = excluded.session_id, "presence_time" = excluded.presence_time, "scope_anchors" = excluded.scope_anchors, "scope_anchors_at" = excluded.scope_anchors_at, "updated_at" = excluded.updated_at WHERE t."tenant_id" = $1::text`

// v1.9.0:backend/services/device-state/model/projection_merge.go mergeHeartbeats (via deviceStateHeartbeat).
const deviceStateHeartbeatPrevSQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","incarnation","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::int8[]), unnest($6::text[]), unnest($7::text[]), unnest($8::bool[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::timestamptz[]), unnest($13::text[]), unnest($14::int8[]), unnest($15::timestamptz[]), unnest($16::text[])::text[], unnest($17::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "external_id" = CASE WHEN excluded.external_id <> '' THEN excluded.external_id ELSE t.external_id END, "source" = CASE WHEN excluded.source <> '' THEN excluded.source ELSE t.source END, "last_activity_time" = CASE WHEN t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time THEN excluded.last_activity_time ELSE t.last_activity_time END, "scope_anchors" = CASE WHEN excluded.scope_anchors_at IS NOT NULL AND (t.scope_anchors_at IS NULL OR excluded.scope_anchors_at > t.scope_anchors_at) THEN excluded.scope_anchors ELSE t.scope_anchors END, "scope_anchors_at" = CASE WHEN excluded.scope_anchors_at IS NOT NULL AND (t.scope_anchors_at IS NULL OR excluded.scope_anchors_at > t.scope_anchors_at) THEN excluded.scope_anchors_at ELSE t.scope_anchors_at END, "updated_at" = excluded.updated_at WHERE (NOT (t.presence_source <> 'ASSERTED' AND NOT t.active AND (t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time)) AND (excluded.incarnation IS NULL OR t.incarnation IS NULL OR excluded.incarnation = t.incarnation)) AND t."tenant_id" = $1::text`

// v1.9.0:backend/services/device-state/model/projection_merge.go upsertLatestMeasurements / MergeLatestMeasurements (via latestMeasurementUpsert).
const latestMeasurementUpsertPrevSQL = `INSERT INTO "device-state"."latest_measurements" AS t ("tenant_id","created_at","updated_at","device_token","name","value","classifier","unit","data_type","occurred_time") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]::numeric[]), unnest($7::int8[]), unnest($8::text[]), unnest($9::text[]), unnest($10::timestamptz[]) ON CONFLICT ("tenant_id","device_token","name") DO UPDATE SET "value" = excluded.value, "classifier" = excluded.classifier, "unit" = excluded.unit, "data_type" = excluded.data_type, "occurred_time" = excluded.occurred_time, "updated_at" = excluded.updated_at WHERE (t.occurred_time < excluded.occurred_time) AND t."tenant_id" = $1::text`

// v1.9.0:backend/services/device-state/model/projection_merge.go upsertLatestLocations / MergeLatestLocations (via latestLocationUpsert).
const latestLocationUpsertPrevSQL = `INSERT INTO "device-state"."latest_locations" AS t ("tenant_id","created_at","updated_at","device_token","latitude","longitude","elevation","accuracy","speed","heading","occurred_time") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]::numeric[]), unnest($6::text[]::numeric[]), unnest($7::text[]::numeric[]), unnest($8::text[]::numeric[]), unnest($9::text[]::numeric[]), unnest($10::text[]::numeric[]), unnest($11::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "latitude" = excluded.latitude, "longitude" = excluded.longitude, "elevation" = excluded.elevation, "accuracy" = excluded.accuracy, "speed" = excluded.speed, "heading" = excluded.heading, "occurred_time" = excluded.occurred_time, "updated_at" = excluded.updated_at WHERE (t.occurred_time < excluded.occurred_time) AND t."tenant_id" = $1::text`

// v1.9.0:backend/services/device-state/model/dropped_measurements.go upsertDropped/droppedSet. Frozen gorm
// CreateInBatches output for one row (ID omitted, RETURNING id); the only dynamic
// part is the repeated VALUES group. The tenant callback also guards DO UPDATE.
const droppedUpsertPrevSQL = `INSERT INTO "device-state"."dropped_measurements" ("created_at","updated_at","tenant_id","device_token","name","reason","last_value","count","first_time","last_time") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT ("tenant_id","device_token","name","reason") DO UPDATE SET "count"=dropped_measurements.count + excluded.count,"first_time"=CASE WHEN excluded.first_time < dropped_measurements.first_time THEN excluded.first_time ELSE dropped_measurements.first_time END,"last_value"=CASE WHEN excluded.last_time > dropped_measurements.last_time THEN excluded.last_value ELSE dropped_measurements.last_value END,"last_time"=CASE WHEN excluded.last_time > dropped_measurements.last_time THEN excluded.last_time ELSE dropped_measurements.last_time END,"updated_at"="excluded"."updated_at" WHERE "dropped_measurements"."tenant_id" = "excluded"."tenant_id"  RETURNING "id"`

// v1.9.0:backend/services/device-state/model/dropped_measurements.go enforceDroppedBound. Representative gorm
// subquery output, including the explicit inner tenant and both callback predicates.
// MaxDroppedMeasurementsPerDevice=32; evictScan=100000. No soft-delete field on this table.
const droppedEvictPrevSQL = `DELETE FROM "device-state"."dropped_measurements" WHERE id IN (SELECT "id" FROM "device-state"."dropped_measurements" WHERE (tenant_id = $1 AND device_token = $2) AND "dropped_measurements"."tenant_id" = $3 ORDER BY last_time DESC, id DESC LIMIT $4 OFFSET $5) AND "dropped_measurements"."tenant_id" = $6`

func TestPrevReleaseDeviceStateWriters(t *testing.T) {
	db := prevReleaseConn(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "prev-release-device-writer"
	for _, table := range []string{"device_states", "latest_measurements", "latest_locations", "dropped_measurements"} {
		require.NoError(t, tx.Exec(fmt.Sprintf(`DELETE FROM "device-state".%s WHERE tenant_id = ?`, table), tenant).Error)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later := at.Add(time.Minute)
	// The column arrays have one element each, as the previous-release per-device path binds them.
	// Numeric values travel as decimal strings, and anchors as PostgreSQL array literals.
	stateArgs := func(at time.Time, external string) []any {
		return []any{tenant, []time.Time{at}, []time.Time{at}, []string{"device"}, []*int64{nil}, []string{external},
			[]string{"source"}, []bool{true}, []*time.Time{&at}, []*time.Time{nil}, []*time.Time{&at},
			[]*time.Time{nil}, []string{"INFERRED"}, []int64{0}, []*time.Time{nil}, []*string{nil}, []*time.Time{nil}}
	}
	assertState := func(external string, at time.Time) {
		t.Helper()
		row := prevStateJSON(t, tx, "device_states", tenant)
		require.Equal(t, external, row["external_id"])
		require.Equal(t, "source", row["source"])
		require.Equal(t, true, row["active"])
		require.Equal(t, "INFERRED", row["presence_source"])
		require.Equal(t, float64(0), row["session_id"])
		for _, c := range []string{"last_disconnect_time", "inactivity_alarm_time", "presence_time", "scope_anchors", "scope_anchors_at", "deleted_at", "incarnation"} {
			require.Nil(t, row[c], c)
		}
		for _, c := range []string{"last_connect_time", "last_activity_time"} {
			parsed, err := time.Parse(time.RFC3339Nano, row[c].(string))
			require.NoError(t, err)
			require.True(t, at.Equal(parsed), c)
		}
		// Already obsolete in previous-release's model; its migrated default still accepts omitted values.
		require.Equal(t, float64(600), row["inactivity_timeout"])
	}
	// lockOrCreate: insert and then the deliberately non-writing conflict/row-lock arm.
	require.NoError(t, tx.Exec(deviceStateLockPrevSQL, stateArgs(at, "first")...).Error)
	assertState("first", at)
	res := tx.Exec(deviceStateLockPrevSQL, stateArgs(later, "ignored")...)
	require.NoError(t, res.Error)
	require.Zero(t, res.RowsAffected)
	assertState("first", at)
	// writeFoldedStates: insert, then overwrite through the conflict arm.
	require.NoError(t, tx.Exec(`DELETE FROM "device-state".device_states WHERE tenant_id = ?`, tenant).Error)
	require.NoError(t, tx.Exec(deviceStateFoldedPrevSQL, stateArgs(at, "folded")...).Error)
	assertState("folded", at)
	require.NoError(t, tx.Exec(deviceStateFoldedPrevSQL, stateArgs(later, "folded-again")...).Error)
	assertState("folded-again", later)
	// mergeHeartbeats: first sight and the monotone data-event merge, then a stale replay.
	require.NoError(t, tx.Exec(`DELETE FROM "device-state".device_states WHERE tenant_id = ?`, tenant).Error)
	require.NoError(t, tx.Exec(deviceStateHeartbeatPrevSQL, stateArgs(at, "heartbeat")...).Error)
	assertState("heartbeat", at)
	args := stateArgs(later, "heartbeat-again")
	require.NoError(t, tx.Exec(deviceStateHeartbeatPrevSQL, args...).Error)
	// Heartbeats advance activity, but retain the original connect time.
	row := prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, "heartbeat-again", row["external_id"])
	parsed, err := time.Parse(time.RFC3339Nano, row["last_activity_time"].(string))
	require.NoError(t, err)
	require.True(t, later.Equal(parsed))
	parsed, err = time.Parse(time.RFC3339Nano, row["last_connect_time"].(string))
	require.NoError(t, err)
	require.True(t, at.Equal(parsed))
	require.NoError(t, tx.Exec(deviceStateHeartbeatPrevSQL, stateArgs(at, "")...).Error)
	row = prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, "heartbeat-again", row["external_id"])
	parsed, err = time.Parse(time.RFC3339Nano, row["last_activity_time"].(string))
	require.NoError(t, err)
	require.True(t, later.Equal(parsed))

	// upsertLatestMeasurements and upsertLatestLocations (also used by batch merge).
	for i, when := range []time.Time{at, later, at} {
		value := "12.25"
		expected := 12.25
		if i == 1 {
			value = "13.5"
		}
		if i > 0 {
			expected = 13.5
		}
		classifier := int64(7)
		unit, dataType := "C", "NUMERIC"
		args := []any{tenant, []time.Time{when}, []time.Time{when}, []string{"device"}, []string{"temperature"}, []*string{&value}, []*int64{&classifier}, []*string{&unit}, []*string{&dataType}, []time.Time{when}}
		require.NoError(t, tx.Exec(latestMeasurementUpsertPrevSQL, args...).Error)
		row := prevStateJSON(t, tx, "latest_measurements", tenant)
		require.Equal(t, expected, row["value"])
		require.Equal(t, "temperature", row["name"])
		require.Equal(t, float64(7), row["classifier"])
		require.Equal(t, "C", row["unit"])
		require.Equal(t, "NUMERIC", row["data_type"])
		wantTime := when
		if i == 2 {
			wantTime = later
		}
		parsed, err := time.Parse(time.RFC3339Nano, row["occurred_time"].(string))
		require.NoError(t, err)
		require.True(t, wantTime.Equal(parsed))
		coords := []string{"40.25", "-73.5", "20", "3", "4", "90"}
		if i == 1 {
			coords[0] = "41.25"
		}
		args = []any{tenant, []time.Time{when}, []time.Time{when}, []string{"device"}}
		for _, v := range coords {
			v := v
			args = append(args, []*string{&v})
		}
		args = append(args, []time.Time{when})
		require.NoError(t, tx.Exec(latestLocationUpsertPrevSQL, args...).Error)
		row = prevStateJSON(t, tx, "latest_locations", tenant)
		latitude := 40.25
		if i > 0 {
			latitude = 41.25
		}
		for c, want := range map[string]float64{"latitude": latitude, "longitude": -73.5, "elevation": 20, "accuracy": 3, "speed": 4, "heading": 90} {
			require.Equal(t, want, row[c], c)
		}
		parsed, err = time.Parse(time.RFC3339Nano, row["occurred_time"].(string))
		require.NoError(t, err)
		require.True(t, wantTime.Equal(parsed))
	}
	// v1.9.0:backend/services/device-state/model/columnar_writes.go nullable
	// NumericColumn, NullUintColumn and NullTextColumn values on newer readings/fixes.
	// Invalid sql.NullFloat64 and nil metadata must overwrite earlier non-NULL values.
	nullAt := later.Add(time.Minute)
	require.NoError(t, tx.Exec(latestMeasurementUpsertPrevSQL, tenant, []time.Time{nullAt}, []time.Time{nullAt},
		[]string{"device"}, []string{"temperature"}, []*string{nil}, []*int64{nil}, []*string{nil}, []*string{nil}, []time.Time{nullAt}).Error)
	row = prevStateJSON(t, tx, "latest_measurements", tenant)
	for _, column := range []string{"value", "classifier", "unit", "data_type"} {
		require.Nil(t, row[column], column)
	}
	nullLocation := []any{tenant, []time.Time{nullAt}, []time.Time{nullAt}, []string{"device"}}
	for range 6 {
		nullLocation = append(nullLocation, []*string{nil})
	}
	nullLocation = append(nullLocation, []time.Time{nullAt})
	require.NoError(t, tx.Exec(latestLocationUpsertPrevSQL, nullLocation...).Error)
	row = prevStateJSON(t, tx, "latest_locations", tenant)
	for _, column := range []string{"latitude", "longitude", "elevation", "accuracy", "speed", "heading"} {
		require.Nil(t, row[column], column)
	}
	// upsertDropped: insert then the additive-count/earliest-first/latest-value merge.
	for i, when := range []time.Time{at, later, at} {
		value := fmt.Sprintf("drop-%d", i)
		require.NoError(t, tx.Exec(droppedUpsertPrevSQL, when, when, tenant, "device", "text", "UNDECLARED", value, int64(1), when, when).Error)
		row := prevStateJSON(t, tx, "dropped_measurements", tenant)
		require.Equal(t, float64(i+1), row["count"])
		require.Equal(t, "UNDECLARED", row["reason"])
		require.Equal(t, "text", row["name"])
		want := "drop-0"
		if i > 0 {
			want = "drop-1"
		}
		require.Equal(t, want, row["last_value"])
		parsed, err := time.Parse(time.RFC3339Nano, row["first_time"].(string))
		require.NoError(t, err)
		require.True(t, at.Equal(parsed))
		wantTime := at
		if i > 0 {
			wantTime = later
		}
		parsed, err = time.Parse(time.RFC3339Nano, row["last_time"].(string))
		require.NoError(t, err)
		require.True(t, wantTime.Equal(parsed))
	}
	// enforceDroppedBound: representative single-device eviction. Insert 32 newer
	// keys, so the existing (oldest) diagnostic is precisely the row evicted.
	for i := 0; i < 32; i++ {
		when := later.Add(time.Duration(i+1) * time.Second)
		require.NoError(t, tx.Exec(droppedUpsertPrevSQL, when, when, tenant, "device", fmt.Sprintf("key-%02d", i), "UNDECLARED", "new", int64(1), when, when).Error)
	}
	res = tx.Exec(droppedEvictPrevSQL, tenant, "device", tenant, 100000, 32, tenant)
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	var n int64
	require.NoError(t, tx.Raw(`SELECT count(*) FROM "device-state".dropped_measurements WHERE tenant_id = ? AND device_token = ?`, tenant, "device").Row().Scan(&n))
	require.EqualValues(t, 32, n)
	require.NoError(t, tx.Raw(`SELECT count(*) FROM "device-state".dropped_measurements WHERE tenant_id = ? AND device_token = ? AND name = 'text'`, tenant, "device").Row().Scan(&n))
	require.Zero(t, n)
}

// Additional per-event shapes from v1.9.0:backend/services/device-state/model/api.go and ownership.go.
// SNAPSHOT v1.9.0:backend/services/device-state/model/model.go DeviceState; gorm.Model expanded by hand.
type deviceStatePrev struct {
	ID                  uint `gorm:"primarykey"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           gorm.DeletedAt `gorm:"index"`
	TenantId            string         `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:1;index;not null;size:128"`
	DeviceToken         string         `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:2"`
	Incarnation         sql.NullInt64  `gorm:"type:bigint"`
	ExternalId          string         `gorm:"type:text"`
	Source              string         `gorm:"type:text"`
	Active              bool
	LastConnectTime     sql.NullTime
	LastDisconnectTime  sql.NullTime
	LastActivityTime    sql.NullTime
	InactivityAlarmTime sql.NullTime
	PresenceSource      string `gorm:"type:varchar(16);not null;default:INFERRED"`
	SessionId           uint64 `gorm:"not null;default:0"`
	PresenceTime        sql.NullTime
	ScopeAnchors        anchorsPrev `gorm:"type:text[]"`
	ScopeAnchorsAt      sql.NullTime
}

func (deviceStatePrev) TableName() string { return "device-state.device_states" }

// SNAPSHOT v1.9.0:backend/services/device-state/model/model.go LatestMeasurement and
// LatestLocation, and v1.9.0:backend/services/device-state/model/dropped_measurements.go
// DroppedMeasurement. gorm.Model v1.31.2 is expanded; every field/tag is retained.
type latestMeasurementPrev struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
	TenantId     string         `gorm:"uniqueIndex:idx_latest_measurement_tenant_device_name,priority:1;index;not null;size:128"`
	DeviceToken  string         `gorm:"uniqueIndex:idx_latest_measurement_tenant_device_name,priority:2"`
	Name         string         `gorm:"uniqueIndex:idx_latest_measurement_tenant_device_name,priority:3"`
	Value        sql.NullFloat64
	Classifier   *uint
	Unit         *string `gorm:"type:text"`
	DataType     *string `gorm:"type:varchar(32)"`
	OccurredTime time.Time
}

func (latestMeasurementPrev) TableName() string { return "device-state.latest_measurements" }

type latestLocationPrev struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt  `gorm:"index"`
	TenantId     string          `gorm:"uniqueIndex:idx_latest_location_tenant_device,priority:1;index;not null;size:128"`
	DeviceToken  string          `gorm:"uniqueIndex:idx_latest_location_tenant_device,priority:2"`
	Latitude     sql.NullFloat64 `gorm:"type:decimal(10,8)"`
	Longitude    sql.NullFloat64 `gorm:"type:decimal(11,8)"`
	Elevation    sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Accuracy     sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Speed        sql.NullFloat64 `gorm:"type:decimal(12,4)"`
	Heading      sql.NullFloat64 `gorm:"type:decimal(7,4)"`
	OccurredTime time.Time
}

func (latestLocationPrev) TableName() string { return "device-state.latest_locations" }

type droppedMeasurementPrev struct {
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	TenantId    string    `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:1;not null;size:128"`
	DeviceToken string    `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:2;not null;type:varchar(128)"`
	Name        string    `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:3;not null;size:128"`
	Reason      string    `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:4;not null;size:32"`
	LastValue   string    `gorm:"not null;type:varchar(64)"`
	Count       int64     `gorm:"not null;default:0"`
	FirstTime   time.Time `gorm:"not null"`
	LastTime    time.Time `gorm:"not null"`
}

func (droppedMeasurementPrev) TableName() string { return "device-state.dropped_measurements" }

// Frozen v1.9.0:backend/core/rdb/anchors.go Value serialization; no live base/model dependency.
type anchorsPrev []string

func (a anchorsPrev) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range s {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan reads a one-dimensional PostgreSQL text array in its text form. A NULL scans as
// nil. A NULL element is refused: an anchor list has no use for one, and dropping it
// silently would change what the row says about its owners.
func (a *anchorsPrev) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("rdb.Anchors: cannot scan %T", src)
	}
	out, err := parseTextArrayPrev(s)
	if err != nil {
		return err
	}
	*a = out
	return nil
}

// parseTextArrayPrev parses the text form of a one-dimensional text[]: {a,"b c","d\"e"}.
func parseTextArrayPrev(s string) (anchorsPrev, error) {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("rdb.Anchors: %q is not an array literal", s)
	}
	body := s[1 : len(s)-1]
	out := anchorsPrev{}
	if body == "" {
		return out, nil
	}
	for i := 0; i <= len(body); {
		var elem strings.Builder
		quoted := i < len(body) && body[i] == '"'
		if quoted {
			i++
			for ; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' && i+1 < len(body) {
					i++
				}
				elem.WriteByte(body[i])
			}
			if i >= len(body) {
				return nil, fmt.Errorf("rdb.Anchors: unterminated element in %q", s)
			}
			i++ // closing quote
		} else {
			for ; i < len(body) && body[i] != ','; i++ {
				if body[i] == '{' || body[i] == '"' {
					return nil, fmt.Errorf("rdb.Anchors: %q is not a one-dimensional text array", s)
				}
				elem.WriteByte(body[i])
			}
			if elem.String() == "NULL" {
				return nil, fmt.Errorf("rdb.Anchors: %q holds a NULL element", s)
			}
		}
		out = append(out, elem.String())
		if i < len(body) && body[i] != ',' {
			return nil, fmt.Errorf("rdb.Anchors: malformed array %q", s)
		}
		i++ // past the comma, or past the end
	}
	return out, nil
}

func TestPrevReleaseDeviceStatePerEventWriters(t *testing.T) {
	tx := prevReleaseConn(t).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "prev-release-per-event-writer"
	require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(&deviceStatePrev{}).Error)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: true} }
	// v1.9.0:backend/services/device-state/model/api.go MergeDeviceState's first-sight Create and existing-row full Save.
	row := deviceStatePrev{TenantId: tenant, DeviceToken: "device", ExternalId: "external", Source: "source", Active: true,
		Incarnation:     sql.NullInt64{Int64: 17, Valid: true},
		LastConnectTime: stamp(at), LastActivityTime: stamp(at), PresenceSource: "INFERRED"}
	read := func(want string, active bool, when time.Time) {
		t.Helper()
		got := prevStateJSON(t, tx, "device_states", tenant)
		require.Equal(t, want, got["external_id"])
		require.Equal(t, active, got["active"])
		require.Equal(t, float64(17), got["incarnation"])
		parsed, err := time.Parse(time.RFC3339Nano, got["last_activity_time"].(string))
		require.NoError(t, err)
		require.True(t, when.Equal(parsed))
	}
	require.NoError(t, tx.Create(&row).Error)
	read("external", true, at)
	later := at.Add(time.Minute)
	row.ExternalId = "changed"
	row.LastActivityTime = stamp(later)
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	read("changed", true, later)
	// v1.9.0:backend/services/device-state/model/ownership.go ApplyDeviceOwnership's exact update map and 30-second settle.
	ownershipAt := later.Add(30 * time.Second)
	res := tx.Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).
		Where("device_token = ?", "device").Where("scope_anchors_at IS NULL OR scope_anchors_at < ?", ownershipAt).
		Updates(map[string]any{"scope_anchors": anchorsPrev{"customer:owner"}, "scope_anchors_at": sql.NullTime{Time: ownershipAt, Valid: true}})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	read("changed", true, later)
	got := prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, []any{"customer:owner"}, got["scope_anchors"])
	parsed, err := time.Parse(time.RFC3339Nano, got["scope_anchors_at"].(string))
	require.NoError(t, err)
	require.True(t, ownershipAt.Equal(parsed))
	// The same stamped update loses; an empty owner set at a later precedence is
	// stored as '{}', which differs from the NULL a never-stamped row carries.
	res = tx.Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).
		Where("device_token = ?", "device").Where("scope_anchors_at IS NULL OR scope_anchors_at < ?", ownershipAt).
		Updates(map[string]any{"scope_anchors": anchorsPrev{}, "scope_anchors_at": stamp(ownershipAt)})
	require.NoError(t, res.Error)
	require.Zero(t, res.RowsAffected)
	require.Equal(t, got, prevStateJSON(t, tx, "device_states", tenant))
	res = tx.Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).
		Where("device_token = ?", "device").Where("scope_anchors_at IS NULL OR scope_anchors_at < ?", ownershipAt.Add(time.Second)).
		Updates(map[string]any{"scope_anchors": anchorsPrev{}, "scope_anchors_at": stamp(ownershipAt.Add(time.Second))})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	got = prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, []any{}, got["scope_anchors"])
	// v1.9.0:backend/services/device-state/model/api.go SweepInactive, including the exact Postgres inactivitySQL fragment and
	// locked ARRAY subquery. The production system-context sweep has no tenant filter;
	// this replay confines its candidates explicitly to this test's tenant.
	now := later.Add(601 * time.Second)
	candidates := tx.Model(&deviceStatePrev{}).Select("id").Where("tenant_id = ?", tenant).
		Where("active = ? AND presence_source <> ?", true, "ASSERTED").
		Where("last_activity_time IS NOT NULL AND last_activity_time < ?::timestamptz - ?::integer * INTERVAL '1 second'", now, 600).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	res = tx.Model(&deviceStatePrev{}).Where("id = ANY(ARRAY(?))", candidates).Updates(map[string]any{
		"active": false, "last_disconnect_time": sql.NullTime{Time: now, Valid: true}, "inactivity_alarm_time": sql.NullTime{Time: now, Valid: true},
	})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	read("changed", false, later)
	got = prevStateJSON(t, tx, "device_states", tenant)
	for _, column := range []string{"last_disconnect_time", "inactivity_alarm_time"} {
		parsed, err := time.Parse(time.RFC3339Nano, got[column].(string))
		require.NoError(t, err)
		require.True(t, now.Equal(parsed), column)
	}
}

// v1.9.0:backend/services/device-state/model/columnar_writes.go deviceStateColumns,
// deviceStateHeartbeat/heartbeatGuard and deviceStateFolded: inserts stamp an
// incarnation (or NULL), but conflicts never assign it; mismatches require the locked reset.
func TestPrevReleaseDeviceStateIncarnationArrays(t *testing.T) {
	tx := prevReleaseConn(t).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "prev-release-incarnation-array-writer"
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	args := func(inc *int64, when time.Time, external string) []any {
		return []any{tenant, []time.Time{when}, []time.Time{when}, []string{"device"}, []*int64{inc}, []string{external},
			[]string{"source"}, []bool{true}, []*time.Time{&when}, []*time.Time{nil}, []*time.Time{&when},
			[]*time.Time{nil}, []string{"INFERRED"}, []int64{0}, []*time.Time{nil}, []*string{nil}, []*time.Time{nil}}
	}
	old, newer := int64(17), int64(23)
	for _, statement := range []string{deviceStateLockPrevSQL, deviceStateFoldedPrevSQL, deviceStateHeartbeatPrevSQL} {
		for _, inc := range []*int64{nil, &old} {
			require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(&deviceStatePrev{}).Error)
			require.NoError(t, tx.Exec(statement, args(inc, at, "first")...).Error)
			row := prevStateJSON(t, tx, "device_states", tenant)
			if inc == nil {
				require.Nil(t, row["incarnation"])
			} else {
				require.Equal(t, float64(*inc), row["incarnation"])
			}
			res := tx.Exec(statement, args(&newer, at.Add(time.Minute), "replacement")...)
			require.NoError(t, res.Error)
			got := prevStateJSON(t, tx, "device_states", tenant)
			require.Equal(t, row["incarnation"], got["incarnation"])
			if statement == deviceStateLockPrevSQL || (statement == deviceStateHeartbeatPrevSQL && inc != nil) {
				require.Zero(t, res.RowsAffected)
				require.Equal(t, row, got)
			} else {
				require.EqualValues(t, 1, res.RowsAffected)
				require.Equal(t, "replacement", got["external_id"])
			}
		}
	}
}

// v1.9.0:backend/services/device-state/model/projection_merge.go foldIncarnations
// and v1.9.0:backend/services/device-state/model/deletion.go ReconcileDeviceState,
// deleteLiveProjection/deleteProjectionChildren (also ApplyDeviceDeleted and
// ownership.go ApplyDeviceOwnership): every distinct UpdateColumn and hard delete.
func TestPrevReleaseDeviceStateIncarnationResetAndDeletes(t *testing.T) {
	tx := prevReleaseConn(t).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "prev-release-incarnation-delete-writer"
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	children := []any{&latestMeasurementPrev{}, &latestLocationPrev{}, &droppedMeasurementPrev{}}
	for _, child := range append([]any{&deviceStatePrev{}}, children...) {
		require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(child).Error)
	}
	stamp := func(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: true} }
	seedChildren := func(when time.Time) {
		t.Helper()
		require.NoError(t, tx.Exec(latestMeasurementUpsertPrevSQL, tenant, []time.Time{when}, []time.Time{when}, []string{"device"},
			[]string{"temperature"}, []*string{nil}, []*int64{nil}, []*string{nil}, []*string{nil}, []time.Time{when}).Error)
		args := []any{tenant, []time.Time{when}, []time.Time{when}, []string{"device"}}
		for range 6 {
			args = append(args, []*string{nil})
		}
		args = append(args, []time.Time{when})
		require.NoError(t, tx.Exec(latestLocationUpsertPrevSQL, args...).Error)
		require.NoError(t, tx.Exec(droppedUpsertPrevSQL, when, when, tenant, "device", "text", "UNDECLARED", "drop", int64(1), when, when).Error)
		for _, table := range []string{"latest_measurements", "latest_locations", "dropped_measurements"} {
			require.Equal(t, "device", prevStateJSON(t, tx, table, tenant)["device_token"])
		}
	}
	count := func(model any, want int64) {
		t.Helper()
		var n int64
		require.NoError(t, tx.Unscoped().Model(model).Where("tenant_id = ? AND device_token = ?", tenant, "device").Count(&n).Error)
		require.Equal(t, want, n)
	}
	// A legacy Create writes NULL. Reconcile's guarded stamp uses UpdateColumn:
	// it has no updated_at stamp, and a repeat claim cannot overwrite the incarnation.
	row := deviceStatePrev{TenantId: tenant, DeviceToken: "device", CreatedAt: at, UpdatedAt: at,
		ExternalId: "old", Source: "source", Active: true, LastActivityTime: stamp(at), LastConnectTime: stamp(at),
		PresenceSource: "ASSERTED", SessionId: 7, PresenceTime: stamp(at), ScopeAnchors: anchorsPrev{"customer:old"}, ScopeAnchorsAt: stamp(at)}
	require.NoError(t, tx.Create(&row).Error)
	var locked deviceStatePrev
	require.NoError(t, tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND device_token = ?", tenant, "device").First(&locked).Error)
	require.Equal(t, row.ID, locked.ID)
	before := prevStateJSON(t, tx, "device_states", tenant)
	require.Nil(t, before["incarnation"])
	// v1.9.0:backend/services/device-state/model/deletion.go ReconcileDeviceState.
	res := tx.Unscoped().Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).
		Where("id = ? AND incarnation IS NULL", row.ID).UpdateColumn("incarnation", int64(17))
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	got := prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, float64(17), got["incarnation"])
	require.Equal(t, before["updated_at"], got["updated_at"])
	res = tx.Unscoped().Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).
		Where("id = ? AND incarnation IS NULL", row.ID).UpdateColumn("incarnation", int64(23))
	require.NoError(t, res.Error)
	require.Zero(t, res.RowsAffected)
	require.Equal(t, got, prevStateJSON(t, tx, "device_states", tenant))
	seedChildren(at)
	// v1.9.0:backend/services/device-state/model/projection_merge.go foldIncarnations:
	// delete all children under the held parent lock, then explicitly reset its stamp.
	for _, child := range children {
		res = tx.Unscoped().Where("tenant_id = ?", tenant).Where("device_token = ?", "device").Delete(child)
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		count(child, 0)
	}
	res = tx.Model(&deviceStatePrev{}).Where("tenant_id = ?", tenant).Where("device_token = ?", "device").
		UpdateColumn("incarnation", sql.NullInt64{Int64: 23, Valid: true})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	got = prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, float64(23), got["incarnation"])
	require.Equal(t, before["updated_at"], got["updated_at"])
	// MergeDeviceState's full Save after reset: keep Model/tenant, clear the previous
	// device's nullable state and ownership, and apply the replacement's first event.
	row = deviceStatePrev{ID: row.ID, CreatedAt: row.CreatedAt, TenantId: tenant, DeviceToken: "device",
		Incarnation: sql.NullInt64{Int64: 23, Valid: true}, PresenceSource: "INFERRED", Active: true,
		LastActivityTime: stamp(at.Add(-time.Hour)), LastConnectTime: stamp(at.Add(-time.Hour))}
	require.NoError(t, tx.Where("tenant_id = ?", tenant).Save(&row).Error)
	got = prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, float64(23), got["incarnation"])
	require.Equal(t, "", got["external_id"])
	require.Equal(t, "", got["source"])
	require.Equal(t, "INFERRED", got["presence_source"])
	require.Equal(t, float64(0), got["session_id"])
	for _, column := range []string{"last_disconnect_time", "inactivity_alarm_time", "presence_time", "scope_anchors", "scope_anchors_at"} {
		require.Nil(t, got[column], column)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got["last_activity_time"].(string))
	require.NoError(t, err)
	require.True(t, at.Add(-time.Hour).Equal(parsed))
	seedChildren(at)
	// v1.9.0:backend/services/device-state/model/deletion.go deleteLiveProjection:
	// parent-present branch deletes parent first, then all three children unconditionally.
	for _, model := range append([]any{&deviceStatePrev{}}, children...) {
		res = tx.Unscoped().Where("tenant_id = ?", tenant).Where("device_token = ?", "device").Delete(model)
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		count(model, 0)
	}
	// No parent: both timestamps must be <= cutoff. A freshly updated child and a
	// freshly created child survive; Unscoped also deletes soft-deleted old children.
	seedChildren(at)
	for i, child := range children {
		if i < 2 {
			// Setup: both gorm.Model children can carry a soft-delete marker.
			require.NoError(t, tx.Unscoped().Model(child).Where("tenant_id = ? AND device_token = ?", tenant, "device").
				UpdateColumn("deleted_at", at).Error)
		}
		for _, times := range [][2]time.Time{{at, at.Add(time.Hour)}, {at.Add(time.Hour), at}, {at, at}} {
			// Setup only; the following Delete is the frozen producer shape.
			require.NoError(t, tx.Unscoped().Model(child).Where("tenant_id = ? AND device_token = ?", tenant, "device").
				UpdateColumns(map[string]any{"created_at": times[0], "updated_at": times[1]}).Error)
			res = tx.Unscoped().Where("tenant_id = ?", tenant).Where("device_token = ?", "device").
				Where("created_at <= ? AND updated_at <= ?", at, at).Delete(child)
			require.NoError(t, res.Error)
			if times[0].Equal(at) && times[1].Equal(at) {
				require.EqualValues(t, 1, res.RowsAffected)
				count(child, 0)
			} else {
				require.Zero(t, res.RowsAffected)
				count(child, 1)
			}
		}
	}
}
