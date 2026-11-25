// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fleetAttemptTime struct {
	At          time.Time
	WindowStart time.Time
}

// I-14: the invocation witness is outside the posting transaction's rollback:
// refused and busy attempts remain in the log. It uses DB time, never pg_stat.
func TestFleetPostgresI14DatabaseClock(t *testing.T) {
	for _, skewed := range []bool{false, true} {
		for _, fixture := range []string{"postable", "refusing", "busy"} {
			t.Run(fmt.Sprintf("%s/skew=%t", fixture, skewed), func(t *testing.T) {
				p := newPostPG(t, "v1")
				db := p.api.RDB.DB(p.ctx)
				const targets = 250
				// Distinct devices keep device contention out of the postable fixture.
				for i := 2; i < targets; i++ {
					d, err := p.api.CreateDevice(p.ctx, &DeviceCreateRequest{Token: fmt.Sprintf("clock%d", i), DeviceTypeToken: "sensor"})
					require.NoError(t, err)
					line := p.lines[0]
					line.ID, line.Ordinal, line.TargetKey, line.TargetId.Int64 = 0, int32(i+1), d.Token, int64(d.ID)
					require.NoError(t, db.Create(&line).Error)
					p.devs = append(p.devs, d)
				}
				require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", p.op.ID).
					Updates(map[string]any{"eff_posts_per_second": 50, "target_count": targets}).Error)
				if fixture == "refusing" {
					require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", p.op.ID).
						Update("intent", `{"key":"undeclared","valueType":"STRING","value":"v"}`).Error)
				}
				if fixture == "busy" {
					holder := db.Begin()
					require.NoError(t, holder.Error)
					t.Cleanup(func() { _ = holder.Rollback().Error })
					for _, dev := range p.devs {
						require.NoError(t, holder.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, dev.ID).Error)
					}
				}
				oldClock := fleetClock
				var reads atomic.Int64
				fleetClock = func() time.Time {
					offset := time.Duration(0)
					if skewed {
						// Three independent replicas' clock readings, cycling -3s/0/+3s.
						// Atomic reads also make this hostile-clock fixture race-safe.
						offset = time.Duration(reads.Add(1)%3-1) * 3 * time.Second
					}
					return time.Now().Add(offset)
				}
				t.Cleanup(func() { fleetClock = oldClock })
				var mu sync.Mutex
				var log []fleetAttemptTime
				var logErr error
				fleetPostHook = func(stage string, tx *gorm.DB) {
					if stage != "read" {
						return
					}
					var row fleetAttemptTime
					err := tx.Raw(`SELECT clock_timestamp() AS at, pace_window_start AS window_start
						FROM fleet_operation_runtime WHERE operation_id = ? AND tenant_id = ?`, p.op.ID, p.tenant).Scan(&row).Error
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						logErr = err
					}
					log = append(log, row)
				}
				t.Cleanup(func() { fleetPostHook = nil })
				var wg sync.WaitGroup
				errs := make(chan error, 3)
				start := make(chan struct{})
				for replica := 0; replica < 3; replica++ {
					// Separate pools/connections, the same operation and runtime row.
					api := NewApi(newPostgresRdbManager(t))
					api.PrincipalStanding = slowStanding{}
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						end := time.Now().Add(3200 * time.Millisecond)
						for time.Now().Before(end) {
							// Other integration fixtures may leave RUNNING operations in
							// other tenants. Isolate this operation's 50/s cap from them.
							caps := FleetCaps{TenantPostsPerSecond: 50, InstancePostsPerSecond: 1000000}
							if _, err := api.DriveFleetOperation(p.ctx, p.op.Token, caps); err != nil {
								errs <- err
								return
							}
							time.Sleep(5 * time.Millisecond)
						}
					}()
				}
				close(start)
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				require.NoError(t, logErr)
				require.Greater(t, len(log), 50, "exercise a rollover, not an empty or single-window pass")
				windows := map[time.Time]int{}
				for _, row := range log {
					windows[row.WindowStart]++
				}
				for window, count := range windows {
					require.LessOrEqual(t, count, 50, "fixed DB pacer window %s", window)
				}
				sort.Slice(log, func(i, j int) bool { return log[i].At.Before(log[j].At) })
				for left, right := 0, 0; right < len(log); right++ {
					for !log[right].At.Before(log[left].At.Add(time.Second)) {
						left++
					}
					require.LessOrEqual(t, right-left+1, 100, "sliding 1s DB-time window")
				}
				if fixture != "postable" {
					var posted int64
					require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = 'POSTED'", p.op.ID).Count(&posted).Error)
					require.Zero(t, posted, "unsuccessful attempts must still be paced")
				}
			})
		}
	}
}

func fleetPGNow(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Row().Scan(&now))
	return now
}

func TestFleetPostgresDeadlineAndRuntimeIgnoreProcessSkew(t *testing.T) {
	for _, skew := range []time.Duration{-3 * time.Second, 3 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			p := newPostPG(t, "v1")
			db := p.api.RDB.DB(p.ctx)
			old := fleetClock
			fleetClock = func() time.Time { return time.Now().Add(skew) }
			t.Cleanup(func() { fleetClock = old })
			// A short line deadline lets the real DB-time due predicate pass it.
			require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", p.op.ID).Update("settle_deadline_s", 1).Error)
			before := fleetPGNow(t, db)
			r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
			require.NoError(t, err)
			require.Equal(t, FleetPosted, r.Outcome)
			after := fleetPGNow(t, db)
			var line FleetOperationLine
			require.NoError(t, db.First(&line, p.lines[0].ID).Error)
			require.False(t, line.SettleBy.Time.Before(before.Add(time.Second)))
			require.False(t, line.SettleBy.Time.After(after.Add(time.Second)))
			require.WithinDuration(t, line.PostedAt.Time.Add(time.Second), line.SettleBy.Time, 50*time.Millisecond)
			var due int64
			deadlinePass := func() int64 {
				t.Helper()
				require.NoError(t, db.Model(&FleetOperationLine{}).Where("id = ? AND settle_by <= clock_timestamp()", line.ID).Count(&due).Error)
				return due
			}
			require.Zero(t, deadlinePass())
			require.Eventually(t, func() bool { return deadlinePass() == 1 }, 2500*time.Millisecond, 25*time.Millisecond)
			// The BC-6 reconciler is not on main yet; this exercises its DB due
			// predicate without implementing any settlement behavior here.
			hold, progress := FleetHoldRateLimit, true
			before = fleetPGNow(t, db)
			p.api.touchFleetRuntime(p.ctx, p.op, &progress, &hold)
			after = fleetPGNow(t, db)
			rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
			require.NoError(t, err)
			for _, stamp := range []time.Time{rt.DriverSeenAt.Time, rt.LastProgressAt.Time, rt.HoldSince.Time, rt.HoldSeenAt.Time} {
				require.False(t, stamp.Before(before))
				require.False(t, stamp.After(after))
			}
			p.api.touchFleetRuntime(p.ctx, p.op, nil, &hold)
			again, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
			require.NoError(t, err)
			require.Equal(t, rt.HoldSince, again.HoldSince)
			_, err = p.api.AcquireFleetPermits(core.WithTenant(context.Background(), "another-tenant"), p.op.ID, 50, 50, time.Now())
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		})
	}
}
