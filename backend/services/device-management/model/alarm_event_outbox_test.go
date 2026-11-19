// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type alarmOutboxPublisher func(context.Context, *AlarmStateChangeEvent, string) error

func (p alarmOutboxPublisher) PublishAlarmEventDedup(ctx context.Context, ev *AlarmStateChangeEvent, id string) error {
	return p(ctx, ev, id)
}

func alarmOutboxRig(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, _ := newContributorTestApi(t)
	api.suppressAlarmEventKickForTest = true
	ctx := core.WithTenant(context.Background(), "acme")
	return api, ctx
}
func alarmOutboxSeed(t *testing.T, api *Api, ctx context.Context, token string) *Alarm {
	t.Helper()
	a := &Alarm{AlarmKey: token, State: string(AlarmStateActive), Severity: "MAJOR", RaisedTime: tsec(10), OriginatorType: "asset", OriginatorId: 42}
	a.Token = token
	require.NoError(t, api.RDB.DB(ctx).Create(a).Error)
	return a
}
func alarmOutboxRows(t *testing.T, api *Api, ctx context.Context) []PendingAlarmEvent {
	t.Helper()
	var rows []PendingAlarmEvent
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&rows).Error)
	return rows
}
func alarmOutboxQueue(t *testing.T, api *Api, ctx context.Context, token string) PendingAlarmEvent {
	t.Helper()
	a := alarmOutboxSeed(t, api, ctx, token)
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return queueAlarmEventOnTx(ctx, tx, a, newAlarmStateChangeEvent(a, AlarmEventRaised, "", tsec(10)))
	}))
	rows := alarmOutboxRows(t, api, ctx)
	require.NotEmpty(t, rows)
	return rows[len(rows)-1]
}
func waitAlarmRelay(t *testing.T, api *Api) {
	t.Helper()
	require.Eventually(t, func() bool { api.alarmEventMu.Lock(); defer api.alarmEventMu.Unlock(); return !api.alarmEventRunning }, 2*time.Second, time.Millisecond)
}

// Each operation waits once, even a five-alarm bulk. A stalled broker cannot hold
// a committed mutation beyond the configured budget; recovery drains every snapshot.
func TestAlarmOutboxBrokerDownBoundAndRecovery(t *testing.T) {
	for _, operation := range []string{"ack", "clear", "raise", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			api, ctx := alarmOutboxRig(t)
			api.suppressAlarmEventKickForTest = false
			api.alarmEventWait = 80 * time.Millisecond
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var healed atomic.Bool
			var mu sync.Mutex
			var ids []string
			api.AlarmPublisher = alarmOutboxPublisher(func(pubCtx context.Context, _ *AlarmStateChangeEvent, id string) error {
				tenant, _ := core.TenantFromContext(pubCtx)
				if tenant != "acme" || core.IsSystemContext(pubCtx) {
					return errors.New("wrong publishing authority")
				}
				if !healed.Load() {
					select {
					case <-release:
						return errors.New("broker down")
					case <-pubCtx.Done():
						return pubCtx.Err()
					}
				}
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
				return nil
			})
			var tokens []string
			count := 1
			if operation == "bulk" {
				count = 5
			}
			if operation != "raise" {
				for i := 0; i < count; i++ {
					tokens = append(tokens, alarmOutboxSeed(t, api, ctx, fmt.Sprint("a", i)).Token)
				}
			}
			done := make(chan error, 1)
			start := time.Now()
			go func() {
				var err error
				switch operation {
				case "ack":
					_, err = api.AcknowledgeAlarm(ctx, tokens[0], nil)
				case "clear":
					_, err = api.ClearAlarm(ctx, tokens[0])
				case "raise":
					err = api.ApplyAlarmContributorEdge(ctx, dev, "hot", "temp", "r1", AlarmEdgeRaised, "MAJOR", nil, tsec(10), new(bool))
				case "bulk":
					_, err = api.AcknowledgeAlarms(ctx, tokens, nil)
				}
				done <- err
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(4 * api.alarmEventWait):
				t.Fatal("mutation exceeded one bounded relay wait")
			}
			require.Less(t, time.Since(start), 4*api.alarmEventWait)
			rows := alarmOutboxRows(t, api, ctx)
			require.Len(t, rows, count)
			unblock()
			waitAlarmRelay(t, api)
			healed.Store(true)
			res, err := api.DrainAlarmEvents(context.Background(), AlarmEventDrainPageSize)
			require.NoError(t, err)
			require.Equal(t, count, res.Published)
			mu.Lock()
			defer mu.Unlock()
			var want []string
			for _, row := range rows {
				want = append(want, fmt.Sprintf("alarm-event-acme-%d", row.ID))
			}
			require.Equal(t, want, ids)
			require.Empty(t, alarmOutboxRows(t, api, ctx))
		})
	}
}

func TestAlarmOutboxCrashRecoveryAndHealthyReply(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	a := alarmOutboxSeed(t, api, ctx, "alarm")
	calls := 0
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error { calls++; return nil })
	_, err := api.AcknowledgeAlarm(ctx, a.Token, nil)
	require.NoError(t, err)
	require.Zero(t, calls)
	require.Len(t, alarmOutboxRows(t, api, ctx), 1)
	fresh := NewApi(api.RDB)
	fresh.AlarmPublisher = api.AlarmPublisher
	_, err = fresh.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Empty(t, alarmOutboxRows(t, api, ctx))
	api.suppressAlarmEventKickForTest = false
	_, err = api.ClearAlarm(ctx, a.Token)
	require.NoError(t, err)
	require.Equal(t, 2, calls, "healthy publication must finish before the mutation answers")
}

func TestAlarmOutboxRollbackAndLostCAS(t *testing.T) {
	for _, operation := range []string{"ack", "clear", "raise", "update"} {
		t.Run(operation, func(t *testing.T) {
			api, ctx := alarmOutboxRig(t)
			a := alarmOutboxSeed(t, api, ctx, "alarm")
			failure := errors.New("failure after enqueue")
			api.afterAlarmEventEnqueueForTest = func(tx *gorm.DB) error {
				var n int64
				require.NoError(t, tx.Model(&PendingAlarmEvent{}).Count(&n).Error)
				require.EqualValues(t, 1, n, "hook must run after enqueue in the transition transaction")
				return failure
			}
			var err error
			switch operation {
			case "ack":
				_, err = api.AcknowledgeAlarm(ctx, a.Token, nil)
			case "clear":
				_, err = api.ClearAlarm(ctx, a.Token)
			case "raise":
				err = api.ApplyAlarmContributorEdge(ctx, dev, "new", "temp", "r1", AlarmEdgeRaised, "MAJOR", nil, tsec(10), new(bool))
			case "update":
				err = api.updateAlarmFromContributors(ctx, a, "CRITICAL", true, sql.NullFloat64{}, []byte(`{}`), tsec(20), new(bool))
			}
			require.ErrorIs(t, err, failure)
			require.Empty(t, alarmOutboxRows(t, api, ctx))
			var got Alarm
			require.NoError(t, api.RDB.DB(ctx).First(&got, a.ID).Error)
			require.False(t, got.Acknowledged)
			require.Equal(t, string(AlarmStateActive), got.State)
			require.Equal(t, "MAJOR", got.Severity)
			var count int64
			require.NoError(t, api.RDB.DB(ctx).Model(&Alarm{}).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
	api, ctx := alarmOutboxRig(t)
	a := alarmOutboxSeed(t, api, ctx, "alarm")
	require.NoError(t, api.RDB.DB(ctx).Model(a).Update("contributor_version", 9).Error)
	a.ContributorVersion = 0
	err := api.updateAlarmFromContributors(ctx, a, "CRITICAL", true, sql.NullFloat64{}, []byte(`{}`), tsec(20), new(bool))
	require.ErrorIs(t, err, errAlarmContributorConflict)
	require.Empty(t, alarmOutboxRows(t, api, ctx))
}

func TestAlarmOutboxFreshCycleForAckAndClear(t *testing.T) {
	for _, kind := range []AlarmEventType{AlarmEventAcknowledged, AlarmEventCleared} {
		t.Run(string(kind), func(t *testing.T) {
			api, ctx := alarmOutboxRig(t)
			stale := alarmOutboxSeed(t, api, ctx, "alarm")
			freshCycle := tsec(100)
			require.NoError(t, api.RDB.DB(ctx).Model(&Alarm{}).Where("id = ?", stale.ID).Update("raised_time", freshCycle).Error)
			var err error
			if kind == AlarmEventAcknowledged {
				_, err = api.applyAcknowledgement(ctx, stale, nil)
			} else {
				_, err = api.applyAlarmTransition(ctx, stale, kind, func(tx *gorm.DB, a *Alarm) (*AlarmStateChangeEvent, error) { return clearAlarmTx(ctx, tx, a) })
			}
			require.NoError(t, err)
			require.True(t, freshCycle.Equal(stale.RaisedTime), "copy authoritative row back to caller")
			rows := alarmOutboxRows(t, api, ctx)
			require.Len(t, rows, 1)
			var ev AlarmStateChangeEvent
			require.NoError(t, json.Unmarshal(rows[0].Payload, &ev))
			require.True(t, freshCycle.Equal(ev.RaisedTime), "event must use the reactivated cycle")
		})
	}
}

func TestAlarmOutboxTenantValidation(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	a := alarmOutboxSeed(t, api, ctx, "alarm")
	ev := newAlarmStateChangeEvent(a, AlarmEventRaised, "", tsec(10))
	require.Error(t, queueAlarmEventOnTx(core.WithTenant(ctx, "other"), api.RDB.DB(ctx), a, ev))
	a.TenantId = ""
	require.Error(t, queueAlarmEventOnTx(ctx, api.RDB.DB(ctx), a, ev))
	require.Empty(t, alarmOutboxRows(t, api, ctx))
	api.AlarmPublisher = nil
	_, err := api.DrainAlarmEvents(context.Background(), 10)
	require.ErrorIs(t, err, ErrNoAlarmEventPublisher)
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error { return nil })
	for _, size := range []int{0, AlarmEventDrainPageSize + 1} {
		_, err = api.DrainAlarmEvents(context.Background(), size)
		require.Error(t, err)
	}
	_, err = api.DrainAlarmEvents(core.WithSystemContext(context.Background()), 10)
	require.Error(t, err)
}

func TestAlarmOutboxOrderingFailureAndPoison(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	first := alarmOutboxQueue(t, api, ctx, "first")
	poison := alarmOutboxQueue(t, api, ctx, "poison")
	second := alarmOutboxQueue(t, api, ctx, "second")
	// A row queued after the one the broker refuses: it must wait for the next pass, in order.
	after := alarmOutboxQueue(t, api, ctx, "after")
	third := alarmOutboxQueue(t, api, core.WithTenant(context.Background(), "zeta"), "third")
	require.NoError(t, api.RDB.DB(ctx).Model(&PendingAlarmEvent{}).Where("id = ?", poison.ID).Update("payload", []byte(`not-json`)).Error)
	var calls []string
	failure := fmt.Errorf("broker down: %w", ErrAlarmEventBrokerUnavailable)
	api.AlarmPublisher = alarmOutboxPublisher(func(pubCtx context.Context, ev *AlarmStateChangeEvent, id string) error {
		require.False(t, core.IsSystemContext(pubCtx))
		tenant, _ := core.TenantFromContext(pubCtx)
		require.Equal(t, alarmEventDedupID(tenant, map[string]uint64{"first": first.ID, "second": second.ID, "after": after.ID, "third": third.ID}[ev.AlarmToken]), id)
		calls = append(calls, ev.AlarmToken)
		if ev.AlarmToken == "second" {
			return failure
		}
		return nil
	})
	var observedPending, observedQ int
	api.AlarmEventObserver = func(pending int, oldest time.Duration, q int) {
		observedPending, observedQ = pending, q
		require.GreaterOrEqual(t, oldest, time.Duration(0))
	}
	res, err := api.DrainAlarmEvents(context.Background(), 10)
	require.ErrorIs(t, err, failure)
	require.Equal(t, []string{"first", "second"}, calls)
	require.Equal(t, 1, res.Published)
	require.Equal(t, 1, res.Quarantined)
	require.Equal(t, 3, observedPending)
	require.Equal(t, 1, observedQ)
	rows := alarmOutboxRows(t, api, ctx)
	require.Len(t, rows, 3)
	require.Equal(t, poison.ID, rows[0].ID)
	require.NotNil(t, rows[0].QuarantinedAt)
	require.NotEmpty(t, rows[0].LastError)
	api.AlarmPublisher = alarmOutboxPublisher(func(pubCtx context.Context, ev *AlarmStateChangeEvent, _ string) error {
		require.False(t, core.IsSystemContext(pubCtx))
		calls = append(calls, ev.AlarmToken)
		return nil
	})
	res, err = api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 3, res.Published)
	require.Zero(t, res.Quarantined)
	require.Equal(t, []string{"first", "second", "second", "after", "third"}, calls)
	require.Len(t, alarmOutboxRows(t, api, ctx), 1)
	require.Zero(t, observedPending)
	require.Equal(t, 1, observedQ)
	// A tenant left with only a quarantined row is not visited again, but still observed.
	res, err = api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, res.Tenants, "a quarantine-only tenant was visited")
	require.Equal(t, 1, observedQ)
}

func TestAlarmOutboxInvalidAndUnpublishableQuarantine(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	for _, payload := range []string{`{}`, `{"AlarmToken":"a","EventType":"RAISED"}`} {
		row := PendingAlarmEvent{TenantId: "acme", Payload: []byte(payload), CreatedAt: tsec(10)}
		require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
	}
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error {
		return fmt.Errorf("encoding failed: %w", ErrAlarmEventUnpublishable)
	})
	res, err := api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, res.Quarantined)
	require.Zero(t, res.Published)
	rows := alarmOutboxRows(t, api, ctx)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.NotNil(t, row.QuarantinedAt)
		require.NotNil(t, row.LastError)
	}
}

func TestAlarmOutboxImmutableSuppressionAndDescription(t *testing.T) {
	r := newSuppressionCycleRig(t)
	r.api.suppressAlarmEventKickForTest = true
	require.NoError(t, r.edge(r.now, "r1", AlarmEdgeRaised, "MAJOR"))
	rows := alarmOutboxRows(t, r.api, r.ctx)
	require.Len(t, rows, 1)
	var stored AlarmStateChangeEvent
	require.NoError(t, json.Unmarshal(rows[0].Payload, &stored))
	require.NotNil(t, stored.SuppressedUntil)
	require.NotEmpty(t, stored.DeviceToken)
	calls := 0
	suppressionEvaluationHookForTest = func(context.Context, uint, string, string, time.Time) (*SuppressionMatch, error) {
		calls++
		return nil, nil
	}
	defer func() { suppressionEvaluationHookForTest = nil }()
	// A query callback counts forbidden re-description during relay retries.
	descriptions := 0
	require.NoError(t, r.api.RDB.Database.Callback().Query().Before("gorm:query").Register("outbox_description_test", func(tx *gorm.DB) {
		if tx.Statement.Table == "devices" {
			descriptions++
		}
	}))
	defer r.api.RDB.Database.Callback().Query().Remove("outbox_description_test")
	fail := true
	r.api.AlarmPublisher = alarmOutboxPublisher(func(_ context.Context, ev *AlarmStateChangeEvent, _ string) error {
		encoded, err := json.Marshal(ev)
		require.NoError(t, err)
		require.JSONEq(t, string(rows[0].Payload), string(encoded))
		if fail {
			return errors.New("broker down")
		}
		return nil
	})
	_, err := r.api.DrainAlarmEvents(context.Background(), 10)
	require.Error(t, err)
	require.Equal(t, rows[0].Payload, alarmOutboxRows(t, r.api, r.ctx)[0].Payload)
	fail = false
	_, err = r.api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, calls, "relay must not re-evaluate suppression")
	require.Zero(t, descriptions, "relay must not re-describe the device")
}

func TestAlarmOutboxRelayIgnoresCallerContext(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	alarmOutboxQueue(t, api, ctx, "alarm")
	api.suppressAlarmEventKickForTest = false
	published := make(chan struct{}, 1)
	api.AlarmPublisher = alarmOutboxPublisher(func(ctx context.Context, _ *AlarmStateChangeEvent, _ string) error {
		if core.IsSystemContext(ctx) || ctx.Err() != nil {
			return errors.New("request context leaked")
		}
		published <- struct{}{}
		return nil
	})
	caller, cancel := context.WithCancel(core.WithSystemContext(ctx))
	cancel()
	api.relayAlarmEvents(caller)
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("cancelled system caller prevented detached relay")
	}
	require.Empty(t, alarmOutboxRows(t, api, ctx))
}

func TestAlarmOutboxBulkPartialFailureStillKicks(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	a := alarmOutboxSeed(t, api, ctx, "a")
	b := alarmOutboxSeed(t, api, ctx, "b")
	api.suppressAlarmEventKickForTest = false
	calls := 0
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error { calls++; return nil })
	failure := errors.New("second alarm failed")
	enqueues := 0
	api.afterAlarmEventEnqueueForTest = func(*gorm.DB) error {
		enqueues++
		if enqueues == 2 {
			return failure
		}
		return nil
	}
	_, err := api.AcknowledgeAlarms(ctx, []string{a.Token, b.Token}, nil)
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, calls)
	require.Empty(t, alarmOutboxRows(t, api, ctx))
}

// A kick while the first pass is blocked must wait for a shared second pass,
// rather than being satisfied by completion of a pass started before its kick.
func TestAlarmOutboxRelayCoalescesFollowupPass(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	alarmOutboxQueue(t, api, ctx, "alarm")
	api.suppressAlarmEventKickForTest = false
	api.alarmEventWait = 15 * time.Millisecond
	entered := make(chan int, 4)
	first := make(chan struct{})
	second := make(chan struct{})
	var once1, once2 sync.Once
	defer once1.Do(func() { close(first) })
	defer once2.Do(func() { close(second) })
	var calls atomic.Int32
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error {
		n := int(calls.Add(1))
		entered <- n
		if n == 1 {
			<-first
			return errors.New("broker down")
		}
		<-second
		return nil
	})
	api.relayAlarmEvents(ctx)
	select {
	case n := <-entered:
		require.Equal(t, 1, n)
	case <-time.After(time.Second):
		t.Fatal("first relay never reached publisher")
	}
	var kicks sync.WaitGroup
	for range 40 {
		kicks.Add(1)
		go func() { defer kicks.Done(); api.relayAlarmEvents(ctx) }()
	}
	kicks.Wait()
	require.EqualValues(t, 1, calls.Load())
	// One extra waiter has a large budget, letting us verify which pass completes it.
	api.alarmEventWait = time.Second
	waiter := make(chan struct{})
	go func() { api.relayAlarmEvents(ctx); close(waiter) }()
	// Observe that its kick was accepted before allowing the first pass to end.
	require.Eventually(t, func() bool {
		api.alarmEventMu.Lock()
		defer api.alarmEventMu.Unlock()
		return api.alarmEventPending != nil
	}, time.Second, time.Millisecond)
	once1.Do(func() { close(first) })
	select {
	case n := <-entered:
		require.Equal(t, 2, n)
	case <-time.After(time.Second):
		t.Fatal("follow-up relay never reached publisher")
	}
	select {
	case <-waiter:
		t.Fatal("kick completed on a pass that had already started")
	case <-time.After(20 * time.Millisecond):
	}
	once2.Do(func() { close(second) })
	select {
	case <-waiter:
	case <-time.After(time.Second):
		t.Fatal("follow-up did not finish")
	}
	waitAlarmRelay(t, api)
	require.EqualValues(t, 2, calls.Load(), "kicks must share a single pending pass")
	require.Empty(t, alarmOutboxRows(t, api, ctx))
}

func TestAlarmOutboxMissingTableRollsBack(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	a := alarmOutboxSeed(t, api, ctx, "alarm")
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&PendingAlarmEvent{}))
	_, err := api.AcknowledgeAlarm(ctx, a.Token, nil)
	require.Error(t, err)
	var fresh Alarm
	require.NoError(t, api.RDB.DB(ctx).First(&fresh, a.ID).Error)
	require.False(t, fresh.Acknowledged, "missing outbox must fail the transition closed")
}

// Shutdown ends the post-commit relay: a running pass is cancelled and joined, a pending
// follow-up is not run, and later kicks return at once, leaving their rows for the periodic drain.
func TestAlarmOutboxRelayStopsOnShutdown(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	alarmOutboxQueue(t, api, ctx, "alarm")
	api.suppressAlarmEventKickForTest = false
	api.alarmEventWait = 20 * time.Millisecond
	var calls atomic.Int32
	entered := make(chan struct{}, 4)
	api.AlarmPublisher = alarmOutboxPublisher(func(pubCtx context.Context, _ *AlarmStateChangeEvent, _ string) error {
		calls.Add(1)
		entered <- struct{}{}
		<-pubCtx.Done()
		return pubCtx.Err()
	})
	api.relayAlarmEvents(ctx)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("the relay never reached the publisher")
	}
	api.relayAlarmEvents(ctx) // a follow-up kick, pending behind the blocked pass
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, api.StopAlarmEventRelay(stopCtx), "the running pass was not cancelled and joined")
	require.EqualValues(t, 1, calls.Load(), "a pending pass ran after shutdown")
	api.alarmEventMu.Lock()
	idle := api.alarmRelayIdle
	api.alarmEventMu.Unlock()
	api.alarmEventWait = time.Second
	started := time.Now()
	api.relayAlarmEvents(ctx)
	api.alarmEventMu.Lock()
	require.True(t, idle == api.alarmRelayIdle && !api.alarmEventRunning, "a kick after shutdown started a relay worker")
	api.alarmEventMu.Unlock()
	require.Less(t, time.Since(started), 200*time.Millisecond, "a kick after shutdown waited for a pass")
	require.EqualValues(t, 1, calls.Load(), "a kick after shutdown started a pass")
	require.Len(t, alarmOutboxRows(t, api, ctx), 1, "the event must stay queued for the periodic drain")
}

// A pass publishes its whole page through the publisher it started with, so a broker restart
// that replaces the publisher mid-pass is not raced; the next pass uses the replacement.
func TestAlarmOutboxPassKeepsItsPublisherWhenItIsReplaced(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	alarmOutboxQueue(t, api, ctx, "one")
	alarmOutboxQueue(t, api, ctx, "two")
	var mu sync.Mutex
	var by []string
	record := func(name string) { mu.Lock(); by = append(by, name); mu.Unlock() }
	replacement := alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error {
		record("replacement")
		return nil
	})
	first := true
	api.SetAlarmPublisher(alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error {
		if first {
			first = false
			api.SetAlarmPublisher(replacement) // the broker restarted mid-pass
		}
		record("original")
		return nil
	}))
	_, err := api.DrainAlarmEvents(context.Background(), AlarmEventDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, []string{"original", "original"}, by)
	alarmOutboxQueue(t, api, ctx, "three")
	_, err = api.DrainAlarmEvents(context.Background(), AlarmEventDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, []string{"original", "original", "replacement"}, by)
}

func TestAlarmOutboxUnknownTypeStaysUntouched(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	row := PendingAlarmEvent{TenantId: "acme", Payload: []byte(`{"AlarmToken":"future","EventType":"FUTURE"}`), CreatedAt: tsec(10)}
	require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
	alarmOutboxQueue(t, api, ctx, "later")
	var calls []string
	api.AlarmPublisher = alarmOutboxPublisher(func(_ context.Context, ev *AlarmStateChangeEvent, _ string) error {
		calls = append(calls, ev.AlarmToken)
		return nil
	})
	api.AlarmEventDeadLetter = func(context.Context, string, []byte, error) error {
		t.Fatal("unknown type was dead-lettered")
		return nil
	}
	res, err := api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, res.Skipped)
	require.Equal(t, 1, res.Published)
	require.Zero(t, res.Quarantined)
	require.Equal(t, []string{"later"}, calls)
	rows := alarmOutboxRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.Equal(t, row, rows[0])
}

func TestAlarmOutboxStoredJSONKeys(t *testing.T) {
	by, value, derived, until := "operator", 42.0, true, tsec(30)
	ev := AlarmStateChangeEvent{
		ClearReason: "reason", EventType: AlarmEventEscalated, AlarmToken: "alarm",
		OriginatorType: "device", OriginatorId: 1, AlarmKey: "key", MetricKey: "metric",
		State: "ACTIVE", Severity: "CRITICAL", Acknowledged: true, PreviousSeverity: "MAJOR",
		AcknowledgedBy: &by, LastValue: &value, RaisedTime: tsec(10), OccurredTime: tsec(20),
		ScopeAnchors: []string{"anchor"}, DeviceToken: "device", DeviceName: "name", DeviceTypeToken: "type",
		PositionDerived: &derived, SuppressedUntil: &until, SuppressionWindowToken: "window",
		RaisedDuringSuppressionToken: "raised-window",
	}
	payload, err := json.Marshal(ev)
	require.NoError(t, err)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &stored))
	keys := make([]string, 0, len(stored))
	for key := range stored {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, []string{
		"ClearReason", "EventType", "AlarmToken", "OriginatorType", "OriginatorId", "AlarmKey", "MetricKey",
		"State", "Severity", "Acknowledged", "PreviousSeverity", "AcknowledgedBy", "LastValue", "RaisedTime",
		"OccurredTime", "ScopeAnchors", "DeviceToken", "DeviceName", "DeviceTypeToken", "PositionDerived",
		"SuppressedUntil", "SuppressionWindowToken", "RaisedDuringSuppressionToken",
	}, keys)
}

func TestAlarmOutboxTenantFailureIsolation(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			api, ctx := alarmOutboxRig(t)
			alarmOutboxQueue(t, api, ctx, "a")
			alarmOutboxQueue(t, api, ctx, "a-later")
			zctx := core.WithTenant(context.Background(), "zeta")
			alarmOutboxQueue(t, api, zctx, "b")
			cause := errors.New("tenant refused")
			if unavailable {
				cause = fmt.Errorf("disconnected: %w", ErrAlarmEventBrokerUnavailable)
			}
			var calls []string
			api.AlarmPublisher = alarmOutboxPublisher(func(_ context.Context, ev *AlarmStateChangeEvent, _ string) error {
				calls = append(calls, ev.AlarmToken)
				if ev.AlarmToken == "a" {
					return cause
				}
				return nil
			})
			res, err := api.DrainAlarmEvents(context.Background(), 10)
			require.ErrorIs(t, err, cause)
			require.Equal(t, 1, res.Failed)
			require.Len(t, alarmOutboxRows(t, api, ctx), 2)
			if unavailable {
				require.Equal(t, []string{"a"}, calls)
				require.Zero(t, res.Published)
				require.Len(t, alarmOutboxRows(t, api, zctx), 1)
			} else {
				require.Equal(t, []string{"a", "b"}, calls)
				require.Equal(t, 1, res.Published)
				require.Empty(t, alarmOutboxRows(t, api, zctx))
			}
		})
	}
}

func TestAlarmOutboxEmptyTenantConstraintAndDiscovery(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	sys := api.RDB.DB(core.WithSystemContext(context.Background()))
	row := PendingAlarmEvent{Payload: []byte(`{}`), CreatedAt: tsec(10)}
	require.Error(t, sys.Create(&row).Error, "the live model must reject an empty tenant")
	// Simulate a legacy malformed row while retaining the real constraint for ordinary writes.
	require.NoError(t, sys.Exec("PRAGMA ignore_check_constraints = ON").Error)
	require.NoError(t, sys.Create(&row).Error)
	require.NoError(t, sys.Exec("PRAGMA ignore_check_constraints = OFF").Error)
	alarmOutboxQueue(t, api, ctx, "valid")
	calls := 0
	api.AlarmPublisher = alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error { calls++; return nil })
	res, err := api.DrainAlarmEvents(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, res.Published)
}

func TestAlarmOutboxPeriodicObserverRunsBeforePublisher(t *testing.T) {
	api, ctx := alarmOutboxRig(t)
	row := alarmOutboxQueue(t, api, ctx, "queued")
	require.NoError(t, api.RDB.DB(ctx).Model(&PendingAlarmEvent{}).Where("id = ?", row.ID).Update("created_at", tsec(10)).Error)
	observations := 0
	api.AlarmEventObserver = func(n int, age time.Duration, q int) {
		observations++
		require.Equal(t, 1, n)
		require.Greater(t, age, time.Second)
		require.Zero(t, q)
	}
	api.SetAlarmPublisher(nil)
	require.ErrorIs(t, api.DrainAlarmEventsLocked(context.Background()), ErrNoAlarmEventPublisher)
	require.Equal(t, 1, observations)
	api.SetAlarmPublisher(alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error {
		require.Equal(t, 2, observations, "observe before publishing, outside the singleton pass")
		return nil
	}))
	require.NoError(t, api.DrainAlarmEventsLocked(context.Background()))
	require.Equal(t, 2, observations)
	// Post-commit passes must not perform the aggregate reads.
	require.NoError(t, api.drainAlarmEventsLocked(context.Background(), false))
	require.Equal(t, 2, observations)
}

func TestAlarmOutboxQuarantineDeadLetterOnceAfterCommit(t *testing.T) {
	for _, failLetter := range []bool{false, true} {
		t.Run(fmt.Sprint(failLetter), func(t *testing.T) {
			api, ctx := alarmOutboxRig(t)
			payloads := []string{`not-json`, `{"AlarmToken":"bad","EventType":42}`, `{}`, `{"AlarmToken":"alarm","EventType":"RAISED"}`}
			var rows []PendingAlarmEvent
			for _, payload := range payloads {
				row := PendingAlarmEvent{TenantId: "acme", Payload: []byte(payload), CreatedAt: tsec(10)}
				require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
				rows = append(rows, row)
			}
			longError := fmt.Errorf("%s: %w", strings.Repeat("\u754c", 3000), ErrAlarmEventUnpublishable)
			api.SetAlarmPublisher(alarmOutboxPublisher(func(context.Context, *AlarmStateChangeEvent, string) error { return longError }))
			letters := 0
			api.SetAlarmEventDeadLetter(func(letterCtx context.Context, reference string, payload []byte, cause error) error {
				i := letters
				letters++
				require.False(t, core.IsSystemContext(letterCtx))
				tenant, _ := core.TenantFromContext(letterCtx)
				require.Equal(t, "acme", tenant)
				expected := fmt.Sprintf("pending-alarm-event-acme-%d", rows[i].ID)
				if i == 3 {
					expected = "alarm"
				}
				require.Equal(t, expected, reference)
				require.Equal(t, []byte(payloads[i]), payload)
				// A fresh DB read sees quarantine before the callback, even if indexing fails.
				var stored PendingAlarmEvent
				require.NoError(t, api.RDB.DB(ctx).First(&stored, rows[i].ID).Error)
				require.NotNil(t, stored.QuarantinedAt)
				require.Equal(t, *stored.LastError, cause.Error())
				if i == 3 {
					require.True(t, utf8.ValidString(*stored.LastError))
					require.Equal(t, 2048, utf8.RuneCountInString(*stored.LastError))
					require.Equal(t, strings.Repeat("\u754c", 2048), *stored.LastError)
				}
				if failLetter {
					return errors.New("dead-letter broker down")
				}
				return nil
			})
			res, err := api.DrainAlarmEvents(context.Background(), 10)
			require.NoError(t, err, "a dead-letter failure must not fail the relay")
			require.Equal(t, 4, res.Quarantined)
			require.Equal(t, 4, letters)
			_, err = api.DrainAlarmEvents(context.Background(), 10)
			require.NoError(t, err)
			require.Equal(t, 4, letters, "quarantined rows are never retried")
		})
	}
}
