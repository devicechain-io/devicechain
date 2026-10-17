// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Variant B (the rule-health counters coalesced and written in the background) on a real
// PostgreSQL and a real JetStream, beside variant A. Run as detect_output_integration_test.go
// says:
//
//	go test -tags integration -count=1 ./processor -run 'VariantB|InFlightStatsBatch' -v
//	go test -tags integration ./processor -run '^$' -bench BenchmarkDetectOutputStatsReal \
//	  -benchtime 2000x -count 7 -benchmem | tee ab-real.txt
package processor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The positive control on the real stores: B passes the oracle with PostgreSQL behind both the
// counters and the checkpoint and JetStream behind the derived events; the counters cost one
// fence read and one INSERT per tenant batch on the server, not per fire; and the table holds
// exactly what the source implies.
func TestVariantBOnPostgresAndJetStream(t *testing.T) {
	b := startDetectBroker(t)
	nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)

	timer := &statementTimer{}
	stats := detperfStats(t, timer)
	timer.fenceN.Store(0)
	timer.otherN.Store(0)
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: w, batch: stats, snapshots: detperfPostgres(t),
		variant: variantB, checkpointEvents: oracleCheckpoint})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))

	v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
	require.True(t, v.clean(), "variant B failed the oracle on the real stores: %s", v)
	require.Equal(t, len(oracleFixture.expectedDetections(oracleInputs)), v.published)
	require.Equal(t, v.published, v.stats)
	sv := judgeOutput(oracleFixture, oracleInputs, storedDerived(t, b))
	require.Equal(t, v.published, sv.published)
	require.Empty(t, sv.ordering, "%s", sv)

	writes, _ := histogramStats(t, r.metrics.ruleStatWrite)
	require.Equal(t, int64(writes), timer.fenceN.Load(), "one fence read per tenant batch on the server")
	require.Equal(t, int64(writes), timer.otherN.Load(), "one INSERT per tenant batch on the server")
	require.Less(t, int64(writes), int64(v.published)/10, "B wrote nearly a row per fire")
	t.Logf("%d acknowledged detections, %d tenant batches", v.published, writes)
	requireStatsEqual(t, expectedRuleStats(oracleFixture, oracleInputs), loadFixtureStats(t, oracleFixture, stats), "variant B on PostgreSQL")
}

// rowLockHolder holds one rule_stats row in its own transaction, so a batch that reaches it
// waits on the server after its fence read: a write genuinely in flight.
func rowLockHolder(t *testing.T, db *gorm.DB, id string) *gorm.DB {
	t.Helper()
	tx := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background())).Begin()
	require.NoError(t, tx.Exec(`SELECT 1 FROM "event-processing".rule_stats WHERE rule_id = ? FOR UPDATE`, id).Error)
	return tx
}

func waitForStatLockWait(t *testing.T, db *gorm.DB) {
	t.Helper()
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n).Error)
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the stats batch never reached the held row")
}

// 🔴 A PURGE DURING AN IN-FLIGHT STATS BATCH DOES NOT RESURRECT ROWS. The accumulator's write for
// a tenant is inside PostgreSQL (past its fence read, waiting on a row lock) when the purge
// plants the tenant's fence and asks DETECT to evict. Purge cancels that write and returns only
// once it has rolled back, so the purge's sweep removes the tenant's row and nothing writes it
// again: not the cancelled batch, and not a fire admitted afterwards, which the store's fence
// refuses. Another tenant's counts keep landing throughout.
func TestAPurgeDuringAnInFlightStatsBatchDoesNotResurrectRows(t *testing.T) {
	mgr := detperfPostgres(t)
	store := model.NewRuleStatStore(mgr)
	db := mgr.Database
	ms := &core.Microservice{InstanceId: "rhpurge", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := NewDetectMetrics(ms)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	const id, other = "acme/p@1/hot", "beta/p@1/hot"

	_, err := store.RecordFireBatch(ctx, "acme", []model.RuleFireDelta{{RuleID: id, Count: 1, LastFiredAt: t0, LastEdge: "raised"}})
	require.NoError(t, err)
	cfg := rulehealth.DefaultConfig()
	cfg.MaxBatchRows = 1 // every admission is a ready batch
	cfg.FlushInterval = rulehealth.MinFlushInterval
	cfg.WriteTimeout = 30 * time.Second // only the purge's cancel can end the held write
	acc, err := rulehealth.New(store, cfg, m)
	require.NoError(t, err)
	acc.Start()
	t.Cleanup(func() { acc.Stop(ctx, false) })

	holder := rowLockHolder(t, db, id)
	acc.RecordFire(ctx, id, "acme", t0.Add(time.Minute), "resolved")
	waitForStatLockWait(t, db)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(ctx))
	require.NoError(t, sys.Create(&rdb.PurgedTenant{Token: "acme", Epoch: now, PlantedAt: now}).Error)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	removed, err := acc.Purge(pctx, "acme")
	cancel()
	require.NoError(t, err, "the in-flight write did not settle")
	require.Equal(t, int64(1), removed, "the in-flight entry counts as evicted")
	require.NoError(t, holder.Rollback().Error)

	// The purge's sweep, then fires after the purge: acme's refused by the fence, beta's written.
	require.NoError(t, sys.Where("tenant = ?", "acme").Delete(&model.RuleStat{}).Error)
	acc.RecordFire(ctx, id, "acme", t0.Add(2*time.Minute), "raised")
	acc.RecordFire(ctx, other, "beta", t0, "raised")
	require.Zero(t, acc.Stop(ctx, true), "the final flush dropped fires")

	got, err := store.LoadByIDs(ctx, "acme", []string{id})
	require.NoError(t, err)
	require.Empty(t, got, "the purged tenant's row came back")
	got, err = store.LoadByIDs(ctx, "beta", []string{other})
	require.NoError(t, err)
	require.Equal(t, int64(1), got[other].FireCount, "the other tenant's count")
	require.Equal(t, 2.0, testutil.ToFloat64(m.ruleStatDiscardedBy[rulehealth.DropPurged]),
		"the cancelled in-flight fire and the fence-refused one")
	require.Zero(t, testutil.ToFloat64(m.ruleStatWriteFailures), "a purge is not a failing store")
}

// BenchmarkDetectOutputStatsReal is BenchmarkDetectOutputStats over the real stores: PostgreSQL
// (production callback chain) behind the counters and the checkpoint, a real JetStream behind
// the derived events, R1 or R3. A and B run inside the same parent so -count interleaves them.
func BenchmarkDetectOutputStatsReal(b *testing.B) {
	parent := b
	var single *natsserver.Server
	var cluster []*natsserver.Server
	broker := func(topology string) func(b *testing.B) messaging.MessageWriter {
		return func(b *testing.B) messaging.MessageWriter {
			if topology == "R1" {
				if single == nil {
					single = detperfServer(parent)
				}
				return detperfWriter(b, single, 1)
			}
			if cluster == nil {
				cluster = dctest.StartJetStreamCluster(parent, 3)
			}
			return detperfWriter(b, cluster[0], 3)
		}
	}
	// One migrated manager for each store, for the whole benchmark: migrating per run costs ~7s
	// and would dominate the wall time of 84 runs. Each run starts from empty tables and gives
	// the counters' manager a fresh statement timer.
	var statsMgr, snapMgr *rdb.RdbManager
	reset := func(b *testing.B) {
		if statsMgr == nil {
			statsMgr, snapMgr = detperfPostgres(parent), detperfPostgres(parent)
		}
		for _, table := range []string{"rule_stats", "detect_snapshots", rdb.FenceTable} {
			if err := snapMgr.Database.Exec(`TRUNCATE TABLE "event-processing".` + table).Error; err != nil {
				b.Fatalf("truncate %s: %v", table, err)
			}
		}
	}
	pg := func(b *testing.B) (fireStatStore, *statementTimer) {
		reset(b)
		timer := &statementTimer{}
		statsMgr.Database.Config.Logger = timer
		return model.NewRuleStatStore(statsMgr), timer
	}
	snapshots := func(b *testing.B) *rdb.RdbManager { reset(b); return snapMgr }
	for _, topology := range []string{"R1", "R3"} {
		for _, c := range []outputBenchCase{
			{name: "fire=100%/lanes=32", fx: benchFixture(16, 1)},
			{name: "fire=10%/lanes=32", fx: benchFixture(16, 10)},
			{name: "fire=0%/lanes=32", fx: benchFixture(16, 0)},
		} {
			c.broker = broker(topology)
			c.snapshots = snapshots
			c.stats = pg
			for _, v := range benchVariantOrder() {
				c := c
				c.batched = v == "B"
				b.Run("topology="+topology+"/variant="+v+"/"+c.name, func(b *testing.B) { benchOutput(b, c) })
			}
		}
	}
}

// benchVariantOrder is A then B, or B then A with DETPERF_ORDER=BA: the order control. A run
// that follows another on the same database and broker can inherit its state (the database's
// background work, the cluster's replication), so a difference that follows POSITION rather
// than variant is the environment, not the change.
func benchVariantOrder() []string {
	if os.Getenv("DETPERF_ORDER") == "BA" {
		return []string{"B", "A"}
	}
	return []string{"A", "B"}
}
