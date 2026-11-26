// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Record the PostgreSQL branch's round trips without pretending SQLite can test
// PostgreSQL locking. Real concurrency and SQL execution live in the tagged suite.
type fleetPGDialect struct{ gorm.Dialector }

func (fleetPGDialect) Name() string { return "postgres" }

type fleetSQLCall struct {
	query string
	args  []any
}

type fleetSQLRecorder struct {
	gorm.ConnPool
	calls  []fleetSQLCall
	noRows bool
}

func (r *fleetSQLRecorder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	r.calls = append(r.calls, fleetSQLCall{q, args})
	return r.ConnPool.QueryContext(ctx, "SELECT 0 AS attempts")
}

func (r *fleetSQLRecorder) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	r.calls = append(r.calls, fleetSQLCall{q, args})
	if r.noRows {
		return r.ConnPool.QueryRowContext(ctx, "SELECT 7 AS granted, NULL AS window_start WHERE 0")
	}
	return r.ConnPool.QueryRowContext(ctx, "SELECT 7 AS granted, NULL AS window_start")
}

func TestFleetPostgresPacerFailsClosedWithoutItsTenantRow(t *testing.T) {
	api, recorder := fleetRecordedPG(t)
	_, err := api.AcquireFleetPermits(context.Background(), 42, 9, 50, time.Time{})
	require.ErrorIs(t, err, core.ErrNoTenant)
	require.Empty(t, recorder.calls)
	recorder.noRows = true
	_, err = api.AcquireFleetPermits(core.WithTenant(context.Background(), "acme"), 42, 9, 50, time.Time{})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFleetPostgresRetryDeadlineUsesDatabaseTime(t *testing.T) {
	api, recorder := fleetRecordedPG(t)
	old := fleetClock
	fleetClock = func() time.Time {
		t.Error("PostgreSQL retry must not read fleetClock")
		return time.Time{}
	}
	t.Cleanup(func() { fleetClock = old })
	require.NoError(t, api.RecordFleetLineRetry(context.Background(), 42, "DEVICE_BUSY", time.Now().Add(3*time.Second)))
	require.Len(t, recorder.calls, 2)
	q := strings.ToLower(recorder.calls[1].query)
	require.Contains(t, q, "next_attempt_at")
	require.Contains(t, q, "clock_timestamp()")
	for _, arg := range recorder.calls[1].args {
		_, isTime := arg.(time.Time)
		require.False(t, isTime)
		_, isNullTime := arg.(sql.NullTime)
		require.False(t, isNullTime)
	}
}

func (r *fleetSQLRecorder) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	r.calls = append(r.calls, fleetSQLCall{q, args})
	return fleetSQLResult{}, nil
}

type fleetSQLResult struct{}

func (fleetSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (fleetSQLResult) RowsAffected() (int64, error) { return 1, nil }

func (r *fleetSQLRecorder) Commit() error { return r.ConnPool.(gorm.TxCommitter).Commit() }

func (r *fleetSQLRecorder) Rollback() error { return r.ConnPool.(gorm.TxCommitter).Rollback() }

func fleetRecordedPG(t *testing.T) (*Api, *fleetSQLRecorder) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, &gorm.Config{SkipDefaultTransaction: true})
	db.Config.Dialector = fleetPGDialect{db.Dialector}
	r := &fleetSQLRecorder{ConnPool: db.Statement.ConnPool}
	db.Config.ConnPool, db.Statement.ConnPool = r, r
	return NewApi(&rdb.RdbManager{Database: db}), r
}

func TestFleetPostgresPacerIsOneDatabaseTimedStatement(t *testing.T) {
	api, recorder := fleetRecordedPG(t)
	skew := time.Now().Add(3 * time.Second)
	granted, err := api.AcquireFleetPermits(core.WithTenant(context.Background(), "acme"), 42, 9, 50, skew)
	require.NoError(t, err)
	require.Equal(t, 7, granted, "the grant must come from RETURNING, not local bookkeeping")
	require.Len(t, recorder.calls, 1, "one atomic statement, without a client read/modify/write transaction")
	q := strings.ToLower(recorder.calls[0].query)
	for _, part := range []string{"update fleet_operation_runtime", "clock_timestamp()", "returning", "pace_issued", "least(", "greatest(", "for update", "as materialized", "tenant_id", "pace_window_start > now"} {
		require.Contains(t, q, part)
	}
	for _, arg := range recorder.calls[0].args {
		_, isTime := arg.(time.Time)
		require.False(t, isTime, "a skewed process timestamp must never reach the pacer")
	}
}

func TestFleetPostgresHeartbeatAndHoldsUseDatabaseTime(t *testing.T) {
	api, recorder := fleetRecordedPG(t)
	old := fleetClock
	fleetClock = func() time.Time {
		t.Error("PostgreSQL must not read fleetClock")
		return time.Time{}
	}
	t.Cleanup(func() { fleetClock = old })
	hold, progressed := FleetHoldRateLimit, true
	api.touchFleetRuntime(context.Background(), &FleetOperation{ID: 42}, &progressed, &hold)
	require.Len(t, recorder.calls, 1, "hold_since must be compared and updated atomically")
	q := strings.ToLower(recorder.calls[0].query)
	for _, column := range []string{"driver_seen_at", "last_progress_at", "hold_seen_at", "hold_since"} {
		require.Contains(t, q, column)
	}
	require.Contains(t, q, "clock_timestamp()")
	require.Contains(t, q, "case when")
	for _, arg := range recorder.calls[0].args {
		_, isTime := arg.(time.Time)
		require.False(t, isTime)
		_, isNullTime := arg.(sql.NullTime)
		require.False(t, isNullTime)
	}
}

func TestFleetPostgresPostingWritesDatabaseDeadlineInClaim(t *testing.T) {
	e := postFixture(t, "v1", false)
	var recorder *fleetSQLRecorder
	fleetPostHook = func(stage string, tx *gorm.DB) {
		if stage != "claim" {
			return
		}
		// Keep the real effect transaction, recording just its final claim.
		cfg := *tx.Config
		tx.Config = &cfg
		tx.Dialector = fleetPGDialect{tx.Dialector}
		recorder = &fleetSQLRecorder{ConnPool: tx.Statement.ConnPool}
		tx.Statement.ConnPool = recorder
	}
	t.Cleanup(func() { fleetPostHook = nil })
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	require.NotNil(t, recorder)
	require.Len(t, recorder.calls, 1)
	q := strings.ToLower(recorder.calls[0].query)
	require.Contains(t, q, "settle_by")
	require.Contains(t, q, "clock_timestamp()")
	require.Contains(t, q, "interval '1 second'")
	for _, arg := range recorder.calls[0].args {
		_, isTime := arg.(time.Time)
		require.False(t, isTime, "posting timestamps must be evaluated in the claim transaction")
		_, isNullTime := arg.(sql.NullTime)
		require.False(t, isNullTime)
	}
}

func TestFleetSQLitePostingDeadlineUsesInjectedClock(t *testing.T) {
	e := postFixture(t, "v1", false)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	old := fleetClock
	fleetClock = func() time.Time { return now }
	t.Cleanup(func() { fleetClock = old })
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	line := e.reload(t)
	require.Equal(t, now, line.PostedAt.Time)
	require.Equal(t, now.Add(time.Duration(e.op.SettleDeadlineS)*time.Second), line.SettleBy.Time)
}

func TestFleetPacerChargesRefusedAndBusyAttempts(t *testing.T) {
	for _, fixture := range []string{"postable", "refusing", "busy"} {
		t.Run(fixture, func(t *testing.T) {
			rate := int32(1)
			e := driveFixture(t, 3, FleetSafetyRequest{MaxPostsPerSecond: &rate})
			if fixture == "refusing" {
				require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{}).Where("id = ?", e.op.ID).
					Update("intent", `{"key":"undeclared","valueType":"STRING","value":"v"}`).Error)
			}
			if fixture == "busy" {
				old := fleetTryDeviceLock
				fleetTryDeviceLock = func(context.Context, *gorm.DB, uint) (bool, error) { return false, nil }
				t.Cleanup(func() { fleetTryDeviceLock = old })
			}
			first := e.drive(t)
			switch fixture {
			case "postable":
				require.Equal(t, 1, first.Posted)
			case "refusing":
				require.Equal(t, 1, first.Refused)
			case "busy":
				require.Equal(t, 1, first.Busy)
			}
			second := e.drive(t)
			require.Zero(t, second.Posted+second.Refused+second.Busy)
			require.Equal(t, FleetHoldRateLimit, second.Hold)
		})
	}
}

// A spent window stamped in the future (a failover onto a primary whose clock is behind, a clock stepped back)
// must roll at once: kept, it would hold the operation at RATE_LIMIT until wall time caught up with it.
func TestFleetPacerRollsAWindowThatStartsInTheFuture(t *testing.T) {
	rate := int32(5)
	e := driveFixture(t, 1, FleetSafetyRequest{MaxPostsPerSecond: &rate})
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", e.op.ID).
		Updates(map[string]any{"pace_window_start": e.now.Add(time.Hour), "pace_issued": 5}).Error)
	granted, err := e.api.AcquireFleetPermits(e.ctx, e.op.ID, 3, 5, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 3, granted)
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.current(t))
	require.NoError(t, err)
	require.True(t, rt.PaceWindowStart.Time.Equal(e.now), "the new window starts now, not at the stored future start")
	require.EqualValues(t, 3, rt.PaceIssued)
}

// The lease is the one clock that stays in the process: it measures how long THIS process has been running
// on a standing answer, so it is granted and checked on time.Now's monotonic reading, never on the
// platform clock the shared state uses. A platform clock an hour off must neither stretch nor expire it.
func TestFleetLeaseStaysOnTheProcessMonotonicClock(t *testing.T) {
	for _, skew := range []time.Duration{-time.Hour, time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			e := postFixture(t, "v1", false)
			old := fleetClock
			fleetClock = func() time.Time { return time.Now().Add(skew) }
			t.Cleanup(func() { fleetClock = old })
			e.api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
			lease, hold, err := e.api.fleetLease(e.ctx, e.op)
			require.NoError(t, err)
			require.Empty(t, hold)
			require.NotNil(t, lease)
			require.WithinDuration(t, time.Now().Add(FleetLeaseTTL), lease.Deadline, time.Second, "granted from the process clock")
			e.lease = *lease
			require.Equal(t, FleetPosted, e.post(t).Outcome, "checked at commit against the process clock")
		})
	}
}

// The control path stamps and judges on the fleet clock, which on PostgreSQL is the database's (the
// integration suite proves that branch). Here the fixture clock is eight days ahead of this process:
// the contracts created before the jump are past their seven-day expiry on that clock and nowhere
// else, and every stamp a transition writes must be the fixture clock's, never the process clock's.
func TestFleetControlStampsAndJudgesOnTheFleetClock(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	db := api.RDB.DB(ctx)
	stale := readyOp(t, api, ctx, "stale", FleetSafetyRequest{}, "d1")
	swept := readyOp(t, api, ctx, "swept", FleetSafetyRequest{}, "d1")
	ahead := time.Now().Add(8 * 24 * time.Hour).Truncate(time.Second)
	old := fleetClock
	clock := ahead
	fleetClock = func() time.Time { return clock }
	t.Cleanup(func() { fleetClock = old })
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }
	at := func(want time.Time, got sql.NullTime, what string) {
		t.Helper()
		require.True(t, got.Valid, what)
		require.True(t, want.Equal(got.Time), "%s: want %s on the fleet clock, got %s", what, want, got.Time)
	}
	reload := func(op *FleetOperation) *FleetOperation {
		t.Helper()
		var cur FleetOperation
		require.NoError(t, db.First(&cur, op.ID).Error)
		return &cur
	}
	lastEvent := func(op *FleetOperation) FleetOperationEvent {
		t.Helper()
		var ev FleetOperationEvent
		require.NoError(t, db.Where("operation_id = ?", op.ID).Order("id DESC").First(&ev).Error)
		return ev
	}

	// Expired on the fleet clock only: a start judged on process time would run it.
	_, err := api.StartFleetOperation(ctx, "stale", stale.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeExpired)

	// A contract created on the fleet clock expires seven days after IT.
	now := tick()
	live := readyOp(t, api, ctx, "live", FleetSafetyRequest{}, "d1", "d2")
	at(now.Add(7*24*time.Hour), reload(live).ExpiresAt, "expires_at")
	at(now, reload(live).StateChangedAt, "created state_changed_at")

	// The sweep finds and re-judges on the fleet clock: both contracts from before the jump are due.
	now = tick()
	n, err := api.ExpireFleetOperations(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	for _, op := range []*FleetOperation{stale, swept} {
		cur := reload(op)
		require.Equal(t, FleetDispatchAborted, cur.Dispatch)
		at(now, cur.StateChangedAt, "expired state_changed_at")
		at(now, cur.DispatchedAt, "expired dispatched_at")
		at(now, cur.ClosedAt, "expired closed_at")
		require.True(t, now.Equal(lastEvent(op).At), "EXPIRE event")
	}

	now = tick()
	_, err = api.StartFleetOperation(ctx, "live", live.ContractDigest, alice)
	require.NoError(t, err)
	cur := reload(live)
	at(now, cur.StateChangedAt, "start state_changed_at")
	at(now, cur.StartedAt, "started_at")
	at(now, cur.GrantBoundAt, "start grant_bound_at")
	require.True(t, now.Equal(lastEvent(live).At), "START event")
	var contract FleetOperationContract
	require.NoError(t, db.Where("operation_id = ?", live.ID).First(&contract).Error)
	at(now, contract.ApprovedAt, "approved_at")
	at(now, contract.ActivatedAt, "activated_at")

	now = tick()
	_, err = api.PauseFleetOperation(ctx, "live", alice.Name)
	require.NoError(t, err)
	at(now, reload(live).StateChangedAt, "pause state_changed_at")
	require.True(t, now.Equal(lastEvent(live).At), "PAUSE event")

	now = tick()
	bob := FleetApprover{Name: "bob@example.com", Authorities: alice.Authorities}
	_, err = api.ResumeFleetOperation(ctx, "live", live.ContractDigest, nil, bob)
	require.NoError(t, err)
	cur = reload(live)
	at(now, cur.StateChangedAt, "handover state_changed_at")
	at(now, cur.GrantBoundAt, "handover grant_bound_at")
	require.True(t, now.Equal(lastEvent(live).At), "HANDOVER event")

	now = tick()
	_, err = api.AbortFleetOperation(ctx, "live", bob.Name)
	require.NoError(t, err)
	cur = reload(live)
	at(now, cur.StateChangedAt, "abort state_changed_at")
	at(now, cur.DispatchedAt, "abort dispatched_at")
	at(now, cur.ClosedAt, "abort closed_at")
	require.True(t, now.Equal(lastEvent(live).At), "ABORT event")
}

// The database and this test process share one kernel clock, so no behavioral test can tell a
// process-time stamp from a database-time one on PostgreSQL. The files that stamp control
// transitions and judge expiry therefore never call time.Now: their time comes from fleetDBNow.
func TestFleetControlNeverCallsTheProcessClock(t *testing.T) {
	for _, name := range []string{"api_fleet_operation_control.go", "api_fleet_expiry_sweep.go", "fleet_clock.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Now" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("%s calls time.Now(); control time must come from fleetDBNow", fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
}
