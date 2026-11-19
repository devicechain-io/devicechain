// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
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
	require.True(t, api.RDB.DB(ctx).Migrator().HasConstraint(&PendingAlarmEvent{}, "chk_pending_alarm_events_tenant_id"))
	var indexDef string
	require.NoError(t, api.RDB.DB(ctx).Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = 'device-management' AND indexname = 'idx_pending_alarm_events_tenant_id'`).Scan(&indexDef).Error)
	require.Contains(t, indexDef, "WHERE (quarantined_at IS NULL)")
	require.True(t, api.RDB.DB(ctx).Migrator().HasColumn(&PendingAlarmEvent{}, "quarantined_at"))
	require.True(t, api.RDB.DB(ctx).Migrator().HasColumn(&PendingAlarmEvent{}, "last_error"))
	row := alarmOutboxQueue(t, api, ctx, "outbox-integration")
	tenant, _ := core.TenantFromContext(ctx)
	var observations atomic.Int32
	api.AlarmEventObserver = func(int, time.Duration, int) { observations.Add(1) }
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
	require.EqualValues(t, 2, observations.Load(), "the replica skipping the lock must also observe")
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

// Acknowledge and clear hold the alarm row locked from their re-read to their commit, so the
// event they queue describes the cycle they changed: a concurrent write to the row (a contributor
// fold reactivating it, another operator) waits for them. SQLite ignores the lock clause, so
// only the real engine can show it.
func TestAlarmTransitionsHoldTheRowFromTheReReadToTheCommit(t *testing.T) {
	for _, op := range []string{"ack", "clear"} {
		t.Run(op, func(t *testing.T) {
			api, ctx, _ := ownershipPgRig(t, 1)
			api.suppressAlarmEventKickForTest = true
			a := alarmOutboxSeed(t, api, ctx, fmt.Sprintf("lock-%s-%d", op, time.Now().UnixNano()))
			locked, release := make(chan struct{}), make(chan struct{})
			api.afterAlarmTransitionLockForTest = func() {
				close(locked)
				<-release
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if op == "ack" {
					_, err = api.AcknowledgeAlarm(ctx, a.Token, nil)
				} else {
					_, err = api.ClearAlarm(ctx, a.Token)
				}
				done <- err
			}()
			select {
			case <-locked:
			case <-time.After(10 * time.Second):
				t.Fatal("the transition never read the row")
			}
			// A competing write while the transition holds the row must wait.
			wctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			err := api.RDB.DB(wctx).Model(&Alarm{}).Where("id = ?", a.ID).Update("raised_time", tsec(500)).Error
			require.Error(t, err, "a concurrent write to the alarm went through while the transition held it")
			close(release)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the transition did not finish")
			}
			rows := alarmOutboxRows(t, api, ctx)
			require.Len(t, rows, 1)
			var ev AlarmStateChangeEvent
			require.NoError(t, json.Unmarshal(rows[0].Payload, &ev))
			require.True(t, tsec(10).Equal(ev.RaisedTime), "the event does not describe the cycle the transition locked")
		})
	}
}
