// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-notification-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Lifted from event-management's Postgres harness: use the real rdb initialization
// and migration chain, never AutoMigrate. Missing Postgres is a failure, not a skip.
// Run with DC_IT_PG{HOST,PORT,USER,PASSWORD} and go test -tags integration ./model.
func claimsPostgres(t *testing.T) (*Api, context.Context) {
	t.Helper()
	env := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, password := env("DC_IT_PGHOST", "localhost"), env("DC_IT_PGUSER", "postgres"), env("DC_IT_PGPASSWORD", "devicechain")
	const instance = "itnotifclaims"
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, password, instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "notification-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": user, "password": password,
		}},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	pool, err := mgr.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(24)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "claims-tenant"), 60*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, mgr.DB(core.WithSystemContext(ctx)).Exec(
		`TRUNCATE TABLE "notification-management".notification_states, "notification-management".notification_deferrals RESTART IDENTITY`).Error)
	return NewApi(mgr, nil), ctx
}

// Each participant reserves its own physical connection BEFORE the barrier opens.
// Errors count as neither wins nor losses, and must fail even if one caller won.
func raceClaims(t *testing.T, api *Api, ctx context.Context, n int, claim func(*Api) (bool, error)) int {
	t.Helper()
	type result struct {
		won bool
		err error
	}
	ready, start, results := make(chan struct{}, n), make(chan struct{}), make(chan result, n)
	for i := 0; i < n; i++ {
		go func() {
			var won bool
			err := api.RDB.DB(ctx).Connection(func(db *gorm.DB) error {
				ready <- struct{}{}
				select {
				case <-start:
				case <-ctx.Done():
					return ctx.Err()
				}
				var err error
				won, err = claim(NewApi(&rdb.RdbManager{Database: db}, nil))
				return err
			})
			results <- result{won, err}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(start)
	wins, losses := 0, 0
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			require.NoError(t, r.err)
			if r.won {
				wins++
			} else {
				losses++
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.Equal(t, n, wins+losses)
	return wins
}

func claimState(t *testing.T, api *Api, ctx context.Context, token string) *NotificationState {
	t.Helper()
	rows, err := api.NotificationStatesByAlarmToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

func TestIntegrationNotificationClaims(t *testing.T) {
	api, ctx := claimsPostgres(t)
	for _, nanos := range []int{123456000, 123456789} {
		t.Run(fmt.Sprint(nanos), func(t *testing.T) {
			raised := time.Date(2026, 11, 1, 12, 0, 0, nanos, time.UTC)
			token := fmt.Sprintf("alarm-%d", nanos)
			require.NoError(t, api.RecordNotification(ctx, token, "key", "CRITICAL", raised, raised))
			state := claimState(t, api, ctx, token) // claim only the database's round-tripped cycle
			require.True(t, state.CycleRaisedAt.Valid)
			require.True(t, raised.Truncate(time.Microsecond).Equal(state.CycleRaisedAt.Time))
			require.Equal(t, 1, raceClaims(t, api, ctx, 16, func(a *Api) (bool, error) {
				return a.ClaimEscalation(ctx, token, 0, state.CycleRaisedAt, raised.Add(time.Minute))
			}))
			got := claimState(t, api, ctx, token)
			require.Equal(t, 1, got.EscalationLevel)
			require.Equal(t, 2, got.NotifyCount)
			require.NoError(t, api.MarkCleared(ctx, token, "key", "CRITICAL", raised, raised.Add(time.Hour)))
			state = claimState(t, api, ctx, token)
			require.Equal(t, 1, state.EscalationLevel, "same nanosecond cycle must not reset the tier")
			require.Equal(t, 1, raceClaims(t, api, ctx, 16, func(a *Api) (bool, error) {
				return a.ClaimClearAnnouncement(ctx, token, state.CycleRaisedAt, raised.Add(2*time.Hour))
			}))
			got = claimState(t, api, ctx, token)
			require.True(t, got.ClearNotifiedAt.Valid)
			require.Equal(t, state.CycleRaisedAt, got.ClearNotifiedCycle)

			d := &NotificationDeferral{AlarmToken: token, PolicyId: 1, AlarmKey: "key", Severity: "CRITICAL",
				OccurredTime: raised, ReleaseAt: raised.Add(time.Hour), Event: datatypes.JSON(`{}`)}
			require.NoError(t, api.DeferNotifications(ctx, []*NotificationDeferral{d}))
			rows, err := api.NotificationDeferralsByAlarmToken(ctx, []string{token})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			d = rows[0] // updated_at and release_at must come from Postgres too
			until := raised.Add(3 * time.Hour).Truncate(time.Microsecond)
			require.Equal(t, 1, raceClaims(t, api, ctx, 16, func(a *Api) (bool, error) {
				return a.LeaseNotificationDeferral(ctx, d, until)
			}))
			rows, err = api.NotificationDeferralsByAlarmToken(ctx, []string{token})
			require.NoError(t, err)
			require.Len(t, rows, 1, "leasing must retain the only pending page")
			require.True(t, until.Equal(rows[0].ReleaseAt))
		})
	}

	// Negative control named in the plan: dropping the expected-level predicate
	// makes EVERY contender win. This proves the race's one-winner check is useful.
	raised := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, api.RecordNotification(ctx, "broken-escalation", "key", "CRITICAL", raised, raised))
	require.Equal(t, 16, raceClaims(t, api, ctx, 16, func(a *Api) (bool, error) {
		r := a.RDB.DB(ctx).Model(&NotificationState{}).
			Where("alarm_token = ? AND cycle_raised_at = ? AND acknowledged_at IS NULL AND cleared_at IS NULL", "broken-escalation", raised).
			Updates(map[string]any{"escalation_level": gorm.Expr("escalation_level + 1"), "notify_count": gorm.Expr("notify_count + 1")})
		return r.RowsAffected == 1, r.Error
	}))
}

type claimsControlKey struct{}

// Waiting on an UPDATE is insufficient: without FOR UPDATE a stale read also
// blocks when it eventually writes. Observe the blocked SELECT itself.
func waitingClaimQuery(t *testing.T, api *Api, ctx context.Context, pid int, pattern string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var waiting bool
		require.NoError(t, api.RDB.DB(core.WithSystemContext(ctx)).Raw(
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock'
			 AND cardinality(pg_blocking_pids(pid)) > 0 AND query ILIKE ?)`, pid, pattern).Scan(&waiting).Error)
		if waiting {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestIntegrationNotificationWritersBlockAndReadCommittedState(t *testing.T) {
	api, ctx := claimsPostgres(t)
	// Test-only seam for the named negative control. Production has no bypass.
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("claims:without_lock", func(db *gorm.DB) {
		if db.Statement.Context.Value(claimsControlKey{}) == true {
			delete(db.Statement.Clauses, "FOR")
		}
	}))
	t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Query().Remove("claims:without_lock")) })
	for _, method := range []string{"RecordPagedFrom", "MarkCleared"} {
		for _, broken := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/without-lock=%v", method, broken), func(t *testing.T) {
				token := fmt.Sprintf("%s-%v", method, broken)
				raised := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
				require.NoError(t, api.RecordNotification(ctx, token, "key", "CRITICAL", raised, raised))
				holder := api.RDB.DB(ctx).Begin()
				require.NoError(t, holder.Error)
				defer holder.Rollback()
				stamp := raised.Add(time.Hour)
				writes := map[string]any{"notify_count": 7}
				if method == "MarkCleared" {
					writes["cleared_at"] = stamp
				}
				require.NoError(t, holder.Model(&NotificationState{}).Where("alarm_token = ?", token).Updates(writes).Error)
				pidCh, done := make(chan int, 1), make(chan error, 1)
				waitCtx := context.WithValue(ctx, claimsControlKey{}, broken)
				go func() {
					done <- api.RDB.DB(waitCtx).Connection(func(db *gorm.DB) error {
						var pid int
						if err := db.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
							return err
						}
						pidCh <- pid
						a := NewApi(&rdb.RdbManager{Database: db}, nil)
						if method == "MarkCleared" {
							return a.MarkCleared(waitCtx, token, "key", "CRITICAL", raised, stamp.Add(time.Hour))
						}
						_, err := a.RecordPagedFrom(waitCtx, token, "key", "CRITICAL", raised, stamp, nil, AlarmSource{}, time.Time{})
						return err
					})
				}()
				var pid int
				select {
				case pid = <-pidCh:
				case err := <-done:
					t.Fatalf("waiter ended before query: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				timeout := 5 * time.Second
				if broken {
					timeout = 500 * time.Millisecond
				}
				blocked := waitingClaimQuery(t, api, ctx, pid, "SELECT%FOR UPDATE%", timeout)
				require.Equal(t, !broken, blocked, "removing clause.Locking must time out the blocked-SELECT probe")
				if broken {
					// Prove the unlocked read has finished before releasing the holder;
					// a delayed goroutine must not turn this control into a serial run.
					require.True(t, waitingClaimQuery(t, api, ctx, pid, "UPDATE%", 5*time.Second))
				}
				require.NoError(t, holder.Commit().Error)
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				got := claimState(t, api, ctx, token)
				if method == "RecordPagedFrom" {
					want := 8
					if broken {
						want = 2
					}
					require.Equal(t, want, got.NotifyCount, "the locked reader must increment the holder's value")
				} else {
					want := stamp
					if broken {
						want = stamp.Add(time.Hour)
					}
					require.True(t, got.ClearedAt.Valid && want.Equal(got.ClearedAt.Time), "the first committed clear must survive")
				}
			})
		}
	}
}

func TestIntegrationNotificationConcurrentFirstInserts(t *testing.T) {
	api, ctx := claimsPostgres(t)
	// Park both real API calls after their absent-row reads. A start barrier alone
	// could serialize and exercise an update instead of the unique-index race.
	read, release := make(chan struct{}, 2), make(chan struct{}, 2)
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("claims:first_read", func(db *gorm.DB) {
		if db.Statement.Context.Value(claimsControlKey{}) == true && errors.Is(db.Error, gorm.ErrRecordNotFound) {
			read <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}))
	t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Query().Remove("claims:first_read")) })
	for _, method := range []string{"RecordPagedFrom", "MarkCleared"} {
		t.Run(method, func(t *testing.T) {
			done := make(chan error, 2)
			callCtx := context.WithValue(ctx, claimsControlKey{}, true)
			raised := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			for i := 0; i < 2; i++ {
				go func() {
					if method == "MarkCleared" {
						done <- api.MarkCleared(callCtx, method, "key", "CRITICAL", raised, raised)
						return
					}
					_, err := api.RecordPagedFrom(callCtx, method, "key", "CRITICAL", raised, raised, nil, AlarmSource{}, time.Time{})
					done <- err
				}()
			}
			for i := 0; i < 2; i++ {
				select {
				case <-read:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			// Two sends, rather than closing: reuse the barrier for the other method.
			release <- struct{}{}
			release <- struct{}{}
			wins, conflicts := 0, 0
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if err == nil {
						wins++
						continue
					}
					var pgerr interface{ SQLState() string }
					require.ErrorAs(t, err, &pgerr)
					require.Equal(t, "23505", pgerr.SQLState(), "only a unique violation is an acceptable loser")
					conflicts++
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			require.Equal(t, 1, wins)
			require.Equal(t, 1, conflicts)
			got := claimState(t, api, ctx, method)
			if method == "MarkCleared" {
				require.True(t, got.ClearedAt.Valid)
			} else {
				require.Equal(t, 1, got.NotifyCount)
			}
		})
	}
}
