// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The dropped-measurement record's write against a REAL PostgreSQL, for the two things SQLite
// cannot answer: a varchar that refuses 0x00 (SQLSTATE 22021), and row-lock ordering between the
// batch and the per-message path (SQLSTATE 40P01).
package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A discarded value carrying NUL cannot fail the write: it is recorded without it, and the
// bystander device batched with it is unaffected. The first half is the control: PostgreSQL
// itself refuses the NUL, so the strip is what lets the batch through (take the strip out of
// cutTo and the batch half fails with 22021).
func TestANULBearingDropCannotRefuseTheBatchOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "nul")
	const tenant = "nul-tenant"
	ctx := core.WithTenant(context.Background(), tenant)
	r := resolvedevent.DropUndeclaredNonNumeric
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	err := api.RDB.DB(ctx).Create(&DroppedMeasurement{DeviceToken: "raw", Name: "k", Reason: r,
		LastValue: "a\x00b", Count: 1, FirstTime: t0, LastTime: t0}).Error
	require.Error(t, err, "control: the database refuses the NUL itself")
	require.Contains(t, err.Error(), "22021")

	err = api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: tenant, DeviceToken: "nul-dev", OccurredAt: t0, Dropped: []DroppedMeasurementInput{
			{Name: "la\x00bel", Reason: r, Value: "o\x00k", OccurredTime: t0}}},
		{Tenant: tenant, DeviceToken: "bystander", OccurredAt: t0, Dropped: []DroppedMeasurementInput{
			{Name: "status", Reason: r, Value: "fine", OccurredTime: t0}}},
	})
	require.NoError(t, err, "a NUL in one device's drop must not refuse the batch")

	got, err := api.DroppedMeasurementsByDeviceToken(ctx, "nul-dev")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "label", got[0].Name)
	require.Equal(t, "ok", got[0].LastValue)
	other, err := api.DroppedMeasurementsByDeviceToken(ctx, "bystander")
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.Equal(t, "fine", other[0].LastValue)

	// The per-message path takes the same entry.
	require.NoError(t, api.MergeDroppedMeasurements(ctx, "nul-dev", []DroppedMeasurementInput{
		{Name: "x\x00y", Reason: r, Value: "\x00", OccurredTime: t0}}))
}

// barrier holds the first arrival until a second one comes or the wait is over: it lets a test
// put two transactions at the same point (just before the eviction DELETE) when nothing forbids
// it, and costs only the wait when something does (the device lock, which is the point).
type barrier struct {
	mu      sync.Mutex
	arrived int
	release chan struct{}
	wait    time.Duration
}

func newBarrier(wait time.Duration) *barrier {
	return &barrier{release: make(chan struct{}), wait: wait}
}

func (b *barrier) arrive() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == 2 {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-time.After(b.wait):
	}
}

// A batch and a per-message transaction for ONE device at the bound serialize: both refresh an
// old row, both overshoot, and both then delete the same two oldest rows. Without the per-message
// path taking the device's row lock first, each holds one of the victims and waits for the other
// (40P01 deadlock detected; take lockOrCreate out of MergeDroppedMeasurements and this fails).
// With it the second waits for the first to commit, the bound holds, and nothing is refused.
func TestABatchAndAPerMessageWriteForOneDeviceAtTheBoundDoNotDeadlockOnPostgres(t *testing.T) {
	api := newPostgresProjectionApi(t, "dl")
	const tenant, dev = "dl-tenant", "hot"
	ctx := core.WithTenant(context.Background(), tenant)
	r := resolvedevent.DropUndeclaredNonNumeric
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// The device has its state row (the per-message path always writes it first) and 32 drop
	// rows, k00 and k01 the oldest.
	var seed []DroppedMeasurementInput
	for i := 0; i < MaxDroppedMeasurementsPerDevice; i++ {
		seed = append(seed, DroppedMeasurementInput{Name: fmt.Sprintf("k%02d", i), Reason: r, Value: "v",
			OccurredTime: t0.Add(time.Duration(i) * time.Second)})
	}
	require.NoError(t, api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{
		{Tenant: tenant, DeviceToken: dev, OccurredAt: t0, Dropped: seed}}))
	require.Len(t, mustDropped(t, api, ctx, dev), MaxDroppedMeasurementsPerDevice)

	bar := newBarrier(1500 * time.Millisecond)
	const hook = "dropped-test-barrier"
	require.NoError(t, api.RDB.Database.Callback().Delete().Before("gorm:delete").Register(hook, func(db *gorm.DB) {
		if db.Statement.Table == "dropped_measurements" {
			bar.arrive()
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Delete().Remove(hook) })

	late := func(name string, i int) DroppedMeasurementInput { // an old event: refreshes the row, moves no time
		return DroppedMeasurementInput{Name: name, Reason: r, Value: "late", OccurredTime: t0.Add(time.Duration(i) * time.Second)}
	}
	fresh := func(name string, i int) DroppedMeasurementInput {
		return DroppedMeasurementInput{Name: name, Reason: r, Value: "new", OccurredTime: t0.Add(time.Hour + time.Duration(i)*time.Second)}
	}
	errs := make(chan error, 2)
	go func() {
		errs <- api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{{Tenant: tenant, DeviceToken: dev,
			OccurredAt: t0, Dropped: []DroppedMeasurementInput{late("k01", 1), fresh("a1", 0), fresh("a2", 1)}}})
	}()
	go func() {
		errs <- api.MergeDroppedMeasurements(ctx, dev, []DroppedMeasurementInput{late("k00", 0), fresh("b1", 2), fresh("b2", 3)})
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			require.NoError(t, err)
		case <-time.After(60 * time.Second):
			t.Fatal("a writer never finished")
		}
	}
	rows := mustDropped(t, api, ctx, dev)
	require.Len(t, rows, MaxDroppedMeasurementsPerDevice, "the bound holds, none overshoots")
	names := map[string]bool{}
	for _, row := range rows {
		names[row.Name] = true
	}
	for _, n := range []string{"a1", "a2", "b1", "b2"} {
		require.True(t, names[n], "%s is the newest and survives", n)
	}
	require.False(t, names["k00"] || names["k01"], "the two oldest were evicted")
}

func mustDropped(t *testing.T, api *Api, ctx context.Context, dev string) []*DroppedMeasurement {
	t.Helper()
	rows, err := api.DroppedMeasurementsByDeviceToken(ctx, dev)
	require.NoError(t, err)
	return rows
}
