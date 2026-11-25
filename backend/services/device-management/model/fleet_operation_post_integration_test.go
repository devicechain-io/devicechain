// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// postPG is a RUNNING operation over two configured devices on a real server, with the real guard.
type postPG struct {
	api    *Api
	ctx    context.Context
	tenant string
	op     *FleetOperation
	lines  []FleetOperationLine
	lease  FleetLease
	devs   []*Device
}

func newPostPG(t *testing.T, value string) *postPG {
	t.Helper()
	openTheEngine(t)
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("fleetpost%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 3*time.Minute)
	t.Cleanup(cancel)
	d1 := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	d2, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev2", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	op, err := api.CreateFleetOperation(ctx, &FleetOperationCreateRequest{Token: "pp", Name: "n", Kind: FleetKindConfigSet,
		DeviceTokens: []string{d1.Token, d2.Token}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &value}}, "creator")
	require.NoError(t, err)
	op, err = api.StartFleetOperation(ctx, "pp", op.ContractDigest, alice)
	require.NoError(t, err)
	p := &postPG{api: api, ctx: ctx, tenant: tenant, op: op, devs: []*Device{d1, d2},
		lease: FleetLease{Epoch: op.ExecutionEpoch, Grantee: alice.Name, Deadline: time.Now().Add(2 * time.Minute)}}
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).Order("ordinal").Find(&p.lines).Error)
	require.Len(t, p.lines, 2)
	return p
}

func (p *postPG) posting(t *testing.T, i int) string {
	t.Helper()
	var l FleetOperationLine
	require.NoError(t, p.api.RDB.DB(p.ctx).First(&l, p.lines[i].ID).Error)
	return l.Posting
}

func (p *postPG) attrCount(t *testing.T, devIdx int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(&EntityAttribute{}).
		Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", p.devs[devIdx].ID).Count(&n).Error)
	return n
}

func TestTheTryLockTakesTheSameKeyAsTheBlockingLock(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	const id = 424242
	holder := db.Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, holder.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, id).Error)

	var got bool
	require.NoError(t, db.Raw(`SELECT "device-management".dc_entity_attr_try_lock('device', ?)`, id).Scan(&got).Error)
	require.False(t, got, "a lock held through the blocking function must be refused to the try function")
	// Another device's lock is free.
	require.NoError(t, db.Raw(`SELECT "device-management".dc_entity_attr_try_lock('device', ?)`, id+1).Scan(&got).Error)
	require.True(t, got)
	require.NoError(t, holder.Rollback().Error)

	// And the other way round: a lock taken through the try function blocks the blocking one.
	taker := db.Begin()
	require.NoError(t, taker.Error)
	defer taker.Rollback()
	require.NoError(t, taker.Raw(`SELECT "device-management".dc_entity_attr_try_lock('device', ?)`, id).Scan(&got).Error)
	require.True(t, got)
	waiter := db.Begin()
	require.NoError(t, waiter.Error)
	defer waiter.Rollback()
	require.NoError(t, waiter.Exec(`SELECT set_config('lock_timeout', '300ms', true)`).Error)
	err := waiter.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, id).Error
	require.Error(t, err)
	require.Equal(t, "55P03", attributeSQLState(err))
}

func TestConcurrentPostersOfOneLineApplyTheEffectOnce(t *testing.T) {
	p := newPostPG(t, "v1")
	var effects atomic.Int32
	fleetEffectHook = func() { effects.Add(1) }
	t.Cleanup(func() { fleetEffectHook = nil })

	const posters = 8
	var wg sync.WaitGroup
	results := make([]FleetPostResult, posters)
	errs := make([]error, posters)
	for i := 0; i < posters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
		}(i)
	}
	wg.Wait()
	posted := 0
	for i := range results {
		require.NoError(t, errs[i])
		switch results[i].Outcome {
		case FleetPosted:
			posted++
		case FleetPostLostRace, FleetPostBusy:
		default:
			t.Fatalf("poster %d: unexpected outcome %v", i, results[i])
		}
	}
	require.Equal(t, 1, posted, "exactly one poster committed")
	require.Equal(t, int32(1), effects.Load(), "the effect-invocation log shows one write that committed; losers rolled back before or after it, never both writing")
	require.Equal(t, "POSTED", p.posting(t, 0))
	require.Equal(t, int64(1), p.attrCount(t, 0))
	var revs int64
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", p.devs[0].ID).Count(&revs).Error)
	require.Equal(t, int64(1), revs, "one revision for one effect")
}

func TestAPosterNeverQueuesAheadOfAnInteractiveWriter(t *testing.T) {
	p := newPostPG(t, "v1")
	// An interactive writer holds the device's lock.
	holder := p.api.RDB.DB(p.ctx).Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, holder.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, p.devs[0].ID).Error)

	started := time.Now()
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPostBusy, r.Outcome)
	require.Less(t, time.Since(started), 1500*time.Millisecond, "a busy device answers at once; the poster does not wait")
	require.Equal(t, "PLANNED", p.posting(t, 0))
	require.Zero(t, p.attrCount(t, 0))

	// The other device is not held up, and once the writer is done the same line posts.
	r, err = p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[1].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
	require.NoError(t, holder.Rollback().Error)
	r, err = p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
}

func TestAPauseWaitsForTheLinePostInFlightAndNothingPostsAfterIt(t *testing.T) {
	p := newPostPG(t, "v1")
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fleetEffectHook = func() { once.Do(func() { close(inside) }); <-release }
	t.Cleanup(func() { fleetEffectHook = nil })

	posted := make(chan FleetPostResult, 1)
	go func() {
		r, _ := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
		posted <- r
	}()
	<-inside // the poster holds the fence shared and is about to write

	paused := make(chan error, 1)
	go func() {
		_, err := p.api.PauseFleetOperation(p.ctx, "pp", alice.Name)
		paused <- err
	}()
	select {
	case err := <-paused:
		t.Fatalf("the pause returned while a post was in flight: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	fleetEffectHook = nil
	close(release)
	require.Equal(t, FleetPosted, (<-posted).Outcome, "the post that was in flight committed under the old state")
	require.NoError(t, <-paused)

	// After the pause has returned, no line is applied or refused.
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[1].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPostFenceRefused, r.Outcome)
	require.Equal(t, "PLANNED", p.posting(t, 1))
	require.Zero(t, p.attrCount(t, 1))
}

// With the real activation check and the real trigger in place (no test doubles), a post goes through: the
// poster marks its transaction as the writer protocol requires, so the guard passes it.
func TestAPostPassesTheRealGuardAndTrigger(t *testing.T) {
	p := newPostPG(t, "v1")
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
	require.Equal(t, int64(1), p.attrCount(t, 0))
}

func TestTwoPostersOnDifferentLinesRunAtTheSameTime(t *testing.T) {
	p := newPostPG(t, "v1")
	inside, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fleetEffectHook = func() {
		if calls.Add(1) == 1 {
			close(inside)
			<-release
		}
	}
	t.Cleanup(func() { fleetEffectHook = nil })
	first := make(chan FleetPostResult, 1)
	go func() {
		r, _ := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
		first <- r
	}()
	<-inside // the first poster holds the fence shared, mid-effect
	started := time.Now()
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[1].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
	require.Less(t, time.Since(started), 5*time.Second, "posters share the fence; one in flight does not hold the next up")
	close(release)
	require.Equal(t, FleetPosted, (<-first).Outcome)
}

func TestTheTryLockMarksTheTransactionLikeTheBlockingLock(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("trylock%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		got, err := tryLockDeviceAttributes(ctx, tx, 777001)
		require.NoError(t, err)
		require.True(t, got)
		// The database guard passes a write only inside a marked transaction: the try-lock must have marked it.
		return tx.Exec(`INSERT INTO "device-management".entity_attributes (created_at, updated_at, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
 VALUES (now(), now(), ?, 'device', 777001, 'SHARED', 'trylock-marker', 'STRING', 'v', now())`, tenant).Error
	})
	require.NoError(t, err, "a transaction that took the lock by try is a protocol writer")
	cleanup := api.runDeviceAttributeTx(ctx, "delete", func(tx *gorm.DB) error {
		if _, err := tryLockDeviceAttributes(ctx, tx, 777001); err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM "device-management".entity_attributes WHERE entity_id = 777001 AND attr_key = 'trylock-marker'`).Error
	})
	require.NoError(t, cleanup)
}

// Three replicas driving one operation at once (nothing is elected): every line is applied exactly once.
func TestThreeDriversOfOneOperationApplyEveryLineOnce(t *testing.T) {
	openTheEngine(t)
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("fleetdrive%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 3*time.Minute)
	t.Cleanup(cancel)
	d0 := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	tokens := []string{d0.Token}
	const devices = 12
	for i := 1; i < devices; i++ {
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("dd%d", i), DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		tokens = append(tokens, d.Token)
	}
	rate := int32(50)
	v := "v1"
	op, err := api.CreateFleetOperation(ctx, &FleetOperationCreateRequest{Token: "drv3", Name: "n", Kind: FleetKindConfigSet, DeviceTokens: tokens,
		Safety: FleetSafetyRequest{MaxPostsPerSecond: &rate}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v}}, "creator")
	require.NoError(t, err)
	_, err = api.StartFleetOperation(ctx, "drv3", op.ContractDigest, alice)
	require.NoError(t, err)
	api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
	var effects atomic.Int32
	fleetEffectHook = func() { effects.Add(1) }
	t.Cleanup(func() { fleetEffectHook = nil })

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			deadline := time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) {
				cur, err := api.fleetOperationByToken(ctx, "drv3")
				if err != nil {
					errs[i] = err
					return
				}
				if cur.Dispatch != FleetDispatchRunning {
					return
				}
				if _, err := api.DriveFleetOperation(ctx, "drv3", DefaultFleetCaps); err != nil {
					errs[i] = err
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			errs[i] = fmt.Errorf("driver %d never saw the operation finish", i)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	final, err := api.fleetOperationByToken(ctx, "drv3")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchDispatched, final.Dispatch)
	progress, err := api.FleetOperationProgressOf(ctx, final)
	require.NoError(t, err)
	require.Equal(t, int32(devices), progress.Posted)
	require.Zero(t, progress.Planned)
	require.Equal(t, int32(devices), effects.Load(), "each line's effect ran exactly once, whichever replica reached it")
	var attrs, revs int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).Where("scope = 'SHARED' AND attr_key = 'k'").Count(&attrs).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationRevision{}).Count(&revs).Error)
	require.Equal(t, int64(devices), attrs)
	require.Equal(t, int64(devices), revs)
}

// A poster must read the operation's state AFTER it holds the fence. A pause that commits while a poster is
// waiting for the fence must be seen by it: reading first would let the post proceed under the old state.
func TestAPosterWaitingForTheFenceSeesThePauseThatCommittedFirst(t *testing.T) {
	p := newPostPG(t, "v1")
	ctlr := p.api.RDB.DB(p.ctx).Begin()
	require.NoError(t, ctlr.Error)
	defer ctlr.Rollback()
	require.NoError(t, fleetLock(ctlr, fleetOpKey(p.tenant, p.op.ID), false))

	done := make(chan FleetPostResult, 1)
	go func() {
		r, _ := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
		done <- r
	}()
	time.Sleep(500 * time.Millisecond) // the poster is now waiting for the fence
	require.NoError(t, ctlr.Model(&FleetOperation{ID: p.op.ID}).Updates(map[string]any{
		"dispatch": FleetDispatchPaused, "execution_epoch": p.op.ExecutionEpoch + 1}).Error)
	require.NoError(t, ctlr.Commit().Error)
	select {
	case r := <-done:
		require.Equal(t, FleetPostFenceRefused, r.Outcome)
	case <-time.After(15 * time.Second):
		t.Fatal("the poster never finished")
	}
	require.Equal(t, "PLANNED", p.posting(t, 0))
	require.Zero(t, p.attrCount(t, 0))
}

// The same for a decided refusal: it takes the fence too, so it cannot land after a pause that committed first.
func TestARefusalWaitingForTheFenceSeesThePauseThatCommittedFirst(t *testing.T) {
	p := newPostPG(t, "v1")
	ctlr := p.api.RDB.DB(p.ctx).Begin()
	require.NoError(t, ctlr.Error)
	defer ctlr.Rollback()
	require.NoError(t, fleetLock(ctlr, fleetOpKey(p.tenant, p.op.ID), false))

	done := make(chan FleetPostResult, 1)
	go func() {
		r, _ := p.api.recordFleetRefusal(p.ctx, p.tenant, p.op.ID, p.lines[0].ID, p.lease, fleetDecision{FleetPostingRefused, "X", "d"})
		done <- r
	}()
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, ctlr.Model(&FleetOperation{ID: p.op.ID}).Updates(map[string]any{
		"dispatch": FleetDispatchPaused, "execution_epoch": p.op.ExecutionEpoch + 1}).Error)
	require.NoError(t, ctlr.Commit().Error)
	select {
	case r := <-done:
		require.Equal(t, FleetPostFenceRefused, r.Outcome)
	case <-time.After(15 * time.Second):
		t.Fatal("the refusal never finished")
	}
	require.Equal(t, "PLANNED", p.posting(t, 0))
}
