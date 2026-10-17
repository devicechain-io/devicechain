// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The DETECT output-path baseline on a real PostgreSQL and a real JetStream.
//
// sqlite cannot stand in for the rule-health write: the fence read and the upsert are two round
// trips to a server, and an approach that batches them later has to be compared against those
// round trips, not against an in-process call. So the rule-health store and the checkpoint store
// here run the REAL event-processing migrations through the REAL core/rdb path (whose callback
// chain is the production one), and the derived events go to a real JetStream: one embedded
// server, and a three-node replicated cluster.
//
// Tagged `integration` so it stays out of the default `go test ./...`. Against a throwaway server
// (hack/integration-tests.sh does exactly this for the tests):
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	export DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://')
//	go test -tags integration -count=1 ./processor -run Postgres -v
//	go test -tags integration ./processor -run '^$' -bench BenchmarkDetectOutputBaselineReal \
//	  -benchtime 2000x -count 7 -benchmem | tee a-real.txt
package processor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func detperfEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// detperfPostgres opens one event-processing store manager on the real server, migrated, with the
// rule-health, checkpoint and fence tables emptied. Each call is its OWN manager (its own pool
// and gorm configuration), so the rule-health store's statement timer sees its statements alone.
func detperfPostgres(tb testing.TB) *rdb.RdbManager {
	tb.Helper()
	port, err := strconv.Atoi(detperfEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		tb.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := detperfEnv("DC_IT_PGHOST", "localhost"), detperfEnv("DC_IT_PGUSER", "postgres"), detperfEnv("DC_IT_PGPASSWORD", "postgres")
	const instance = "dcdetperf"
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		tb.Fatalf("creating the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"},
		Migrations:   model.Migrations,
		InstanceConfig: mscfg.DatastoreConfiguration{
			Type:          "postgres95",
			Configuration: map[string]interface{}{"hostname": host, "port": port, "username": user, "password": pass},
		},
		MicroserviceConfig: mscfg.MicroserviceDatastoreConfiguration{},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		tb.Fatalf("run the event-processing migrations on the real server: %v", err)
	}
	tb.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	for _, table := range []string{"rule_stats", "detect_snapshots", rdb.FenceTable} {
		if err := mgr.Database.Exec(`TRUNCATE TABLE "event-processing".` + table).Error; err != nil {
			tb.Fatalf("truncate %s: %v", table, err)
		}
	}
	return mgr
}

// detperfStats is the real rule-health store, its statements timed when timer is set.
func detperfStats(tb testing.TB, timer *statementTimer) *model.RuleStatStore {
	tb.Helper()
	mgr := detperfPostgres(tb)
	if timer != nil {
		mgr.Database.Config.Logger = timer
	}
	return model.NewRuleStatStore(mgr)
}

var detperfInstances atomic.Int32

// detperfWriter is a production derived-event writer for a fresh instance on srv, with replicas
// stream replicas; its stream is deleted when the run ends.
func detperfWriter(tb testing.TB, srv *natsserver.Server, replicas int) messaging.MessageWriter {
	tb.Helper()
	w, _ := detperfWriterNamed(tb, srv, replicas)
	return w
}

// detperfWriterNamed is detperfWriter, also naming the instance so the stream can be read back.
func detperfWriterNamed(tb testing.TB, srv *natsserver.Server, replicas int) (messaging.MessageWriter, string) {
	tb.Helper()
	u, err := url.Parse(srv.ClientURL())
	require.NoError(tb, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(tb, err)
	instance := fmt.Sprintf("detperf%04d", detperfInstances.Add(1))
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port), StreamReplicas: uint32(replicas),
		StreamMaxBytes: 64 << 20, StreamMaxBytesCold: 16 << 20,
	}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(tb, nmgr.Initialize(context.Background()))
	require.NoError(tb, nmgr.Start(context.Background()))
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(tb, err)
	tb.Cleanup(func() {
		if nc, err := nats.Connect(srv.ClientURL()); err == nil {
			if js, err := nc.JetStream(); err == nil {
				_ = js.DeleteStream(messaging.StreamName(instance, streams.DerivedEvents))
			}
			nc.Close()
		}
		_ = nmgr.Stop(context.Background())
	})
	return w, instance
}

// detperfServer is one embedded JetStream server for a whole benchmark.
func detperfServer(tb testing.TB) *natsserver.Server {
	tb.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(tb),
		JetStreamMaxStore: 4 << 30, JetStreamMaxMemory: 256 << 20, NoLog: true, NoSigs: true,
	})
	require.NoError(tb, err)
	go srv.Start()
	require.True(tb, srv.ReadyForConnections(10*time.Second), "embedded JetStream server not ready")
	tb.Cleanup(srv.Shutdown)
	return srv
}

// The positive control on the real stores: the baseline passes the oracle with the rule-health
// write and the checkpoint on PostgreSQL and the derived events on JetStream, every fire is one
// fence read and one upsert on the server, and the rows written match the source.
func TestTheOutputOracleOnPostgresAndJetStream(t *testing.T) {
	b := startDetectBroker(t)
	nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)

	timer := &statementTimer{}
	stats := detperfStats(t, timer)
	timer.fenceN.Store(0)
	timer.otherN.Store(0)
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: w, stats: stats, snapshots: detperfPostgres(t),
		variant: variantA, checkpointEvents: oracleCheckpoint})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))

	v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
	require.True(t, v.clean(), "the baseline failed the oracle on the real stores: %s", v)
	require.Equal(t, len(oracleFixture.expectedDetections(oracleInputs)), v.published)
	require.Equal(t, v.published, v.stats)
	require.Equal(t, int64(v.published), timer.fenceN.Load(), "fence reads on the server, one per fire")
	require.Equal(t, int64(v.published), timer.otherN.Load(), "upserts on the server, one per fire")
	sv := judgeOutput(oracleFixture, oracleInputs, storedDerived(t, b))
	require.Equal(t, v.published, sv.published)
	require.Empty(t, sv.ordering, "%s", sv)

	fires := map[string]int64{}
	for _, k := range oracleFixture.expectedDetections(oracleInputs) {
		fires[k.rule]++
	}
	for _, tenant := range oracleFixture.tenants {
		ids := []string{oracleFixture.ruleID(tenant, 0), oracleFixture.ruleID(tenant, 1)}
		got, err := stats.LoadByIDs(context.Background(), tenant, ids)
		require.NoError(t, err)
		for _, id := range ids {
			require.Equal(t, fires[id], got[id].FireCount, "fire count of %s on PostgreSQL", id)
		}
	}
}

// BenchmarkDetectOutputBaselineReal is BenchmarkDetectOutputBaseline over the real stores: the
// rule-health write and the checkpoint on PostgreSQL with the production callbacks, the derived
// events on a real JetStream, R1 (one embedded server) or R3 (an in-process three-node cluster,
// stream replicas 3). In-process over loopback, so the absolute figures are a floor; what it
// answers is which of the loop's waits dominates each detection.
func BenchmarkDetectOutputBaselineReal(b *testing.B) {
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
	pg := func(split bool) func(b *testing.B) (fireStatStore, *statementTimer) {
		return func(b *testing.B) (fireStatStore, *statementTimer) {
			var timer *statementTimer
			if split {
				timer = &statementTimer{}
			}
			return detperfStats(b, timer), timer
		}
	}
	snapshots := func(b *testing.B) *rdb.RdbManager { return detperfPostgres(b) }
	for _, topology := range []string{"R1", "R3"} {
		for _, c := range []outputBenchCase{
			{name: "fire=100%/lanes=32/stats=postgres", fx: benchFixture(16, 1), stats: pg(false)},
			{name: "fire=100%/lanes=32/stats=postgres/split=fence", fx: benchFixture(16, 1), stats: pg(true)},
			{name: "fire=100%/lanes=32/stats=none", fx: benchFixture(16, 1), noStats: true},
			{name: "fire=10%/lanes=32/stats=postgres", fx: benchFixture(16, 10), stats: pg(false)},
			{name: "fire=0%/lanes=32/stats=postgres", fx: benchFixture(16, 0), stats: pg(false)},
		} {
			c.broker = broker(topology)
			c.snapshots = snapshots
			b.Run("topology="+topology+"/"+c.name, func(b *testing.B) { benchOutput(b, c) })
		}
	}
}
