// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

// The latest-value rule, by VALUE, with the row PRESENT before each call: a test against an
// absent row cannot tell "not overwritten" from "never written".

func mustReading(t *testing.T, api *Api, ctx context.Context, token, name string) LatestMeasurement {
	t.Helper()
	var m LatestMeasurement
	if err := api.RDB.DB(ctx).Where("device_token = ? AND name = ?", token, name).First(&m).Error; err != nil {
		t.Fatalf("load %s/%s: %v", token, name, err)
	}
	return m
}

func num(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }

func TestALatestReadingIsReplacedOnlyByAStrictlyNewerOne(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		incoming  time.Time
		wantValue float64
	}{
		{"newer replaces it", t0.Add(time.Second), 2},
		{"older does not", t0.Add(-time.Second), 1},
		{"equal does not", t0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "db")
			ctx := core.WithTenant(context.Background(), "acme")
			if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{{Name: "temp", Value: num(1), OccurredTime: t0}}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if got := mustReading(t, api, ctx, "d1", "temp"); got.Value.Float64 != 1 {
				t.Fatalf("seeded value = %v; want 1", got.Value.Float64)
			}
			if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{{Name: "temp", Value: num(2), OccurredTime: tc.incoming}}); err != nil {
				t.Fatalf("merge: %v", err)
			}
			if got := mustReading(t, api, ctx, "d1", "temp"); got.Value.Float64 != tc.wantValue {
				t.Errorf("value = %v; want %v", got.Value.Float64, tc.wantValue)
			}
		})
	}
}

func TestALatestPositionIsReplacedOnlyByAStrictlyNewerFix(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		incoming time.Time
		wantLat  float64
	}{
		{"newer replaces it", t0.Add(time.Second), 29},
		{"older does not", t0.Add(-time.Second), 28},
		{"equal does not", t0, 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "db")
			ctx := core.WithTenant(context.Background(), "acme")
			if err := api.MergeLatestLocations(ctx, "d1", []LatestLocationInput{{Latitude: num(28), Longitude: num(-81), OccurredTime: t0}}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := api.MergeLatestLocations(ctx, "d1", []LatestLocationInput{{Latitude: num(29), Longitude: num(-82), Speed: num(3), OccurredTime: tc.incoming}}); err != nil {
				t.Fatalf("merge: %v", err)
			}
			got := loadLocation(t, api, ctx, "d1")
			if got.Latitude.Float64 != tc.wantLat {
				t.Errorf("latitude = %v; want %v", got.Latitude.Float64, tc.wantLat)
			}
			// A fix is replaced whole or not at all: the kept fix reported no speed.
			if tc.wantLat == 28 && got.Speed.Valid {
				t.Errorf("speed = %v; the kept fix reported none", got.Speed.Float64)
			}
		})
	}
}

// SQLite stores these times as text and compares them as text, which orders two times
// correctly only when both are written in one zone. A reading stamped 21:30 at UTC+10 is
// 11:30Z — OLDER than a stored 12:00Z — and must not replace it, though its text sorts after.
func TestAnOlderReadingInAnotherZoneDoesNotReplaceANewerOne(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 9, 20, 21, 30, 0, 0, time.FixedZone("UTC+10", 10*3600))
	if !older.Before(t0) {
		t.Fatalf("fixture: %v is not before %v", older, t0)
	}
	if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{{Name: "temp", Value: num(1), OccurredTime: t0}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := api.MergeLatestMeasurements(ctx, "d1", []LatestMeasurementInput{{Name: "temp", Value: num(2), OccurredTime: older}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := mustReading(t, api, ctx, "d1", "temp"); got.Value.Float64 != 1 {
		t.Errorf("value = %v; an older reading in another zone replaced the newer one", got.Value.Float64)
	}
}

// Within one event, one name is one row: the latest reading, the first to arrive among
// equal times.
func TestCoalescingKeepsTheLatestAndTheFirstAmongEquals(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	rows := coalesceMeasurements("d1", []LatestMeasurementInput{
		{Name: "temp", Value: num(1), OccurredTime: t0},
		{Name: "hum", Value: num(10), OccurredTime: t0},
		{Name: "temp", Value: num(2), OccurredTime: t0.Add(time.Second)},
		{Name: "temp", Value: num(3), OccurredTime: t0.Add(time.Second)},
		{Name: "hum", Value: num(11), OccurredTime: t0.Add(-time.Second)},
		// Inside the same microsecond as the kept temp reading: a tie, so it loses.
		{Name: "temp", Value: num(4), OccurredTime: t0.Add(time.Second + 400*time.Nanosecond)},
	})
	if len(rows) != 2 || rows[0].Name != "hum" || rows[1].Name != "temp" {
		t.Fatalf("rows = %+v; want hum then temp", rows)
	}
	if rows[0].Value.Float64 != 10 || rows[1].Value.Float64 != 2 {
		t.Errorf("kept hum=%v temp=%v; want hum=10 temp=2", rows[0].Value.Float64, rows[1].Value.Float64)
	}
	fixRow := coalesceLocations("d1", []LatestLocationInput{
		{Latitude: num(1), OccurredTime: t0.Add(time.Second)},
		{Latitude: num(2), OccurredTime: t0.Add(time.Second)},
		{Latitude: num(3), OccurredTime: t0},
	})
	if fixRow == nil || fixRow.Latitude.Float64 != 1 {
		t.Errorf("kept fix = %+v; want latitude 1", fixRow)
	}
	if coalesceLocations("d1", nil) != nil {
		t.Error("no fixes coalesced to a row")
	}
}

// A large batch of readings is written in chunks, and every row lands: 1200 rows are three
// statements of at most 500.
func TestALargeUpsertIsChunkedAndEveryRowLands(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	counter := rdbtest.NewStatementCounter(rdb.FenceTable)
	db := api.RDB.Database.Session(&gorm.Session{Logger: counter})
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	rows := make([]LatestMeasurement, 0, 1200)
	for d := 0; d < 12; d++ {
		inputs := make([]LatestMeasurementInput, 0, 100)
		for n := 0; n < 100; n++ {
			inputs = append(inputs, LatestMeasurementInput{Name: fmt.Sprintf("m%03d", n), Value: num(float64(n)), OccurredTime: t0})
		}
		rows = append(rows, coalesceMeasurements(fmt.Sprintf("dev-%02d", d), inputs)...)
	}
	counter.Reset()
	if err := upsertLatestMeasurements(db.WithContext(ctx), rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if all, _ := counter.Counts(); all != 3 {
		t.Errorf("1200 rows took %d statements; want 3", all)
	}
	var n int64
	if err := api.RDB.DB(ctx).Model(&LatestMeasurement{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1200 {
		t.Errorf("%d rows landed; want 1200", n)
	}
}

// The slice form of the upsert does not bypass the tenant stamp: a row naming another
// tenant is refused, not rewritten, and nothing lands.
func TestAnUpsertRowNamingAnotherTenantIsRefused(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	rows := []LatestMeasurement{{TenantId: "someone-else", DeviceToken: "d1", Name: "temp", Value: num(1),
		OccurredTime: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}}
	err := upsertLatestMeasurements(api.RDB.DB(ctx), rows)
	if !errors.Is(err, rdb.ErrTenantMismatch) {
		t.Fatalf("upsert of another tenant's row returned %v; want ErrTenantMismatch", err)
	}
	var n int64
	if err := api.RDB.DB(core.WithSystemContext(context.Background())).Model(&LatestMeasurement{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rows landed; want 0", n)
	}
}

// A batch with no tenant bound fails closed: MergeProjectionBatch binds each tenant's
// statements itself, and a statement that did not would carry no tenant at all.
func TestABatchStatementWithoutItsTenantFailsClosed(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	err := upsertLatestMeasurements(api.RDB.DB(context.Background()), []LatestMeasurement{{DeviceToken: "d1", Name: "temp",
		OccurredTime: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}})
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("an upsert with no tenant in context returned %v; want core.ErrNoTenant", err)
	}
}

// A device's row created by ANOTHER writer between the batch's lock read and its insert: the
// insert does nothing, and the batch folds its events into the committed row — what merging
// them one at a time would have done after losing the race — rather than failing the batch.
//
// The other writer is simulated inside the batch's own transaction, the one way to put a row
// in that gap deterministically on SQLite: a callback creates it right after the batch's lock
// read of device_states.
func TestABatchFoldsIntoARowAnotherWriterCreatedMeanwhile(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	db := api.RDB.Database
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	armed := true
	if err := db.Callback().Query().After("gorm:query").Register("test:create_meanwhile", func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "device_states" {
			return
		}
		armed = false
		// The other writer's first event: data at t0, external id "theirs" — the row
		// newDeviceState builds, written raw on the batch's own connection.
		if _, err := tx.Statement.ConnPool.ExecContext(context.Background(),
			"INSERT INTO device_states (created_at, updated_at, tenant_id, device_token, external_id, source, active, "+
				"last_connect_time, last_activity_time, inactivity_timeout, presence_source, session_id) "+
				"VALUES (?, ?, 'acme', 'race-1', 'theirs', '', true, ?, ?, 600, 'INFERRED', 0)",
			t0, t0, t0, t0); err != nil {
			t.Errorf("the other writer's insert: %v", err)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(time.Minute), Identity: DeviceIdentity{Source: "mqtt1"}},
		{Tenant: "acme", DeviceToken: "race-1", OccurredAt: t0.Add(2 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("MergeProjectionBatch over a row created meanwhile: %v", err)
	}
	if armed {
		t.Fatal("the callback never ran, so this test did not put a row in the gap")
	}
	var rows []DeviceState
	if err := api.RDB.DB(core.WithTenant(context.Background(), "acme")).Where("device_token = ?", "race-1").Find(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for race-1; want exactly 1", len(rows))
	}
	got := rows[0]
	// The other writer's row, with the batch's events folded in: its identity kept, the
	// batch's source added, activity advanced to the batch's latest, first connect still its.
	if got.ExternalId != "theirs" || got.Source != "mqtt1" {
		t.Errorf("identity = %q/%q; want theirs/mqtt1", got.ExternalId, got.Source)
	}
	if !got.LastActivityTime.Time.Equal(t0.Add(2*time.Minute)) || !got.LastConnectTime.Time.Equal(t0) {
		t.Errorf("activity %v connect %v; want %v and %v", got.LastActivityTime.Time, got.LastConnectTime.Time,
			t0.Add(2*time.Minute), t0)
	}
}
