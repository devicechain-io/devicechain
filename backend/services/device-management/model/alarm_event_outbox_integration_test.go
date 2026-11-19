// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// ownershipPgRig runs the real migration chain. SQLite cannot test the advisory
// lock, so this test uses the same shared Postgres rig as the deletion outbox.
func TestAlarmOutboxMigrationAndConcurrentPostgresDrains(t *testing.T) {
	api, ctx, _ := ownershipPgRig(t, 1)
	require.True(t, api.RDB.DB(ctx).Migrator().HasTable(&PendingAlarmEvent{}))
	require.True(t, api.RDB.DB(ctx).Migrator().HasColumn(&PendingAlarmEvent{}, "quarantined_at"))
	require.True(t, api.RDB.DB(ctx).Migrator().HasColumn(&PendingAlarmEvent{}, "last_error"))
	row := alarmOutboxQueue(t, api, ctx, "outbox-integration")
	tenant, _ := core.TenantFromContext(ctx)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var mu sync.Mutex
	calls := 0
	api.AlarmPublisher = alarmOutboxPublisher(func(pubCtx context.Context, _ *AlarmStateChangeEvent, id string) error {
		require.False(t, core.IsSystemContext(pubCtx))
		if id != alarmEventDedupID(tenant, row.ID) {
			return nil
		}
		mu.Lock()
		calls++
		mu.Unlock()
		close(entered)
		select {
		case <-release:
			return nil
		case <-pubCtx.Done():
			return pubCtx.Err()
		}
	})
	first := make(chan error, 1)
	go func() { first <- api.DrainAlarmEventsLocked(context.Background()) }()
	select {
	case <-entered:
	case err := <-first:
		t.Fatalf("first drain failed before publishing: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("publisher never entered")
	}
	second := make(chan error, 1)
	go func() { second <- api.DrainAlarmEventsLocked(context.Background()) }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, core.ErrPassSkipped)
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent drain waited instead of skipping")
	}
	unblock()
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first drain did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls)
	require.Empty(t, alarmOutboxRows(t, api, ctx))
}
