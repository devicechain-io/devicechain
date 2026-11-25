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

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Immutable standing answers are safe to share between concurrent replicas.
type driverStanding struct{}

func (driverStanding) PrincipalStanding(context.Context, []string, []string) (map[string]bool, error) {
	return map[string]bool{alice.Name: true}, nil
}

// Loop discovery is deliberately cross-tenant. Give these tests their own instance database
// so fixtures left by unrelated integration tests cannot enter the effect-invocation log.
func driverPGManager(t *testing.T) *rdb.RdbManager {
	t.Helper()
	base := newPostgresRdbManager(t)
	instance := fmt.Sprintf("dcloop%d", time.Now().UnixNano())
	cfg := base.InstanceConfig.Configuration
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), cfg["hostname"].(string), cfg["port"].(int),
		cfg["username"].(string), cfg["password"].(string), instance, ""))
	mgr := &rdb.RdbManager{Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "device-management"},
		Migrations: schema.Migrations, InstanceConfig: base.InstanceConfig, MicroserviceConfig: base.MicroserviceConfig}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		pool, err := mgr.Database.DB()
		require.NoError(t, err)
		require.NoError(t, pool.Close())
		require.NoError(t, base.Database.Exec(`DROP DATABASE "`+instance+`" WITH (FORCE)`).Error)
	})
	return mgr
}

func driverPGOperation(t *testing.T, api *Api, tenant string, n int) (context.Context, *FleetOperation) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	d := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	tokens := []string{d.Token}
	for i := 1; i < n; i++ {
		dev, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("loop%d", i), DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		tokens = append(tokens, dev.Token)
	}
	value, rate := "loop-value", int32(50)
	op, err := api.CreateFleetOperation(ctx, &FleetOperationCreateRequest{
		Token:        "loop",
		Name:         "loop",
		Kind:         FleetKindConfigSet,
		DeviceTokens: tokens,
		ConfigSet:    &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &value},
		Safety:       FleetSafetyRequest{MaxPostsPerSecond: &rate},
	}, "creator")
	require.NoError(t, err)
	op, err = api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
	require.NoError(t, err)
	return ctx, op
}

func runDriverReplicas(t *testing.T, apis []*Api, ctx context.Context, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, api := range apis {
		wg.Add(1)
		go func(api *Api) {
			defer wg.Done()
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				if err := api.RunFleetDriver(ctx); err != nil && ctx.Err() == nil {
					t.Errorf("driver pass: %v", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}(api)
	}
	defer wg.Wait()
	defer cancel()
	require.Eventually(t, done, 4*time.Minute, 100*time.Millisecond)
}

type driverReplicaKey struct{}

func TestFleetDriverPostgresThreeReplicasExactlyOnce2000(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(fmt.Sprintf("crash=%t", crash), func(t *testing.T) {
			openTheEngine(t)
			mgr := driverPGManager(t)
			apis := []*Api{NewApi(mgr), NewApi(mgr), NewApi(mgr)}
			for _, api := range apis {
				api.PrincipalStanding = driverStanding{}
			}
			tenant := fmt.Sprintf("driver%d", time.Now().UnixNano())
			tenantCtx, op := driverPGOperation(t, apis[0], tenant, 2000)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			killedCtx, kill := context.WithCancel(context.WithValue(ctx, driverReplicaKey{}, true))
			defer kill()
			var killed atomic.Bool
			crashed := make(chan struct{})
			var invocations atomic.Int64
			fleetEffectHook = func() { invocations.Add(1) }
			fleetPostHook = func(stage string, tx *gorm.DB) {
				if crash && stage == "claim" && tx.Statement.Context.Value(driverReplicaKey{}) == true && killed.CompareAndSwap(false, true) {
					kill()
					close(crashed)
				}
			}
			defer func() { fleetEffectHook, fleetPostHook = nil, nil }()
			// The cancelled replica has its own loop lifetime. The other two continue after its death.
			var victim sync.WaitGroup
			victim.Add(1)
			go func() {
				defer victim.Done()
				for killedCtx.Err() == nil {
					_ = apis[0].RunFleetDriver(killedCtx)
					select {
					case <-killedCtx.Done():
					case <-time.After(time.Second):
					}
				}
			}()
			defer func() { kill(); victim.Wait() }()
			if crash {
				select {
				case <-crashed:
				case <-time.After(30 * time.Second):
					t.Fatal("victim never reached the claim hook")
				}
			}
			runDriverReplicas(t, apis[1:], ctx, func() bool {
				var cur FleetOperation
				return mgr.DB(tenantCtx).First(&cur, op.ID).Error == nil && cur.Dispatch == FleetDispatchDispatched
			})
			kill()
			victim.Wait()
			if crash {
				require.True(t, killed.Load(), "a replica must actually die at claim")
			} else {
				require.Equal(t, int64(2000), invocations.Load(), "effect-invocation log: exactly one effect per line")
			}
			var posted int64
			require.NoError(t, mgr.DB(tenantCtx).Model(&FleetOperationLine{}).
				Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPosted).Count(&posted).Error)
			require.Equal(t, int64(2000), posted)
			// Committed effect log: one revision per targeted device, including after the rollback at claim.
			var counts []int64
			require.NoError(t, mgr.DB(tenantCtx).Model(&DeviceConfigurationRevision{}).
				Select("count(*)").Where("device_id IN (?)", mgr.DB(tenantCtx).Model(&FleetOperationLine{}).
				Select("target_id").Where("operation_id = ?", op.ID)).Group("device_id").Scan(&counts).Error)
			require.Len(t, counts, 2000)
			for _, count := range counts {
				require.Equal(t, int64(1), count)
			}
		})
	}
}

func TestFleetDriverPostgresTenantBFinishesWithinTwiceSolo(t *testing.T) {
	openTheEngine(t)
	mgr := driverPGManager(t)
	apis := []*Api{NewApi(mgr), NewApi(mgr)}
	for _, api := range apis {
		api.PrincipalStanding = driverStanding{}
	}
	measure := func(mixed bool) time.Duration {
		suffix := time.Now().UnixNano()
		if mixed {
			driverPGOperation(t, apis[0], fmt.Sprintf("a%d", suffix), 2000)
		}
		bctx, b := driverPGOperation(t, apis[0], fmt.Sprintf("b%d", suffix), 50)
		start := time.Now()
		runDriverReplicas(t, apis, context.Background(), func() bool {
			var cur FleetOperation
			return mgr.DB(bctx).First(&cur, b.ID).Error == nil && cur.Dispatch == FleetDispatchDispatched
		})
		return time.Since(start)
	}
	solo := measure(false)
	mixed := measure(true)
	require.LessOrEqual(t, mixed, 2*solo)
}

func TestFleetDriverPostgresDependencyPauseUsesDatabaseClock(t *testing.T) {
	p := newPostPG(t, "v1")
	p.api.PrincipalStanding = driverStanding{}
	// Deliberately wrong local clock: shared-time decisions must ignore it.
	previous := fleetClock
	fleetClock = func() time.Time { return time.Now().Add(-72 * time.Hour) }
	defer func() { fleetClock = previous }()
	db := p.api.RDB.DB(p.ctx)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", p.op.ID).
		Updates(map[string]any{"attempts": 20, "last_error_code": "CONNECTION_LOST", "next_attempt_at": gorm.Expr("clock_timestamp() + interval '1 hour'")}).Error)
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
		Updates(map[string]any{"hold_code": "POST_ERROR", "hold_since": gorm.Expr("clock_timestamp() - interval '24 hours'")}).Error)
	r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, "DEPENDENCY_UNAVAILABLE", r.Paused)
}

// The exactly-once oracle above counts effect ATTEMPTS, and on a 2,000-line run the replicas rarely
// work the same line at once: whichever takes a second's permits posts that window alone. This test
// forces them onto the same lines, and judges by what COMMITTED.
//
//   - Overlap: each effect is slowed, so one replica's window of posts is still in flight when the
//     next window's permits go to another replica, which selects the same still-PLANNED head lines.
//     A line left with DEVICE_BUSY proves a second replica reached a line another was posting.
//   - Oracle: a test-only AFTER trigger appends one row per committed generation bump. A rolled-back
//     effect leaves no row, so the log is exactly the committed effects: one per line, never two.
//
// Negative control (run by hand when this oracle or the claim changes): drop the PLANNED re-check under
// the device lock and the claim's `posting = PLANNED` predicate; this test then fails on a device with
// two committed effects.
func TestFleetDriverPostgresContendedReplicasCommitOneEffectPerLine(t *testing.T) {
	const lines = 200
	openTheEngine(t)
	mgr := driverPGManager(t)
	apis := []*Api{NewApi(mgr), NewApi(mgr), NewApi(mgr)}
	for _, api := range apis {
		api.PrincipalStanding = driverStanding{}
	}
	tenant := fmt.Sprintf("contend%d", time.Now().UnixNano())
	tenantCtx, op := driverPGOperation(t, apis[0], tenant, lines)
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.Exec(`CREATE TABLE "device-management".test_committed_effects (device_id bigint NOT NULL)`).Error)
	require.NoError(t, sys.Exec(`CREATE FUNCTION "device-management".test_log_effect() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO "device-management".test_committed_effects (device_id) VALUES (NEW.device_id);
  RETURN NEW;
END $$`).Error)
	require.NoError(t, sys.Exec(`CREATE TRIGGER test_log_effect AFTER INSERT OR UPDATE ON "device-management".entity_attribute_generations
FOR EACH ROW EXECUTE FUNCTION "device-management".test_log_effect()`).Error)
	fleetEffectHook = func() { time.Sleep(100 * time.Millisecond) }
	defer func() { fleetEffectHook = nil }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	runDriverReplicas(t, apis, ctx, func() bool {
		var cur FleetOperation
		return mgr.DB(tenantCtx).First(&cur, op.ID).Error == nil && cur.Dispatch == FleetDispatchDispatched
	})

	var contended int64
	require.NoError(t, mgr.DB(tenantCtx).Model(&FleetOperationLine{}).
		Where("operation_id = ? AND last_error_code = ?", op.ID, "DEVICE_BUSY").Count(&contended).Error)
	require.Positive(t, contended, "the replicas never worked one line at once: this run proves nothing")
	var posted int64
	require.NoError(t, mgr.DB(tenantCtx).Model(&FleetOperationLine{}).
		Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPosted).Count(&posted).Error)
	require.Equal(t, int64(lines), posted)
	type effects struct {
		DeviceID int64
		N        int64
	}
	var perDevice []effects
	require.NoError(t, sys.Raw(`SELECT device_id, count(*) AS n FROM "device-management".test_committed_effects
WHERE device_id IN (SELECT target_id FROM "device-management".fleet_operation_lines WHERE operation_id = ?)
GROUP BY device_id`, op.ID).Scan(&perDevice).Error)
	require.Len(t, perDevice, lines, "every line's effect committed")
	for _, e := range perDevice {
		require.Equal(t, int64(1), e.N, "device %d has more than one committed effect", e.DeviceID)
	}
	t.Logf("%d of %d lines were contended by a second replica", contended, lines)
}
