// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type campaignConcurrentGroups struct {
	mu       sync.Mutex
	resolver pagingResolver
}

func (r *campaignConcurrentGroups) ResolveGroupTargets(ctx context.Context, token string, version *int32, cursor string, limit int) (*GroupTargetPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolver.ResolveGroupTargets(ctx, token, version, cursor, limit)
}

// Separate managers must open separate pools: sharing one SQLite-style working
// connection would serialize the passes before the Postgres lock was exercised.
func postgresCampaignReplicas(t *testing.T) (*Api, *Api, []string) {
	t.Helper()
	a := newPostgresApi(t)
	for _, table := range []string{"campaign_elements", "campaign_cycles", "campaigns"} {
		require.NoError(t, a.RDB.Database.Exec(fmt.Sprintf(`TRUNCATE TABLE "command-delivery".%q CASCADE`, table)).Error)
	}
	mgr := &rdb.RdbManager{
		Microservice:       a.RDB.Microservice,
		Migrations:         Migrations,
		InstanceConfig:     a.RDB.InstanceConfig,
		MicroserviceConfig: a.RDB.MicroserviceConfig,
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	poolA, err := a.RDB.Database.DB()
	require.NoError(t, err)
	poolB, err := mgr.Database.DB()
	require.NoError(t, err)
	require.NotSame(t, poolA, poolB)
	t.Cleanup(func() { require.NoError(t, poolB.Close()) })
	b := NewApi(mgr)
	members := prepareReplicaCampaign(t, a)
	a.GroupTargetResolver = &campaignConcurrentGroups{resolver: pagingResolver{members: members}}
	b.GroupTargetResolver = &campaignConcurrentGroups{resolver: pagingResolver{members: members}}
	return a, b, members
}

func campaignResult(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("campaign operation did not complete")
	}
}

func TestPostgresCampaignReplicasDispatchOneCycleOnce(t *testing.T) {
	a, b, members := postgresCampaignReplicas(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Race both planning and firing, with eight passes over independent pools.
	for _, now := range []time.Time{at("2026-06-21T22:00:00Z"), campaignReplicaDue, campaignReplicaDue.Add(time.Minute)} {
		setClock(a, now)
		setClock(b, now)
		start := make(chan struct{})
		results := make(chan error, 8)
		var ready sync.WaitGroup
		ready.Add(8)
		for i := range 8 {
			api := []*Api{a, b}[i%2]
			go func() {
				ready.Done()
				<-start
				results <- api.TickCampaigns(ctx, now, fmt.Sprintf("replica-%d-%d", i, now.Unix()))
			}()
		}
		ready.Wait()
		close(start)
		for range 8 {
			campaignResult(t, results)
		}
	}
	assertReplicaDispatch(t, a, members, 1)
}

func TestPostgresRecoveredCampaignClaimsRecheckMembership(t *testing.T) {
	a, b, members := postgresCampaignReplicas(t)
	tick(t, a, at("2026-06-21T22:00:00Z"), "plan")
	token := failCampaignBatchCreation(t, a, "replicas", campaignReplicaDue, members)
	// Recovery uses an independent pool, after membership changed during the outage.
	b.GroupTargetResolver = &campaignConcurrentGroups{resolver: pagingResolver{members: members[1:]}}
	tick(t, b, campaignReplicaDue.Add(campaignClaimStale+time.Second), "recover")
	require.Zero(t, commandsFor(t, b, members[0]))
	e := elementOf(t, b, "replicas", members[0])
	require.Equal(t, string(ElementSkipped), e.Status)
	require.Equal(t, SkipNotInGroup, e.Reason.String)
	for _, d := range members[1:] {
		require.Equal(t, 1, commandsFor(t, b, d))
		e := elementOf(t, b, "replicas", d)
		require.Equal(t, string(ElementDispatched), e.Status)
		require.Equal(t, token, e.BatchToken.String)
	}
}

// The campaign is stopped while the pass owns tokened claims but has not yet
// acquired the batch lock. Both operations must win against that stale pass.
func TestPostgresCampaignStoppedWithClaimsCreatesNoBatch(t *testing.T) {
	for _, operation := range []string{"delete", "disable"} {
		t.Run(operation, func(t *testing.T) {
			a, b, members := postgresCampaignReplicas(t)
			tick(t, a, at("2026-06-21T22:00:00Z"), "plan")
			campaignDispatchHook = func() { require.NoError(t, stopReplicaCampaign(b, operation)) }
			t.Cleanup(func() { campaignDispatchHook = nil })
			tick(t, a, campaignReplicaDue, "claimed")
			assertReplicaDispatch(t, a, members, 0)
		})
	}
}

func TestPostgresCampaignBatchLockOrdersStopAgainstDispatch(t *testing.T) {
	for _, operation := range []string{"delete", "disable"} {
		for _, bypass := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bypass=%v", operation, bypass), func(t *testing.T) {
				a, b, members := postgresCampaignReplicas(t)
				tick(t, a, at("2026-06-21T22:00:00Z"), "plan")
				setClock(a, campaignReplicaDue)
				setClock(b, campaignReplicaDue)
				poolB, err := b.RDB.Database.DB()
				require.NoError(t, err)
				poolB.SetMaxOpenConns(1)
				var stoppingPID int
				require.NoError(t, b.RDB.Database.Raw("SELECT pg_backend_pid()").Scan(&stoppingPID).Error)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				armed := atomic.Bool{}
				reached, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unpark := func() { once.Do(func() { close(release) }) }
				defer unpark()
				campaignDispatchHook = func() { armed.Store(true) }
				t.Cleanup(func() { campaignDispatchHook = nil })
				// A test-only seam skips exactly the dispatcher's tenant lock. All
				// other statements and the stopping replica retain production code.
				require.NoError(t, a.RDB.Database.Callback().Raw().Before("gorm:raw").Register("test:bypass-campaign-lock", func(tx *gorm.DB) {
					if bypass && armed.Load() && strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
						tx.Statement.SQL.Reset()
						tx.Statement.SQL.WriteString("SELECT 1")
						tx.Statement.Vars = nil
					}
				}))
				require.NoError(t, a.RDB.Database.Callback().Query().After("gorm:query").Register("test:park-campaign-fence", func(tx *gorm.DB) {
					if tx.Error == nil && tx.Statement.Table == "campaigns" && armed.CompareAndSwap(true, false) {
						close(reached)
						select {
						case <-release:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					}
				}))
				pass := make(chan error, 1)
				go func() { pass <- a.TickCampaigns(ctx, campaignReplicaDue, "held-fence") }()
				select {
				case <-reached:
				case <-ctx.Done():
					t.Fatal("dispatch never reached its fence")
				}
				stopped := make(chan error, 1)
				go func() { stopped <- stopReplicaCampaign(b, operation) }()
				if bypass {
					campaignResult(t, stopped)
					// The campaign is already gone/off, and no command exists yet.
					for _, d := range members {
						require.Zero(t, commandsFor(t, b, d))
					}
				} else {
					// Prove real overlap, rather than accepting a serial schedule.
					require.Eventually(t, func() bool {
						var waiting int64
						err := a.RDB.Database.Raw(`SELECT count(*) FROM pg_stat_activity
							WHERE pid = ? AND wait_event = 'advisory'
							AND query LIKE '%pg_advisory_xact_lock%'`, stoppingPID).Scan(&waiting).Error
						return err == nil && waiting > 0
					}, 10*time.Second, 10*time.Millisecond, "stop never waited on the dispatch lock")
					select {
					case err := <-stopped:
						t.Fatalf("stop returned before dispatch committed: %v", err)
					default:
					}
				}
				unpark()
				campaignResult(t, pass)
				if !bypass {
					campaignResult(t, stopped)
				}
				// With the lock this batch committed BEFORE stop returned. Without
				// it the same batch appears AFTER deletion/disable: the named control.
				assertReplicaDispatch(t, a, members, 1)
			})
		}
	}
}
