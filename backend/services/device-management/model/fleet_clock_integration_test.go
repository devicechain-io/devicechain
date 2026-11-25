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
	At time.Time
}

type fleetGrant struct {
	Window  time.Time // the window the statement charged the grant to
	At      time.Time // the database clock read straight after the grant, on the witness's own connection
	Granted int
}

// I-14: three replicas on one operation at 50/s, against a postable, a refusing and a busy fixture, with
// and without skewed process clocks. Two witnesses, both on database time and neither from pg_stat:
//
//   - the GRANTS: each grant the pacer makes and the window it charged it to. A window holds at most 50,
//     and any 750 ms of database time sees at most 100 granted (two windows; the stamp is read just after
//     the grant, so the 250 ms margin absorbs that read's delay without reaching into a third window);
//   - the ATTEMPTS: the invocation log, outside the posting transaction's rollback, so refused and busy
//     attempts stay in it. Every attempt spent a permit: attempts never exceed what was granted.
//
// The attempts are NOT bucketed by time. A permit is spent after it is granted, by a worker that may run
// late, so under load one window's attempts spill into the next one's interval. Bucketing them by the
// window current at invocation measures that lag, not the pacer: it failed at 58 and 84 attempts in one
// "window" on a loaded host.
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
				releaseDispatchSlot(t, p)
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
				var grants []fleetGrant
				var witnessErr error
				fleetPermitHook = func(window time.Time, granted int) {
					var at time.Time
					err := db.Raw("SELECT clock_timestamp()").Row().Scan(&at)
					mu.Lock()
					defer mu.Unlock()
					if err != nil && witnessErr == nil {
						witnessErr = err
					}
					grants = append(grants, fleetGrant{Window: window, At: at, Granted: granted})
				}
				t.Cleanup(func() { fleetPermitHook = nil })
				fleetPostHook = func(stage string, tx *gorm.DB) {
					if stage != "read" {
						return
					}
					var row fleetAttemptTime
					err := tx.Raw(`SELECT clock_timestamp() AS at`).Scan(&row).Error
					mu.Lock()
					defer mu.Unlock()
					if err != nil && witnessErr == nil {
						witnessErr = err
					}
					log = append(log, row)
				}
				t.Cleanup(func() { fleetPostHook = nil })
				// Enough evidence, whatever the host's load: more than one window's worth granted, across
				// at least two windows. Run at least 3.2 s, and stop gathering it after 30 s.
				evidence := func() bool {
					mu.Lock()
					defer mu.Unlock()
					total, windows := 0, map[time.Time]bool{}
					for _, g := range grants {
						if g.Granted > 0 {
							total += g.Granted
							windows[g.Window] = true
						}
					}
					return total > 50 && len(windows) >= 2
				}
				var wg sync.WaitGroup
				errs := make(chan error, 3)
				start := make(chan struct{})
				began := time.Now()
				for replica := 0; replica < 3; replica++ {
					// Separate pools/connections, the same operation and runtime row.
					api := NewApi(newPostgresRdbManager(t))
					api.PrincipalStanding = slowStanding{}
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						for time.Since(began) < 3200*time.Millisecond || (!evidence() && time.Since(began) < 30*time.Second) {
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
				require.NoError(t, witnessErr)
				require.True(t, evidence(), "exercise a rollover: more than 50 granted, across two windows")

				granted, perWindow := 0, map[time.Time]int{}
				for _, g := range grants {
					granted += g.Granted
					perWindow[g.Window] += g.Granted
				}
				for window, n := range perWindow {
					require.LessOrEqual(t, n, 50, "fixed DB pacer window %s", window)
				}
				sort.Slice(grants, func(i, j int) bool { return grants[i].At.Before(grants[j].At) })
				sum := 0
				for left, right := 0, 0; right < len(grants); right++ {
					sum += grants[right].Granted
					for !grants[right].At.Before(grants[left].At.Add(750 * time.Millisecond)) {
						sum -= grants[left].Granted
						left++
					}
					require.LessOrEqual(t, sum, 100, "granted in the 750ms of DB time ending %s", grants[right].At)
				}
				require.NotEmpty(t, log)
				require.LessOrEqual(t, len(log), granted, "every posting attempt spent a permit")
				if fixture != "postable" {
					var posted int64
					require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = 'POSTED'", p.op.ID).Count(&posted).Error)
					require.Zero(t, posted, "unsuccessful attempts must still be paced")
				}
			})
		}
	}
}

// releaseDispatchSlot moves the fixture's operation to DISPATCHED when the test ends. A RUNNING operation left behind
// counts against the instance's dispatching limit for every later fixture on the same database, so a
// second run against one server (-count=2, or a mutant loop) failed in fixture setup, not in the code.
func releaseDispatchSlot(t *testing.T, p *postPG) {
	t.Helper()
	t.Cleanup(func() {
		_ = p.api.RDB.DB(p.ctx).Model(&FleetOperation{}).Where("id = ?", p.op.ID).Update("dispatch", FleetDispatchDispatched).Error
	})
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

// The PostgreSQL half of TestFleetPacerRollsAWindowThatStartsInTheFuture: the statement's own rollover.
func TestFleetPostgresPacerRollsAWindowThatStartsInTheFuture(t *testing.T) {
	p := newPostPG(t, "v1")
	releaseDispatchSlot(t, p)
	db := p.api.RDB.DB(p.ctx)
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
		Updates(map[string]any{"pace_window_start": gorm.Expr("clock_timestamp() + interval '1 hour'"), "pace_issued": 50}).Error)
	before := fleetPGNow(t, db)
	granted, err := p.api.AcquireFleetPermits(p.ctx, p.op.ID, 3, 50, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 3, granted)
	after := fleetPGNow(t, db)
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.False(t, rt.PaceWindowStart.Time.Before(before))
	require.False(t, rt.PaceWindowStart.Time.After(after))
	require.EqualValues(t, 3, rt.PaceIssued)
}
