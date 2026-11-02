// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var droppedT0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func drop(name, reason, value string, at time.Time) DroppedMeasurementInput {
	return DroppedMeasurementInput{Name: name, Reason: reason, Value: value, OccurredTime: at}
}

func droppedRows(t *testing.T, api *Api, ctx context.Context, device string) []*DroppedMeasurement {
	t.Helper()
	rows, err := api.DroppedMeasurementsByDeviceToken(ctx, device)
	require.NoError(t, err)
	return rows
}

// The LA-04 shape: one text key becomes one row, counted, valued, and timed.
func TestADroppedKeyIsOneRowThatCounts(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")

	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("status_text", resolvedevent.DropUndeclaredNonNumeric, "ok", droppedT0)}))
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1)
	assert.Equal(t, "status_text", rows[0].Name)
	assert.Equal(t, resolvedevent.DropUndeclaredNonNumeric, rows[0].Reason)
	assert.Equal(t, "ok", rows[0].LastValue)
	assert.EqualValues(t, 1, rows[0].Count)
	assert.True(t, droppedT0.Equal(rows[0].FirstTime))
	assert.True(t, droppedT0.Equal(rows[0].LastTime))

	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("status_text", resolvedevent.DropUndeclaredNonNumeric, "fault", droppedT0.Add(time.Minute))}))
	rows = droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1, "the same (key, reason) stays one row")
	assert.EqualValues(t, 2, rows[0].Count)
	assert.Equal(t, "fault", rows[0].LastValue)
	assert.True(t, droppedT0.Equal(rows[0].FirstTime))
	assert.True(t, droppedT0.Add(time.Minute).Equal(rows[0].LastTime))

	// One key under two reasons is two rows.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("status_text", resolvedevent.DropUndeclaredUnstorable, "NaN", droppedT0)}))
	n, err := api.DroppedMeasurementCount(ctx, "d1")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "the badge counts rows, not events")
}

// An event delivered late counts but does not move last_time or last_value back.
func TestALateDroppedEventDoesNotMoveLastTimeBack(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	r := resolvedevent.DropUndeclaredNonNumeric

	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("s", r, "new", droppedT0.Add(time.Hour))}))
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("s", r, "old", droppedT0)}))
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0].Count)
	assert.Equal(t, "new", rows[0].LastValue, "last_value follows last_time, not arrival")
	assert.True(t, droppedT0.Add(time.Hour).Equal(rows[0].LastTime))
	assert.True(t, droppedT0.Equal(rows[0].FirstTime), "first_time moves back to the earliest event seen")

	// Negative control: an equal time does not replace the value either (first arrival wins).
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("s", r, "tie", droppedT0.Add(time.Hour))}))
	assert.Equal(t, "new", droppedRows(t, api, ctx, "d1")[0].LastValue)

	// Negative control: a strictly newer one does.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("s", r, "newest", droppedT0.Add(2*time.Hour))}))
	assert.Equal(t, "newest", droppedRows(t, api, ctx, "d1")[0].LastValue)
}

// Several entries for one key in one batch fold into the row exactly as they would one at a
// time, whatever their order within it.
func TestOneBatchFoldsRepeatedDropsOfAKey(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	r := resolvedevent.DropUndeclaredNonNumeric
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("s", r, "b", droppedT0.Add(time.Minute)),
		drop("s", r, "a", droppedT0),
		drop("s", r, "c", droppedT0.Add(2*time.Minute)),
		drop("s", r, "c2", droppedT0.Add(2*time.Minute)),
	}))
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 4, rows[0].Count)
	assert.Equal(t, "c", rows[0].LastValue, "of equal times the first to arrive")
	assert.True(t, droppedT0.Equal(rows[0].FirstTime))
	assert.True(t, droppedT0.Add(2*time.Minute).Equal(rows[0].LastTime))
}

// The bound: 32 rows per device; the 33rd distinct (name, reason) evicts the oldest last_time,
// counts it, and leaves other devices alone.
func TestTheDroppedRecordIsBoundedAt32PerDevice(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	var evicted int64
	api.OnDroppedEvicted = func(n int64) { evicted += n }
	r := resolvedevent.DropUndeclaredNonNumeric

	// Another device, which must be untouched by d1's eviction.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "other", []DroppedMeasurementInput{drop("k", r, "v", droppedT0)}))

	for i := 0; i < MaxDroppedMeasurementsPerDevice; i++ {
		require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
			drop(fmt.Sprintf("k%02d", i), r, "v", droppedT0.Add(time.Duration(i)*time.Second))}))
	}
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, MaxDroppedMeasurementsPerDevice, "32 distinct pairs are all kept")
	assert.EqualValues(t, 0, evicted)

	// The 33rd, the newest: the oldest (k00) goes.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("k32", r, "v", droppedT0.Add(time.Hour))}))
	rows = droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, MaxDroppedMeasurementsPerDevice)
	names := map[string]bool{}
	for _, row := range rows {
		names[row.Name] = true
	}
	assert.False(t, names["k00"], "the oldest last_time is evicted")
	assert.True(t, names["k01"] && names["k32"])
	assert.EqualValues(t, 1, evicted, "dropped_measurement_evicted_total")
	assert.Len(t, droppedRows(t, api, ctx, "other"), 1, "another device's record is not part of the bound")

	// A key that is ALREADY recorded never evicts: updating row 32 is not a 33rd pair.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop("k05", r, "v2", droppedT0.Add(2*time.Hour))}))
	assert.Len(t, droppedRows(t, api, ctx, "d1"), MaxDroppedMeasurementsPerDevice)
	assert.EqualValues(t, 1, evicted)

	// One batch carrying several new pairs evicts exactly the excess, oldest first.
	var burst []DroppedMeasurementInput
	for i := 0; i < 5; i++ {
		burst = append(burst, drop(fmt.Sprintf("burst%d", i), r, "v", droppedT0.Add(3*time.Hour+time.Duration(i)*time.Second)))
	}
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", burst))
	rows = droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, MaxDroppedMeasurementsPerDevice)
	assert.EqualValues(t, 6, evicted)
	names = map[string]bool{}
	for _, row := range rows {
		names[row.Name] = true
	}
	for _, gone := range []string{"k01", "k02", "k03", "k04", "k06"} {
		assert.False(t, names[gone], "%s is among the five oldest last_time", gone)
	}
	assert.True(t, names["k05"], "k05 was refreshed above, so it is no longer old")
	assert.True(t, names["burst0"] && names["burst4"])
}

// Negative control for the bound: with the eviction pass removed, the same sequence leaves
// 33 rows. This is what makes the test above able to fail.
func TestWithoutTheBoundThe33rdRowStays(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	r := resolvedevent.DropUndeclaredNonNumeric
	var rows []DroppedMeasurement
	for i := 0; i <= MaxDroppedMeasurementsPerDevice; i++ {
		rows = append(rows, DroppedMeasurement{DeviceToken: "d1", Name: fmt.Sprintf("k%02d", i), Reason: r,
			LastValue: "v", Count: 1, FirstTime: droppedT0, LastTime: droppedT0.Add(time.Duration(i) * time.Second)})
	}
	// The upsert alone, bypassing enforceDroppedBound.
	require.NoError(t, api.RDB.DB(ctx).Create(&rows).Error)
	assert.Len(t, droppedRows(t, api, ctx, "d1"), MaxDroppedMeasurementsPerDevice+1)
	// And the pass restores the bound.
	require.NoError(t, enforceDroppedBound(api.RDB.DB(ctx), []string{"d1"}))
	assert.Len(t, droppedRows(t, api, ctx, "d1"), MaxDroppedMeasurementsPerDevice)
}

// The record is tenant-scoped: the same device token under two tenants is two records.
func TestTheDroppedRecordIsPerTenant(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	a, b := core.WithTenant(context.Background(), "acme"), core.WithTenant(context.Background(), "globex")
	r := resolvedevent.DropUndeclaredNonNumeric
	require.NoError(t, api.MergeDroppedMeasurements(a, "d1", []DroppedMeasurementInput{drop("s", r, "acme-v", droppedT0)}))
	require.NoError(t, api.MergeDroppedMeasurements(b, "d1", []DroppedMeasurementInput{drop("s", r, "globex-v", droppedT0)}))
	assert.Equal(t, "acme-v", droppedRows(t, api, a, "d1")[0].LastValue)
	assert.Equal(t, "globex-v", droppedRows(t, api, b, "d1")[0].LastValue)
	// A tenant-less context is refused rather than reading across tenants.
	_, err := api.DroppedMeasurementsByDeviceToken(context.Background(), "d1")
	require.Error(t, err)
}

// A value or name that is over its column is cut, a reason outside the shared vocabulary is
// skipped, and an empty batch writes nothing.
func TestDroppedInputsAreBoundedAndValidated(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	r := resolvedevent.DropUndeclaredNonNumeric

	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d1", []DroppedMeasurementInput{
		drop(strings.Repeat("n", 300), r, strings.Repeat("v", 300), droppedT0),
		drop("fourth", "a_reason_this_build_does_not_know", "x", droppedT0),
		drop("", r, "x", droppedT0),
	}))
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1, "the unknown reason and the nameless entry are skipped")
	assert.Len(t, rows[0].Name, 128)
	assert.Len(t, rows[0].LastValue, resolvedevent.MaxDroppedValueBytes)

	require.NoError(t, api.MergeDroppedMeasurements(ctx, "d2", nil))
	assert.Empty(t, droppedRows(t, api, ctx, "d2"))
}

// A batch of events carries its drops with it: the batch writes the same record the per-event
// path does, and evictions are reported once, after the commit.
func TestTheBatchWritesTheDroppedRecord(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	ctx := core.WithTenant(context.Background(), "acme")
	var evicted int64
	api.OnDroppedEvicted = func(n int64) { evicted += n }
	r := resolvedevent.DropUndeclaredNonNumeric

	updates := []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "d1", OccurredAt: droppedT0,
			Measurements: []LatestMeasurementInput{{Name: "battery", Value: num(50), OccurredTime: droppedT0}},
			Dropped:      []DroppedMeasurementInput{drop("status_text", r, "ok", droppedT0)}},
		{Tenant: "acme", DeviceToken: "d1", OccurredAt: droppedT0.Add(time.Second),
			Dropped: []DroppedMeasurementInput{drop("status_text", r, "ok2", droppedT0.Add(time.Second))}},
		{Tenant: "acme", DeviceToken: "d2", OccurredAt: droppedT0},
	}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), updates))
	rows := droppedRows(t, api, ctx, "d1")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0].Count)
	assert.Equal(t, "ok2", rows[0].LastValue)
	assert.Empty(t, droppedRows(t, api, ctx, "d2"))
	assert.EqualValues(t, 0, evicted)

	// 33 pairs in one batch: one eviction, reported once.
	var over []DroppedMeasurementInput
	for i := 0; i <= MaxDroppedMeasurementsPerDevice; i++ {
		over = append(over, drop(fmt.Sprintf("x%02d", i), r, "v", droppedT0.Add(time.Duration(i)*time.Minute)))
	}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "d3", OccurredAt: droppedT0, Dropped: over}}))
	assert.Len(t, droppedRows(t, api, ctx, "d3"), MaxDroppedMeasurementsPerDevice)
	assert.EqualValues(t, 1, evicted)
}

// Rows are visible to exactly the readers of the device's latest readings: the same scope rule.
func TestADroppedRecordIsScopedLikeALatestReading(t *testing.T) {
	assert.Equal(t, LatestMeasurement{}.ScopedBy(), DroppedMeasurement{}.ScopedBy())
	assert.Equal(t, LatestLocation{}.ScopedBy(), DroppedMeasurement{}.ScopedBy())
}

// A NUL in a discarded name or value is removed before the write: PostgreSQL refuses 0x00 in a
// varchar (22021), which would refuse every batch the entry rides in. A name that is only NULs
// is nameless and skipped.
func TestANULInADroppedEntryIsRemoved(t *testing.T) {
	r := resolvedevent.DropUndeclaredNonNumeric
	rows := coalesceDropped("d1", []DroppedMeasurementInput{
		drop("la\x00bel", r, "o\x00k", droppedT0),
		drop("\x00\x00", r, "x", droppedT0),
	})
	require.Len(t, rows, 1)
	assert.Equal(t, "label", rows[0].Name)
	assert.Equal(t, "ok", rows[0].LastValue)

	// Two spellings of one key that differ only by a NUL are one key, not two.
	rows = coalesceDropped("d1", []DroppedMeasurementInput{
		drop("a\x00b", r, "1", droppedT0), drop("ab", r, "2", droppedT0.Add(time.Second))})
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0].Count)
}

// The eviction is ONE statement that chooses the rows by the ordering as it deletes, and it
// names the tenant inside its subquery: with a read and then a delete by id, a row refreshed
// between them is deleted on its old rank. Two tenants with the same device token each keep
// their own bound.
func TestEvictionDeletesInOneOrderedStatementPerTenant(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	r := resolvedevent.DropUndeclaredNonNumeric
	var rowsA, rowsB []DroppedMeasurement
	for i := 0; i <= MaxDroppedMeasurementsPerDevice+2; i++ {
		row := DroppedMeasurement{DeviceToken: "d1", Name: fmt.Sprintf("k%02d", i), Reason: r,
			LastValue: "v", Count: 1, FirstTime: droppedT0, LastTime: droppedT0.Add(time.Duration(i) * time.Second)}
		rowsA = append(rowsA, row)
		if i < 5 {
			rowsB = append(rowsB, row)
		}
	}
	ctxA, ctxB := core.WithTenant(context.Background(), "acme"), core.WithTenant(context.Background(), "globex")
	require.NoError(t, api.RDB.DB(ctxA).Create(&rowsA).Error)
	require.NoError(t, api.RDB.DB(ctxB).Create(&rowsB).Error)

	rec := &sqlRecorder{}
	require.NoError(t, enforceDroppedBound(api.RDB.DB(ctxA).Session(&gorm.Session{Logger: rec}), []string{"d1"}))
	var deletes []string
	for _, s := range rec.sql {
		if strings.HasPrefix(strings.ToUpper(s), "DELETE") {
			deletes = append(deletes, s)
		}
	}
	require.Len(t, deletes, 1, "one DELETE, not a read followed by a delete by id")
	assert.Contains(t, deletes[0], "ORDER BY last_time DESC, id DESC")
	assert.Contains(t, deletes[0], "tenant_id")
	assert.Len(t, droppedRows(t, api, ctxA, "d1"), MaxDroppedMeasurementsPerDevice)
	assert.Len(t, droppedRows(t, api, ctxB, "d1"), 5, "the other tenant's device of the same token is untouched")
}

// sqlRecorder is a gorm logger that keeps the SQL of every statement.
type sqlRecorder struct{ sql []string }

func (r *sqlRecorder) LogMode(gormlogger.LogLevel) gormlogger.Interface { return r }
func (r *sqlRecorder) Info(context.Context, string, ...interface{})     {}
func (r *sqlRecorder) Warn(context.Context, string, ...interface{})     {}
func (r *sqlRecorder) Error(context.Context, string, ...interface{})    {}
func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	s, _ := fc()
	r.sql = append(r.sql, s)
}
