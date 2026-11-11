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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SNAPSHOT of model/model_state.go:NotificationState at v1.3.0. The embedded
// gorm.Model (gorm v1.31.2) and rdb.TenantScoped are expanded by hand. Never
// replace this with a live service model or a live embedded base.
type notificationStateV130 struct {
	ID              uint `gorm:"primarykey"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
	TenantId        string         `gorm:"index;not null;size:128"`
	AlarmToken      string         `gorm:"not null;size:128"`
	AlarmKey        string         `gorm:"not null;size:256"`
	Severity        string         `gorm:"size:16"`
	FirstNotifiedAt sql.NullTime
	LastNotifiedAt  sql.NullTime
	NotifyCount     int
	AcknowledgedAt  sql.NullTime
	ClearedAt       sql.NullTime
	EscalationLevel int
	LastEscalatedAt sql.NullTime
	CycleRaisedAt   sql.NullTime
}

func (notificationStateV130) TableName() string { return "notification-management.notification_states" }

func TestPrevReleaseNotificationStateWriters(t *testing.T) {
	db := prevReleaseConn(t)
	// Roll back all replay rows so reruns (including -count) start cleanly.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "v130-notification-writer"
	require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(&notificationStateV130{}).Error)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: true} }
	read := func(want *notificationStateV130) {
		t.Helper()
		var got notificationStateV130
		require.NoError(t, tx.Where("tenant_id = ? AND alarm_token = ?", tenant, want.AlarmToken).First(&got).Error)
		// Time.Equal ignores the driver's location representation; compare all other fields exactly.
		require.WithinDuration(t, want.CreatedAt, got.CreatedAt, time.Microsecond)
		require.WithinDuration(t, want.UpdatedAt, got.UpdatedAt, time.Microsecond)
		got.CreatedAt, got.UpdatedAt = want.CreatedAt, want.UpdatedAt
		for _, pair := range [][2]*sql.NullTime{
			{&got.FirstNotifiedAt, &want.FirstNotifiedAt}, {&got.LastNotifiedAt, &want.LastNotifiedAt},
			{&got.AcknowledgedAt, &want.AcknowledgedAt}, {&got.ClearedAt, &want.ClearedAt},
			{&got.LastEscalatedAt, &want.LastEscalatedAt}, {&got.CycleRaisedAt, &want.CycleRaisedAt},
		} {
			require.Equal(t, pair[1].Valid, pair[0].Valid)
			if pair[1].Valid {
				require.True(t, pair[1].Time.Equal(pair[0].Time))
				pair[0].Time = pair[1].Time
			}
		}
		require.Equal(t, *want, got)
		var allNull bool
		require.NoError(t, tx.Raw(`SELECT clear_notified_at IS NULL AND clear_notified_cycle IS NULL
   AND paged_targets IS NULL AND paged_cycle IS NULL AND originator_type IS NULL
   AND originator_id IS NULL AND device_token IS NULL AND device_name IS NULL
   AND device_type_token IS NULL AND source_at IS NULL
   FROM "notification-management".notification_states WHERE id = ?`, got.ID).Row().Scan(&allNull))
		require.True(t, allNull, "v1.4 additions must remain NULL on a v1.3-written row")
	}
	// api_state.go:RecordNotification, new-state Create.
	row := notificationStateV130{TenantId: tenant, AlarmToken: "raised", AlarmKey: "temperature", Severity: "MAJOR",
		FirstNotifiedAt: stamp(at), LastNotifiedAt: stamp(at), NotifyCount: 1, CycleRaisedAt: stamp(at)}
	require.NoError(t, tx.Create(&row).Error)
	read(&row)
	// RecordNotification's existing-row Save (including the later-cycle reset shape).
	row.NotifyCount++
	row.LastNotifiedAt = stamp(at.Add(time.Minute))
	row.Severity = "CRITICAL"
	require.NoError(t, tx.Save(&row).Error)
	read(&row)
	// TouchSeverity uses Update, not UpdateColumn: gorm also stamps updated_at.
	res := tx.Model(&notificationStateV130{}).Where("tenant_id = ? AND alarm_token = ?", tenant, row.AlarmToken).Update("severity", "MINOR")
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	row.Severity = "MINOR"
	require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
	read(&row)
	// ClaimEscalation's exact map, value kinds and both cycle predicates.
	for _, knownCycle := range []bool{true, false} {
		if !knownCycle {
			row.CycleRaisedAt = sql.NullTime{}
			require.NoError(t, tx.Save(&row).Error)
			read(&row)
		}
		q := tx.Model(&notificationStateV130{}).Where("tenant_id = ?", tenant).
			Where("alarm_token = ? AND escalation_level = ? AND acknowledged_at IS NULL AND cleared_at IS NULL", row.AlarmToken, row.EscalationLevel)
		if knownCycle {
			q = q.Where("cycle_raised_at = ?", row.CycleRaisedAt.Time)
		} else {
			q = q.Where("cycle_raised_at IS NULL")
		}
		at = at.Add(time.Minute)
		res = q.Updates(map[string]any{
			"escalation_level":  gorm.Expr("escalation_level + 1"),
			"last_escalated_at": at,
			"last_notified_at":  at,
			"notify_count":      gorm.Expr("notify_count + 1"),
		})
		require.NoError(t, res.Error)
		require.EqualValues(t, 1, res.RowsAffected)
		row.EscalationLevel++
		row.NotifyCount++
		row.LastEscalatedAt = stamp(at)
		row.LastNotifiedAt = stamp(at)
		require.NoError(t, tx.Raw(`SELECT updated_at FROM "notification-management".notification_states WHERE id = ?`, row.ID).Row().Scan(&row.UpdatedAt))
		read(&row)
	}
	// markTerminal (MarkAcknowledged and MarkCleared) uses full-row Save in both
	// the stamp branch and the cycle-adoption-only branch; there are no UpdateColumns maps.
	row.AcknowledgedAt = stamp(at)
	require.NoError(t, tx.Save(&row).Error)
	read(&row)
	row.CycleRaisedAt = stamp(at)
	require.NoError(t, tx.Save(&row).Error)
	read(&row)
	row.ClearedAt = stamp(at)
	require.NoError(t, tx.Save(&row).Error)
	read(&row)
	// RecordNotification/enterCycle: the newer cycle resets per-cycle fields to NULL/zero.
	row.CycleRaisedAt = stamp(at.Add(time.Hour))
	row.FirstNotifiedAt = stamp(at.Add(time.Hour))
	row.LastNotifiedAt = row.FirstNotifiedAt
	row.NotifyCount = 1
	row.EscalationLevel = 0
	row.AcknowledgedAt = sql.NullTime{}
	row.ClearedAt = sql.NullTime{}
	row.LastEscalatedAt = sql.NullTime{}
	require.NoError(t, tx.Save(&row).Error)
	read(&row)
	// markTerminal's tombstone Create, once for each terminal column.
	for _, column := range []string{"acknowledged_at", "cleared_at"} {
		tombstone := notificationStateV130{TenantId: tenant, AlarmToken: column, AlarmKey: "temperature", Severity: "MAJOR", CycleRaisedAt: stamp(at)}
		if column == "cleared_at" {
			tombstone.ClearedAt = stamp(at)
		} else {
			tombstone.AcknowledgedAt = stamp(at)
		}
		require.NoError(t, tx.Create(&tombstone).Error)
		read(&tombstone)
	}
	// PruneResolvedStates: exact hard-delete predicate, with explicit tenant scope.
	res = tx.Unscoped().Where("tenant_id = ?", tenant).
		Where("(cleared_at IS NOT NULL OR acknowledged_at IS NOT NULL)").
		Where("(cleared_at IS NULL OR cleared_at < ?)", at.Add(time.Second)).
		Where("(acknowledged_at IS NULL OR acknowledged_at < ?)", at.Add(time.Second)).Delete(&notificationStateV130{})
	require.NoError(t, res.Error)
	require.EqualValues(t, 2, res.RowsAffected)
	var remaining []notificationStateV130
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

// Frozen builder output: v1.3.0 core/rdb/insert_columns.go:buildPlan and
// insert_columns_upsert.go:upsertAssignments, with the column lists/arms from
// device-state/model/columnar_writes.go. These writers have no literal SQL at
// the call site: this is the exact generated Postgres text for one-row arrays.
// The production RdbManager table prefix qualifies the schema (not search_path alone).
// v1.3.0 model/projection_merge.go:lockOrCreate (via deviceStateLock).
const deviceStateLockV130SQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]), unnest($7::bool[]), unnest($8::timestamptz[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::text[]), unnest($13::int8[]), unnest($14::timestamptz[]), unnest($15::text[])::text[], unnest($16::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "updated_at" = t.updated_at WHERE (1 = 0) AND t."tenant_id" = $1::text`

// v1.3.0 model/projection_merge.go:writeFoldedStates (via deviceStateFolded).
const deviceStateFoldedV130SQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]), unnest($7::bool[]), unnest($8::timestamptz[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::text[]), unnest($13::int8[]), unnest($14::timestamptz[]), unnest($15::text[])::text[], unnest($16::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "external_id" = excluded.external_id, "source" = excluded.source, "active" = excluded.active, "last_connect_time" = excluded.last_connect_time, "last_disconnect_time" = excluded.last_disconnect_time, "last_activity_time" = excluded.last_activity_time, "inactivity_alarm_time" = excluded.inactivity_alarm_time, "presence_source" = excluded.presence_source, "session_id" = excluded.session_id, "presence_time" = excluded.presence_time, "scope_anchors" = excluded.scope_anchors, "scope_anchors_at" = excluded.scope_anchors_at, "updated_at" = excluded.updated_at WHERE t."tenant_id" = $1::text`

// v1.3.0 model/projection_merge.go:mergeHeartbeats (via deviceStateHeartbeat).
const deviceStateHeartbeatV130SQL = `INSERT INTO "device-state"."device_states" AS t ("tenant_id","created_at","updated_at","device_token","external_id","source","active","last_connect_time","last_disconnect_time","last_activity_time","inactivity_alarm_time","presence_source","session_id","presence_time","scope_anchors","scope_anchors_at") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]), unnest($7::bool[]), unnest($8::timestamptz[]), unnest($9::timestamptz[]), unnest($10::timestamptz[]), unnest($11::timestamptz[]), unnest($12::text[]), unnest($13::int8[]), unnest($14::timestamptz[]), unnest($15::text[])::text[], unnest($16::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "external_id" = CASE WHEN excluded.external_id <> '' THEN excluded.external_id ELSE t.external_id END, "source" = CASE WHEN excluded.source <> '' THEN excluded.source ELSE t.source END, "last_activity_time" = CASE WHEN t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time THEN excluded.last_activity_time ELSE t.last_activity_time END, "scope_anchors" = CASE WHEN excluded.scope_anchors_at IS NOT NULL AND (t.scope_anchors_at IS NULL OR excluded.scope_anchors_at > t.scope_anchors_at) THEN excluded.scope_anchors ELSE t.scope_anchors END, "scope_anchors_at" = CASE WHEN excluded.scope_anchors_at IS NOT NULL AND (t.scope_anchors_at IS NULL OR excluded.scope_anchors_at > t.scope_anchors_at) THEN excluded.scope_anchors_at ELSE t.scope_anchors_at END, "updated_at" = excluded.updated_at WHERE (NOT (t.presence_source <> 'ASSERTED' AND NOT t.active AND (t.last_activity_time IS NULL OR excluded.last_activity_time >= t.last_activity_time))) AND t."tenant_id" = $1::text`

// v1.3.0 model/projection_merge.go:upsertLatestMeasurements / MergeLatestMeasurements (via latestMeasurementUpsert).
const latestMeasurementUpsertV130SQL = `INSERT INTO "device-state"."latest_measurements" AS t ("tenant_id","created_at","updated_at","device_token","name","value","classifier","unit","data_type","occurred_time") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]::numeric[]), unnest($7::int8[]), unnest($8::text[]), unnest($9::text[]), unnest($10::timestamptz[]) ON CONFLICT ("tenant_id","device_token","name") DO UPDATE SET "value" = excluded.value, "classifier" = excluded.classifier, "unit" = excluded.unit, "data_type" = excluded.data_type, "occurred_time" = excluded.occurred_time, "updated_at" = excluded.updated_at WHERE (t.occurred_time < excluded.occurred_time) AND t."tenant_id" = $1::text`

// v1.3.0 model/projection_merge.go:upsertLatestLocations / MergeLatestLocations (via latestLocationUpsert).
const latestLocationUpsertV130SQL = `INSERT INTO "device-state"."latest_locations" AS t ("tenant_id","created_at","updated_at","device_token","latitude","longitude","elevation","accuracy","speed","heading","occurred_time") SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]::numeric[]), unnest($6::text[]::numeric[]), unnest($7::text[]::numeric[]), unnest($8::text[]::numeric[]), unnest($9::text[]::numeric[]), unnest($10::text[]::numeric[]), unnest($11::timestamptz[]) ON CONFLICT ("tenant_id","device_token") DO UPDATE SET "latitude" = excluded.latitude, "longitude" = excluded.longitude, "elevation" = excluded.elevation, "accuracy" = excluded.accuracy, "speed" = excluded.speed, "heading" = excluded.heading, "occurred_time" = excluded.occurred_time, "updated_at" = excluded.updated_at WHERE (t.occurred_time < excluded.occurred_time) AND t."tenant_id" = $1::text`

// v1.3.0 model/dropped_measurements.go:upsertDropped/droppedSet. Frozen gorm
// CreateInBatches output for one row (ID omitted, RETURNING id); the only dynamic
// part is the repeated VALUES group. Explicit tenant_id replaces the core callback.
const droppedUpsertV130SQL = `INSERT INTO "device-state"."dropped_measurements" ("created_at","updated_at","tenant_id","device_token","name","reason","last_value","count","first_time","last_time") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT ("tenant_id","device_token","name","reason") DO UPDATE SET "count"=dropped_measurements.count + excluded.count,"first_time"=CASE WHEN excluded.first_time < dropped_measurements.first_time THEN excluded.first_time ELSE dropped_measurements.first_time END,"last_value"=CASE WHEN excluded.last_time > dropped_measurements.last_time THEN excluded.last_value ELSE dropped_measurements.last_value END,"last_time"=CASE WHEN excluded.last_time > dropped_measurements.last_time THEN excluded.last_time ELSE dropped_measurements.last_time END,"updated_at"="excluded"."updated_at" RETURNING "id"`

// v1.3.0 model/dropped_measurements.go:enforceDroppedBound. Representative gorm
// subquery output, including the explicit inner tenant and callback's outer tenant.
// MaxDroppedMeasurementsPerDevice=32; evictScan=100000. No soft-delete field on this table.
const droppedEvictV130SQL = `DELETE FROM "device-state"."dropped_measurements" WHERE id IN (SELECT "id" FROM "device-state"."dropped_measurements" WHERE tenant_id = $1 AND device_token = $2 ORDER BY last_time DESC, id DESC LIMIT $3 OFFSET $4) AND "dropped_measurements"."tenant_id" = $5`

func TestPrevReleaseDeviceStateWriters(t *testing.T) {
	db := prevReleaseConn(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "v130-device-writer"
	for _, table := range []string{"device_states", "latest_measurements", "latest_locations", "dropped_measurements"} {
		require.NoError(t, tx.Exec(fmt.Sprintf(`DELETE FROM "device-state".%s WHERE tenant_id = ?`, table), tenant).Error)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later := at.Add(time.Minute)
	// The column arrays have one element each, as the v1.3 per-device path binds them.
	// Numeric values travel as decimal strings, and anchors as PostgreSQL array literals.
	stateArgs := func(at time.Time, external string) []any {
		return []any{tenant, []time.Time{at}, []time.Time{at}, []string{"device"}, []string{external},
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
		for _, c := range []string{"last_disconnect_time", "inactivity_alarm_time", "presence_time", "scope_anchors", "scope_anchors_at", "deleted_at"} {
			require.Nil(t, row[c], c)
		}
		for _, c := range []string{"last_connect_time", "last_activity_time"} {
			parsed, err := time.Parse(time.RFC3339Nano, row[c].(string))
			require.NoError(t, err)
			require.True(t, at.Equal(parsed), c)
		}
		// Already obsolete in v1.3's model; its migrated default still accepts omitted values.
		require.Equal(t, float64(600), row["inactivity_timeout"])
	}
	// lockOrCreate: insert and then the deliberately non-writing conflict/row-lock arm.
	require.NoError(t, tx.Exec(deviceStateLockV130SQL, stateArgs(at, "first")...).Error)
	assertState("first", at)
	res := tx.Exec(deviceStateLockV130SQL, stateArgs(later, "ignored")...)
	require.NoError(t, res.Error)
	require.Zero(t, res.RowsAffected)
	assertState("first", at)
	// writeFoldedStates: insert, then overwrite through the conflict arm.
	require.NoError(t, tx.Exec(`DELETE FROM "device-state".device_states WHERE tenant_id = ?`, tenant).Error)
	require.NoError(t, tx.Exec(deviceStateFoldedV130SQL, stateArgs(at, "folded")...).Error)
	assertState("folded", at)
	require.NoError(t, tx.Exec(deviceStateFoldedV130SQL, stateArgs(later, "folded-again")...).Error)
	assertState("folded-again", later)
	// mergeHeartbeats: first sight and the monotone data-event merge, then a stale replay.
	require.NoError(t, tx.Exec(`DELETE FROM "device-state".device_states WHERE tenant_id = ?`, tenant).Error)
	require.NoError(t, tx.Exec(deviceStateHeartbeatV130SQL, stateArgs(at, "heartbeat")...).Error)
	assertState("heartbeat", at)
	args := stateArgs(later, "heartbeat-again")
	require.NoError(t, tx.Exec(deviceStateHeartbeatV130SQL, args...).Error)
	// Heartbeats advance activity, but retain the original connect time.
	row := prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, "heartbeat-again", row["external_id"])
	parsed, err := time.Parse(time.RFC3339Nano, row["last_activity_time"].(string))
	require.NoError(t, err)
	require.True(t, later.Equal(parsed))
	parsed, err = time.Parse(time.RFC3339Nano, row["last_connect_time"].(string))
	require.NoError(t, err)
	require.True(t, at.Equal(parsed))
	require.NoError(t, tx.Exec(deviceStateHeartbeatV130SQL, stateArgs(at, "")...).Error)
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
		require.NoError(t, tx.Exec(latestMeasurementUpsertV130SQL, args...).Error)
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
		require.NoError(t, tx.Exec(latestLocationUpsertV130SQL, args...).Error)
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
	// upsertDropped: insert then the additive-count/earliest-first/latest-value merge.
	for i, when := range []time.Time{at, later, at} {
		value := fmt.Sprintf("drop-%d", i)
		require.NoError(t, tx.Exec(droppedUpsertV130SQL, when, when, tenant, "device", "text", "UNDECLARED", value, int64(1), when, when).Error)
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
		require.NoError(t, tx.Exec(droppedUpsertV130SQL, when, when, tenant, "device", fmt.Sprintf("key-%02d", i), "UNDECLARED", "new", int64(1), when, when).Error)
	}
	res = tx.Exec(droppedEvictV130SQL, tenant, "device", 100000, 32, tenant)
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	var n int64
	require.NoError(t, tx.Raw(`SELECT count(*) FROM "device-state".dropped_measurements WHERE tenant_id = ? AND device_token = ?`, tenant, "device").Row().Scan(&n))
	require.EqualValues(t, 32, n)
	require.NoError(t, tx.Raw(`SELECT count(*) FROM "device-state".dropped_measurements WHERE tenant_id = ? AND device_token = ? AND name = 'text'`, tenant, "device").Row().Scan(&n))
	require.Zero(t, n)
}

// Additional per-event shapes from v1.3.0 model/api.go and ownership.go.
// SNAPSHOT of model/model.go:DeviceState; gorm.Model expanded by hand.
type deviceStateV130 struct {
	ID                  uint `gorm:"primarykey"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           gorm.DeletedAt `gorm:"index"`
	TenantId            string         `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:1;index;not null;size:128"`
	DeviceToken         string         `gorm:"uniqueIndex:idx_device_state_tenant_token,priority:2"`
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
	ScopeAnchors        anchorsV130 `gorm:"type:text[]"`
	ScopeAnchorsAt      sql.NullTime
}

func (deviceStateV130) TableName() string { return "device-state.device_states" }

// Frozen v1.3.0 core/rdb/anchors.go serialization; no live base/model dependency.
type anchorsV130 []string

func (a anchorsV130) Value() (driver.Value, error) {
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

func TestPrevReleaseDeviceStatePerEventWriters(t *testing.T) {
	tx := prevReleaseConn(t).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	const tenant = "v130-per-event-writer"
	require.NoError(t, tx.Unscoped().Where("tenant_id = ?", tenant).Delete(&deviceStateV130{}).Error)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) sql.NullTime { return sql.NullTime{Time: at, Valid: true} }
	// api.go:MergeDeviceState's first-sight Create and existing-row full Save.
	row := deviceStateV130{TenantId: tenant, DeviceToken: "device", ExternalId: "external", Source: "source", Active: true,
		LastConnectTime: stamp(at), LastActivityTime: stamp(at), PresenceSource: "INFERRED"}
	read := func(want string, active bool, when time.Time) {
		t.Helper()
		got := prevStateJSON(t, tx, "device_states", tenant)
		require.Equal(t, want, got["external_id"])
		require.Equal(t, active, got["active"])
		parsed, err := time.Parse(time.RFC3339Nano, got["last_activity_time"].(string))
		require.NoError(t, err)
		require.True(t, when.Equal(parsed))
	}
	require.NoError(t, tx.Create(&row).Error)
	read("external", true, at)
	later := at.Add(time.Minute)
	row.ExternalId = "changed"
	row.LastActivityTime = stamp(later)
	require.NoError(t, tx.Save(&row).Error)
	read("changed", true, later)
	// ownership.go:ApplyDeviceOwnership's exact update map and 30-second settle.
	ownershipAt := later.Add(30 * time.Second)
	res := tx.Model(&deviceStateV130{}).Where("tenant_id = ?", tenant).
		Where("device_token = ?", "device").Where("scope_anchors_at IS NULL OR scope_anchors_at < ?", ownershipAt).
		Updates(map[string]any{"scope_anchors": anchorsV130{"customer:owner"}, "scope_anchors_at": sql.NullTime{Time: ownershipAt, Valid: true}})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
	read("changed", true, later)
	got := prevStateJSON(t, tx, "device_states", tenant)
	require.Equal(t, []any{"customer:owner"}, got["scope_anchors"])
	parsed, err := time.Parse(time.RFC3339Nano, got["scope_anchors_at"].(string))
	require.NoError(t, err)
	require.True(t, ownershipAt.Equal(parsed))
	// api.go:SweepInactive, including the exact Postgres inactivitySQL fragment and
	// locked ARRAY subquery. The production system-context sweep has no tenant filter;
	// this replay confines its candidates explicitly to this test's tenant.
	now := later.Add(601 * time.Second)
	candidates := tx.Model(&deviceStateV130{}).Select("id").Where("tenant_id = ?", tenant).
		Where("active = ? AND presence_source <> ?", true, "ASSERTED").
		Where("last_activity_time IS NOT NULL AND last_activity_time < ?::timestamptz - ?::integer * INTERVAL '1 second'", now, 600).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	res = tx.Model(&deviceStateV130{}).Where("id = ANY(ARRAY(?))", candidates).Updates(map[string]any{
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
