// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Group rollouts on a real PostgreSQL server, where the rollout row lock exists. SQLite (the unit
// suites) has no row locks, so what the lock buys — the cap and pause exact with several replicas
// stepping at once — is only observable here. Run against a throwaway server (CI's
// hack/integration-tests.sh does):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Rollout -v
package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
)

// pgRollouts is one area database with two replicas over it, and a tenant of its own per test so a
// row a previous run (or a concurrent package) left cannot decide this one.
type pgRollouts struct {
	mgr     *rdb.RdbManager
	store   blob.Store
	devices *syncDevices
	groups  *fakeGroups
	desired *recorder
	tenant  string
}

func newPGRollouts(t *testing.T, n int) *pgRollouts {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "updaterolloutit"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "update-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	store, err := blob.New(ctx, blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, instance)
	require.NoError(t, err)
	e := &pgRollouts{mgr: mgr, store: store, groups: &fakeGroups{}, desired: &recorder{},
		devices: &syncDevices{profiles: map[string]string{}, disabled: map[string]bool{}},
		tenant:  fmt.Sprintf("ro%d", time.Now().UnixNano())}
	var fleet []string
	for i := range n {
		d := fmt.Sprintf("dev-%d", i)
		fleet = append(fleet, d)
		e.devices.profiles[e.tenant+"/"+d] = "gateway"
	}
	e.groups.set(e.tenant, "fleet", fleet...)

	// The artifact, through the real completion path.
	a := e.replica()
	a.Profiles = &fakeProfiles{known: map[string][]string{e.tenant: {"gateway"}}}
	content := []byte("firmware two")
	req := validRequest("fw-2", content)
	art, err := a.CreateArtifact(e.ctx(), req)
	require.NoError(t, err)
	ref, err := store.Put(e.ctx(), blobKey(e.tenant, "fw-2"), bytes.NewReader(content), blobPutOpts(int64(len(content))))
	require.NoError(t, err)
	require.NoError(t, a.CompleteUpload(e.ctx(), art, ref))
	return e
}

func (e *pgRollouts) ctx() context.Context { return tenantCtx(e.tenant) }

// replica is one service replica: its own Api over the shared database.
func (e *pgRollouts) replica() *Api {
	a := NewApi(e.mgr, e.store, Limits{MaxArtifactBytes: 1 << 20, ConfiguredMaxArtifactBytes: 1 << 20, MaxTenantArtifactBytes: 1 << 30})
	a.Devices, a.Groups, a.Desired = e.devices, e.groups, e.desired
	a.Policy, a.AllowUnverified = testPolicy, true
	return a
}

func (e *pgRollouts) start(t *testing.T, a *Api, token string, maxInFlight int) uint {
	t.Helper()
	_, err := a.CreateRollout(e.ctx(), RolloutCreateRequest{Token: token, ArtifactToken: "fw-2", GroupToken: "fleet",
		MaxInFlight: maxInFlight})
	require.NoError(t, err)
	v, err := a.StartRollout(e.ctx(), token)
	require.NoError(t, err)
	return v.Rollout.ID
}

func (e *pgRollouts) progress(t *testing.T, a *Api, token string) RolloutProgress {
	t.Helper()
	v, err := a.RolloutByToken(e.ctx(), token)
	require.NoError(t, err)
	return v.Progress
}

// inFlightAttempts are the attempts of the tenant that have not finished.
func (e *pgRollouts) inFlightAttempts(t *testing.T, a *Api) []UpdateAttempt {
	t.Helper()
	var rows []UpdateAttempt
	require.NoError(t, a.RDB.DB(e.ctx()).Where("state = ?", string(ota.StateQueued)).Find(&rows).Error)
	return rows
}

// barrier lets n callers through together, or each one alone after timeout. timedOut reports
// whether any caller gave up waiting, i.e. whether the callers did NOT meet.
func barrier(n int, timeout time.Duration) (wait func(), timedOut *atomic.Bool) {
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	timedOut = new(atomic.Bool)
	return func() {
		mu.Lock()
		arrived++
		if arrived == n {
			close(release)
		}
		mu.Unlock()
		select {
		case <-release:
		case <-time.After(timeout):
			timedOut.Store(true)
		}
	}, timedOut
}

// 🔴 THE CAP IS EXACT WITH TWO REPLICAS AT ONCE, and the control shows the lock is why. Both
// replicas are held after taking their in-flight count and released together: with the lock the
// second never gets there (SKIP LOCKED), so exactly the cap is dispatched; without it both count
// zero in flight and both fill the cap.
func TestTwoReplicasSteppingOneRolloutNeverExceedTheCapOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noLock bool
		want   int
	}{{"with the lock", false, 2}, {"negative control: without the lock", true, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPGRollouts(t, 10)
			a, b := e.replica(), e.replica()
			_, err := a.CreateRollout(e.ctx(), RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2})
			require.NoError(t, err)
			// Start with the driver not yet holding a step of its own: the inline step would fill the cap.
			// Without the lock BOTH replicas must reach the hook, so the control waits for the other
			// one as long as a loaded CI runner could possibly take: the timeout is only the backstop
			// that turns a replica that never arrives into a failure rather than a hang, and the
			// meeting is asserted below. A short wait here is what made the control flaky — the
			// replica that arrived first gave up, ran alone, and the cap was never exceeded. With
			// the lock the second replica is skipped and never reaches the hook, so the first waits
			// out the (short) timeout alone, which is the intended interleaving.
			wait := 2 * time.Second
			if tc.noLock {
				wait = 60 * time.Second
			}
			gate, gaveUp := barrier(2, wait)
			for _, r := range []*Api{a, b} {
				r.noRolloutLock = tc.noLock
			}
			e.groups.mu.Lock()
			saved := e.groups.members[e.tenant+"/fleet"]
			e.groups.mu.Unlock()
			a.Devices = &syncDevices{err: errors.New("held")} // the start's inline step looks nobody up
			v, err := a.StartRollout(e.ctx(), "r1")
			require.NoError(t, err)
			require.Equal(t, len(saved), v.Progress.Pending)
			a.Devices = e.devices
			// Armed only now: the start's inline step passes through the same hook, and an arrival
			// there would count toward the barrier and let the first real step through alone.
			for _, r := range []*Api{a, b} {
				r.beforeDispatch = gate
			}

			var wg sync.WaitGroup
			var dispatched atomic.Int64
			for _, r := range []*Api{a, b} {
				wg.Add(1)
				go func(r *Api) {
					defer wg.Done()
					res, err := r.StepRollout(e.ctx(), v.Rollout.ID)
					require.NoError(t, err)
					dispatched.Add(int64(res.Dispatched))
				}(r)
			}
			wg.Wait()
			if tc.noLock {
				require.False(t, gaveUp.Load(), "the replicas never met at the hook, so the control did not exercise the race")
			}
			require.Equal(t, int64(tc.want), dispatched.Load())
			require.Equal(t, tc.want, e.progress(t, a, "r1").Occupied())
		})
	}
}

// Two replicas and a device simulator run a whole rollout to completion; an operator assigns the
// same devices by hand meanwhile. At no observation does the rollout hold more than its cap, no
// step deadlocks, and every device ends with one assignment of the artifact.
func TestTwoReplicasDriveARolloutToCompletionWithinTheCapOnPostgres(t *testing.T) {
	const n, maxInFlight = 40, 3
	e := newPGRollouts(t, n)
	a, b := e.replica(), e.replica()
	id := e.start(t, a, "r1", maxInFlight)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var over atomic.Int64
	var wg sync.WaitGroup
	done := func() bool {
		v, err := a.RolloutByToken(e.ctx(), "r1")
		return err == nil && v.Rollout.State == RolloutCompleted
	}
	isDeadlock := func(err error) bool { return err != nil && strings.Contains(err.Error(), "40P01") }
	for _, r := range []*Api{a, b} {
		wg.Add(1)
		go func(r *Api) {
			defer wg.Done()
			for ctx.Err() == nil && !done() {
				_, err := r.StepRollout(e.ctx(), id)
				require.False(t, isDeadlock(err), "a step deadlocked: %v", err)
				require.NoError(t, err)
				if p := e.progress(t, r, "r1"); p.Occupied() > maxInFlight {
					over.Add(1)
				}
			}
		}(r)
	}
	wg.Add(2)
	go func() { // the devices: finish whatever is in flight, a few at a time
		defer wg.Done()
		for ctx.Err() == nil && !done() {
			for _, at := range e.inFlightAttempts(t, a) {
				if rand.IntN(2) == 0 {
					require.NoError(t, a.RDB.DB(e.ctx()).Model(&UpdateAttempt{}).Where("id = ?", at.ID).
						Update("state", string(ota.StateUpdated)).Error)
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	go func() { // an operator assigning the same artifact to the same devices by hand
		defer wg.Done()
		for i := n - 1; i >= 0 && ctx.Err() == nil; i-- {
			_, err := b.AssignArtifact(e.ctx(), fmt.Sprintf("dev-%d", i), "fw-2")
			require.False(t, isDeadlock(err), "a hand assign deadlocked: %v", err)
		}
	}()
	wg.Wait()
	require.NoError(t, ctx.Err(), "the rollout did not complete")
	require.Zero(t, over.Load(), "an observation saw more devices in flight than the cap")
	p := e.progress(t, a, "r1")
	require.Equal(t, n, p.Total)
	require.Equal(t, n, p.Succeeded+p.Skipped, "every device was either updated or refused by name")
	for i := range n {
		var current int64
		require.NoError(t, a.RDB.DB(e.ctx()).Model(&UpdateAssignment{}).
			Where("device_token = ? AND superseded_at IS NULL", fmt.Sprintf("dev-%d", i)).Count(&current).Error)
		require.Equal(t, int64(1), current)
	}
}

// A pause that arrives while a step is dispatching waits for that step, and every later step sees
// it. The control: without the lock the pause returns while the step is still dispatching.
func TestAPauseDuringAStepWaitsForItAndStopsTheNextOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noLock bool
	}{{"with the lock", false}, {"negative control: without the lock", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPGRollouts(t, 6)
			a, b := e.replica(), e.replica()
			_, err := a.CreateRollout(e.ctx(), RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2})
			require.NoError(t, err)
			a.Devices = &syncDevices{err: errors.New("held")}
			v, err := a.StartRollout(e.ctx(), "r1")
			require.NoError(t, err)
			a.Devices = e.devices
			a.noRolloutLock, b.noRolloutLock = tc.noLock, tc.noLock

			held, release := make(chan struct{}), make(chan struct{})
			a.beforeDispatch = func() { close(held); <-release }
			stepped := make(chan RolloutStepResult, 1)
			go func() {
				res, err := a.StepRollout(e.ctx(), v.Rollout.ID)
				require.NoError(t, err)
				stepped <- res
			}()
			<-held
			paused := make(chan error, 1)
			go func() { _, err := b.PauseRollout(e.ctx(), "r1"); paused <- err }()
			select {
			case err := <-paused:
				require.True(t, tc.noLock, "the pause returned while a step held the rollout")
				require.NoError(t, err)
				close(release)
				<-stepped
				return
			case <-time.After(500 * time.Millisecond):
				require.False(t, tc.noLock, "without the lock the pause should not have waited")
			}
			close(release)
			require.Equal(t, 2, (<-stepped).Dispatched)
			require.NoError(t, <-paused)

			// Free both slots: the next step still dispatches nothing.
			for _, at := range e.inFlightAttempts(t, a) {
				require.NoError(t, a.RDB.DB(e.ctx()).Model(&UpdateAttempt{}).Where("id = ?", at.ID).
					Update("state", string(ota.StateUpdated)).Error)
			}
			a.beforeDispatch = nil
			res, err := b.StepRollout(e.ctx(), v.Rollout.ID)
			require.NoError(t, err)
			require.Zero(t, res.Dispatched)
			require.Equal(t, RolloutPaused, e.progressState(t, a, "r1"))
		})
	}
}

func (e *pgRollouts) progressState(t *testing.T, a *Api, token string) string {
	t.Helper()
	v, err := a.RolloutByToken(e.ctx(), token)
	require.NoError(t, err)
	return v.Rollout.State
}

// The tenant purge reaches both rollout tables: it derives its table set from the catalog, so the
// tables are swept because they carry the tenant column, and this asserts that they do.
func TestTheTenantPurgeSweepsRolloutsAndTheirTargetsOnPostgres(t *testing.T) {
	e := newPGRollouts(t, 3)
	a := e.replica()
	e.start(t, a, "r1", 1)

	sys := core.WithSystemContext(context.Background())
	count := func(table string) int64 {
		var n int64
		require.NoError(t, e.mgr.Database.WithContext(sys).Table(table).Where("tenant_id = ?", e.tenant).Count(&n).Error)
		return n
	}
	require.Equal(t, int64(1), count("update_rollouts"))
	require.Equal(t, int64(3), count("update_rollout_targets"))

	plan, err := tenantpurge.Classify(sys, e.mgr.Database)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = tenantpurge.Sweep(sys, e.mgr.Database, plan, e.tenant, now, nil)
	require.NoError(t, err)
	require.Zero(t, count("update_rollouts"))
	require.Zero(t, count("update_rollout_targets"))
	residue, err := tenantpurge.Residue(sys, e.mgr.Database, plan, e.tenant, now)
	require.NoError(t, err)
	require.Zero(t, residue.Rows)
}

// A dispatch that loses the device to a concurrent assignment of the SAME artifact (an operator
// assigning it by hand while the step runs) adopts the winner's assignment: the device is on its
// way to the rollout's target, and reporting it SKIPPED as busy would drop its outcome from the
// rollout. The hand assign is held open until the step is blocked behind it on the unique index.
func TestADispatchThatLosesToAHandAssignOfTheSameArtifactAdoptsItOnPostgres(t *testing.T) {
	e := newPGRollouts(t, 2)
	a := e.replica()
	_, err := a.CreateRollout(e.ctx(), RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 1})
	require.NoError(t, err)
	a.Devices = &syncDevices{err: errors.New("held")} // the start's inline step dispatches nobody
	v, err := a.StartRollout(e.ctx(), "r1")
	require.NoError(t, err)
	a.Devices = e.devices

	artifact, err := a.ArtifactByToken(e.ctx(), "fw-2")
	require.NoError(t, err)
	held := a.RDB.DB(e.ctx()).Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	hand, err := a.assignInTx(e.ctx(), held, "dev-0", artifact, nil)
	require.NoError(t, err)

	reached := make(chan struct{})
	a.beforeDispatch = func() { close(reached) }
	stepped := make(chan RolloutStepResult, 1)
	go func() {
		res, err := a.StepRollout(e.ctx(), v.Rollout.ID)
		require.NoError(t, err)
		stepped <- res
	}()
	<-reached
	select {
	case <-stepped:
		t.Fatal("the step finished while the hand assign was uncommitted: the race was not exercised")
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, held.Commit().Error)
	res := <-stepped
	require.Equal(t, 1, res.Dispatched)
	require.Zero(t, res.Skipped)

	page, err := a.RolloutTargets(e.ctx(), "r1", RolloutTargetCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Equal(t, "dev-0", page.Results[0].Target.DeviceToken)
	require.Equal(t, OutcomeInFlight, page.Results[0].Outcome, "adopted, not skipped as busy")
	require.Equal(t, hand.Assignment.Token, page.Results[0].AssignmentToken)
}
