// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The text column, on a REAL PostgreSQL through the real migration chain: it exists, is
// nullable text with no default (so a pod from before it writes the rows it always wrote), the
// migration replays cleanly, and a STRING reading round-trips through the batch writer.
func TestLatestMeasurementTextValueOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "textvalue")
	ctx := core.WithTenant(context.Background(), "acme")

	var col struct {
		DataType   string
		IsNullable string
		Default    *string
	}
	require.NoError(t, api.RDB.Database.Raw(`SELECT data_type, is_nullable, column_default AS "default"
		FROM information_schema.columns
		WHERE table_schema = 'device-state' AND table_name = 'latest_measurements' AND column_name = 'text_value'`).
		Scan(&col).Error)
	require.Equal(t, "text", col.DataType)
	require.Equal(t, "YES", col.IsNullable)
	require.Nil(t, col.Default, "no default: nothing to lose on a restore, nothing for an old pod to disagree with")

	// Re-runnable: replaying the migration against a database that has the column changes nothing.
	mig := NewLatestMeasurementTextValueSchema()
	require.NoError(t, mig.Migrate(api.RDB.Database))
	require.NoError(t, mig.Migrate(api.RDB.Database))

	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	dt := str("STRING")
	u := data("acme", "d1", t0)
	u.Measurements = []LatestMeasurementInput{
		{Name: "mode", TextValue: str("heating"), DataType: dt, OccurredTime: t0},
		{Name: "temp", Value: num(21.5), OccurredTime: t0},
	}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{u}))
	mode, temp := mustReading(t, api, ctx, "d1", "mode"), mustReading(t, api, ctx, "d1", "temp")
	require.Equal(t, "heating", *mode.TextValue)
	require.False(t, mode.Value.Valid)
	require.Nil(t, temp.TextValue)
	require.Equal(t, 21.5, temp.Value.Float64)

	// A newer number under the text's name replaces the pair.
	u2 := data("acme", "d1", t0.Add(time.Second))
	u2.Measurements = []LatestMeasurementInput{{Name: "mode", Value: num(2), OccurredTime: t0.Add(time.Second)}}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{u2}))
	mode = mustReading(t, api, ctx, "d1", "mode")
	require.Nil(t, mode.TextValue)
	require.Equal(t, 2.0, mode.Value.Float64)
}

// A pod from before the column writes with the upsert it always had, naming no text_value, and
// the row it writes reads NULL there. The statement is v1.8.0's latestMeasurementUpsert, frozen
// (the one backend/tools/migrationdiff replays against every area's migrated schema).
func TestAnOldPodsLatestMeasurementUpsertStillWritesOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "textoldpod")
	ctx := core.WithTenant(context.Background(), "acme")
	const prev = `INSERT INTO "device-state"."latest_measurements" AS t ("tenant_id","created_at","updated_at","device_token","name","value","classifier","unit","data_type","occurred_time") ` +
		`SELECT $1::text, unnest($2::timestamptz[]), unnest($3::timestamptz[]), unnest($4::text[]), unnest($5::text[]), unnest($6::text[]::numeric[]), unnest($7::int8[]), unnest($8::text[]), unnest($9::text[]), unnest($10::timestamptz[]) ` +
		`ON CONFLICT ("tenant_id","device_token","name") DO UPDATE SET "value" = excluded.value, "classifier" = excluded.classifier, "unit" = excluded.unit, "data_type" = excluded.data_type, "occurred_time" = excluded.occurred_time, "updated_at" = excluded.updated_at ` +
		`WHERE (t.occurred_time < excluded.occurred_time) AND t."tenant_id" = $1::text`
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	write := func(at time.Time, v string) {
		t.Helper()
		ts := "{" + at.Format(time.RFC3339Nano) + "}"
		require.NoError(t, api.RDB.Database.Transaction(func(tx *gorm.DB) error {
			return tx.Exec(prev, "acme", ts, ts, "{d1}", "{temp}", "{"+v+"}", "{NULL}", "{NULL}", "{NULL}", ts).Error
		}))
	}
	write(t0, "1")
	write(t0.Add(time.Second), "2")
	got := mustReading(t, api, ctx, "d1", "temp")
	require.Equal(t, 2.0, got.Value.Float64)
	require.Nil(t, got.TextValue, "a row an old pod wrote has no text")
}

// Why resolvedevent.StorableStringValue exists: PostgreSQL refuses a NUL byte for the WHOLE statement, so one such
// reading would fail every other reading merged in its batch.
func TestPostgresRefusesANulByteInATextReading(t *testing.T) {
	api := newPostgresProjectionApi(t, "textnul")
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	u := data("acme", "d1", t0)
	u.Measurements = []LatestMeasurementInput{{Name: "mode", TextValue: str("a\x00b"), OccurredTime: t0}}
	require.Error(t, api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{u}))
}
