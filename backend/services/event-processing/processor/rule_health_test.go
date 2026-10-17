// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// blockingStatStore is a rule-health store whose writes can be held. Every call is announced on
// entered (when set); a held call waits for release or for its own context, unless ignoreCtx,
// the shape of a driver that cannot be interrupted.
type blockingStatStore struct {
	mu        sync.Mutex
	held      bool
	release   chan struct{}
	entered   chan string
	ignoreCtx bool
	// holdOnly, when set, holds only that tenant's writes.
	holdOnly string
	calls    []blockedCall
}

type blockedCall struct {
	tenant string
	deltas []model.RuleFireDelta
	err    error
}

func newBlockingStatStore(held bool) *blockingStatStore {
	return &blockingStatStore{held: held, release: make(chan struct{}), entered: make(chan string, 64)}
}

func (s *blockingStatStore) RecordFireBatch(ctx context.Context, tenant string, deltas []model.RuleFireDelta) (int, error) {
	select {
	case s.entered <- tenant:
	default:
	}
	s.mu.Lock()
	held := s.held && (s.holdOnly == "" || s.holdOnly == tenant)
	s.mu.Unlock()
	var err error
	if held {
		if s.ignoreCtx {
			<-s.release
		} else {
			select {
			case <-s.release:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, blockedCall{tenant: tenant, deltas: append([]model.RuleFireDelta(nil), deltas...), err: err})
	if err != nil {
		return 0, err
	}
	return len(deltas), nil
}

// committed sums what committed, per rule id.
func (s *blockingStatStore) committed() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for _, c := range s.calls {
		if c.err == nil {
			for _, d := range c.deltas {
				out[d.RuleID] += d.Count
			}
		}
	}
	return out
}

func healthConfig() rulehealth.Config {
	c := rulehealth.DefaultConfig()
	c.FlushInterval = rulehealth.MaxFlushInterval // only a full batch or a stop flushes
	c.MaxBatchRows = 1                            // so the first admission puts a write in flight
	c.WriteTimeout = rulehealth.MinWriteTimeout
	return c
}

func withHealthMetrics(rp *ResolvedEventsProcessor) *DetectMetrics {
	ms := &core.Microservice{InstanceId: "rh", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	rp.metrics = NewDetectMetrics(ms)
	return rp.metrics
}

func admit(rp *ResolvedEventsProcessor, tenant, rule string) {
	rp.health.RecordFire(context.Background(), tenant+"/prof@1/"+rule, tenant, time.Now(), "raised")
}

// A term's accumulator is the publisher's recorder only while the term runs: start installs it,
// a graceful stop flushes and joins it, and afterwards the publisher records nowhere and the old
// accumulator refuses everything.
func TestARuleHealthTermStartsAndStopsItsAccumulator(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	before := runtime.NumGoroutine()
	m := withHealthMetrics(rp)
	store := newBlockingStatStore(false)
	rp.ruleStats = store
	rp.cfg.RuleHealth = healthConfig()
	rp.cfg.RuleHealth.MaxBatchRows = 64 // nothing flushes before the stop

	require.NoError(t, rp.startRuleHealth())
	h := rp.health
	require.NotNil(t, h)
	admit(rp, "acme", "a")
	admit(rp, "acme", "a")
	rp.stopRuleHealth(true, context.Background())
	require.Nil(t, rp.health)
	require.Equal(t, map[string]int64{"acme/prof@1/a": 2}, store.committed(), "the graceful stop's final flush")

	h.RecordFire(context.Background(), "acme/prof@1/a", "acme", time.Now(), "raised")
	require.Equal(t, 1.0, testutil.ToFloat64(m.ruleStatDiscardedBy[rulehealth.DropClosed]),
		"an admission into a stopped term's accumulator is refused and counted")
	require.Equal(t, 2.0, testutil.ToFloat64(m.ruleStatWritten))
	rp.stopRuleHealth(true, context.Background()) // nil-safe, idempotent
	assertGoroutinesReturn(t, before)
}

// Without a store there is no accumulator, and every lifecycle call is a no-op.
func TestRuleHealthWithoutAStoreIsInert(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	require.NoError(t, rp.startRuleHealth())
	require.Nil(t, rp.health)
	n, err := rp.purgeRuleHealth("acme")
	require.NoError(t, err)
	require.Zero(t, n)
	rp.stopRuleHealth(true, context.Background())
}

// A loss stop discards what is pending and cancels the write in flight AT ONCE: it does not wait
// out a hung store, and the loss is counted in fires.
func TestALostTermDiscardsWithoutWaitingForTheStore(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	before := runtime.NumGoroutine()
	m := withHealthMetrics(rp)
	store := newBlockingStatStore(true)
	rp.ruleStats = store
	cfg := healthConfig()
	cfg.WriteTimeout = rulehealth.MaxWriteTimeout // the cancel, not the timeout, must end it
	rp.cfg.RuleHealth = cfg
	require.NoError(t, rp.startRuleHealth())
	admit(rp, "acme", "a")
	<-store.entered
	admit(rp, "acme", "b")

	start := time.Now()
	rp.stopRuleHealth(false, context.Background())
	require.Less(t, time.Since(start), 2*time.Second, "a lost term waited on the health store")
	require.Equal(t, 2.0, testutil.ToFloat64(m.ruleStatDiscardedBy[rulehealth.DropShutdown]))
	require.Empty(t, store.committed())
	assertGoroutinesReturn(t, before)
}

// The final flush is bounded by the shutdown deadline it is handed, less its headroom: a store
// that hangs cannot hold the stop past it.
func TestTheFinalFlushRespectsTheShutdownDeadline(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	withHealthMetrics(rp)
	store := newBlockingStatStore(true)
	rp.ruleStats = store
	cfg := healthConfig()
	cfg.MaxBatchRows = 64
	cfg.WriteTimeout = rulehealth.MaxWriteTimeout
	rp.cfg.RuleHealth = cfg
	require.NoError(t, rp.startRuleHealth())
	admit(rp, "acme", "a")
	rp.stopDeadline.Store(time.Now().Add(ruleHealthStopHeadroom + 300*time.Millisecond).UnixNano())

	start := time.Now()
	rp.stopRuleHealth(true, context.Background())
	require.Less(t, time.Since(start), 3*time.Second, "the final flush outlived its share of the shutdown deadline")
	require.Empty(t, store.committed())
}

// The unleased ExecuteStop (the scaffold path) flushes the term's counts after the loop exits.
func TestAnUnleasedStopFlushesTheCounts(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	withHealthMetrics(rp)
	store := newBlockingStatStore(false)
	rp.ruleStats = store
	rp.cfg.RuleHealth = healthConfig()
	rp.cfg.RuleHealth.MaxBatchRows = 64
	ctx, cancel := context.WithCancel(context.Background())
	rp.procCtx, rp.procCancel = ctx, cancel
	require.NoError(t, rp.startRuleHealth())
	admit(rp, "acme", "a")
	require.NoError(t, rp.ExecuteStop(context.Background()))
	require.Equal(t, map[string]int64{"acme/prof@1/a": 1}, store.committed())
	require.Nil(t, rp.health)
}

// 🔴 THE ERASURE ANSWER WAITS FOR THE STATS WRITE IN FLIGHT. A purge reaches the loop while a
// write of the tenant's fire counts is inside the store: the eviction is answered only once
// that write has settled, the tenant's pending counts are gone, the in-flight write counts as
// evicted (it may have landed, so the coordinator must sweep again), and another tenant's
// counts are untouched.
func TestAnEvictionSettlesTheTenantsStatsWriteInFlight(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "acme", "globex")
	rp := rig.rp
	store := newBlockingStatStore(true)
	store.holdOnly = "acme"
	rp.ruleStats = store
	rp.cfg.RuleHealth = healthConfig()
	require.NoError(t, rp.startRuleHealth())
	t.Cleanup(func() { rp.stopRuleHealth(false, context.Background()) })
	admit(rp, "acme", "a")
	require.Equal(t, "acme", <-store.entered, "acme's write is in flight")
	admit(rp, "acme", "b")
	admit(rp, "globex", "a")
	rig.start(t)

	evicted, err := rig.evict(t, "acme")
	require.NoError(t, err)
	// The engine's own state for acme plus three stats entries: two pending, one in flight.
	require.GreaterOrEqual(t, evicted, int64(3))
	store.mu.Lock()
	acmeCalls := 0
	for _, c := range store.calls {
		if c.tenant == "acme" {
			acmeCalls++
			require.Error(t, c.err, "acme's in-flight write was not cancelled")
		}
	}
	store.mu.Unlock()
	require.Equal(t, 1, acmeCalls, "the in-flight write had not returned when the eviction was answered")

	rig.stop()
	rp.stopRuleHealth(true, context.Background())
	committed := store.committed()
	require.Equal(t, int64(1), committed["globex/prof@1/a"], "the other tenant's count")
	for id := range committed {
		require.False(t, strings.HasPrefix(id, "acme/"), "a purged tenant's count was written: %s", id)
	}
}

// If the in-flight write cannot be settled (a driver that ignores cancellation), the eviction
// is answered with an ERROR, never as clean.
func TestAnEvictionWhoseStatsWriteWillNotSettleIsAnError(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "acme")
	rp := rig.rp
	store := newBlockingStatStore(true)
	store.ignoreCtx = true
	rp.ruleStats = store
	rp.cfg.RuleHealth = healthConfig()
	require.NoError(t, rp.startRuleHealth())
	t.Cleanup(func() {
		close(store.release)
		rp.stopRuleHealth(false, context.Background())
	})
	admit(rp, "acme", "a")
	<-store.entered
	rig.start(t)

	_, err := rig.evict(t, "acme")
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule-health")
}

func assertGoroutinesReturn(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), before, "goroutines outlived the stop")
}

// Through the real term teardown on a real lease: a GRACEFUL stop (ExecuteStop) releases the
// partition and then flushes the term's counts; a LOST term (its context cancelled with no stop
// under way, which is what an expiry or a stale checkpoint produces) discards them. Both join
// the accumulator before the teardown returns.
func TestEndTermFlushesOnAStopAndDiscardsOnALoss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		graceful bool
	}{{"graceful stop", true}, {"lost term", false}} {
		t.Run(tc.name, func(t *testing.T) {
			b := startOutageBroker(t)
			rp := b.liveTerm(t)
			store := newBlockingStatStore(false)
			rp.ruleStats = store
			rp.cfg.RuleHealth = healthConfig()
			rp.cfg.RuleHealth.MaxBatchRows = 64
			require.NoError(t, rp.startRuleHealth())
			h := rp.health
			admit(rp, "acme", "a")

			if tc.graceful {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				require.NoError(t, rp.ExecuteStop(ctx)) // joins the supervisor, and so endTerm
			} else {
				rp.pcancel()
				rp.supWG.Wait() // the supervisor ran this one term's endTerm and exited
			}
			b.leaderExits()
			h.RecordFire(context.Background(), "acme/prof@1/z", "acme", time.Now(), "raised")
			require.Equal(t, 1.0, testutil.ToFloat64(rp.metrics.ruleStatDiscardedBy[rulehealth.DropClosed]),
				"the ended term's accumulator still admitted a fire")
			if tc.graceful {
				require.Equal(t, map[string]int64{"acme/prof@1/a": 1}, store.committed())
				require.Zero(t, testutil.ToFloat64(rp.metrics.ruleStatDiscardedBy[rulehealth.DropShutdown]))
			} else {
				require.Empty(t, store.committed(), "a lost term wrote its counts")
				require.Equal(t, 1.0, testutil.ToFloat64(rp.metrics.ruleStatDiscardedBy[rulehealth.DropShutdown]))
			}
		})
	}
}

// 🔴 THE FINAL FLUSH RUNS AFTER THE LEASE IS RELEASED. A graceful stop whose final rule-health
// write is held inside the store has already handed the partition over: a successor reads a
// clean release and acquires while that write is still in flight. A health projection must
// never hold the partition from the next leader.
func TestTheFinalFlushDoesNotHoldTheLease(t *testing.T) {
	b := startOutageBroker(t)
	rp := b.liveTerm(t)
	store := newBlockingStatStore(true)
	rp.ruleStats = store
	cfg := healthConfig()
	cfg.MaxBatchRows = 64 // nothing is written before the stop
	cfg.WriteTimeout = rulehealth.MaxWriteTimeout
	rp.cfg.RuleHealth = cfg
	require.NoError(t, rp.startRuleHealth())
	admit(rp, "acme", "a")

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stopped <- rp.ExecuteStop(ctx)
	}()
	select {
	case <-store.entered: // the final flush is inside the store, and held there
	case <-time.After(15 * time.Second):
		t.Fatal("the graceful stop never made its final flush")
	}
	clean, err := b.successor(t, rp.cfg.PartitionId)
	close(store.release)
	require.NoError(t, <-stopped)
	require.NoError(t, err, "the successor could not acquire while the final flush was in flight")
	require.True(t, clean, "the lease was not released cleanly before the final flush")
	require.Equal(t, map[string]int64{"acme/prof@1/a": 1}, store.committed())
}
