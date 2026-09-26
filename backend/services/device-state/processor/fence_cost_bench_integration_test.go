// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The erasure fence on device-state's per-event write path, against a REAL TimescaleDB:
// one test that proves the fence refuses there (sqlite proves it in the unit suite, and
// sqlite is not the server this runs on), and a benchmark that times the path with the
// fence registered and removed.
//
// 🔴 THE TEST IS NOT DECORATION. hack/integration-tests.sh runs every module carrying an
// integration-tagged file and FAILS a module whose tagged run starts no tests; a
// benchmark does not run without -bench, so a file holding only the benchmark would turn
// that job red for this module.
//
// Run the benchmark against the operand image (trust auth, so the password is ignored):
//
//	. deploy/images/timescaledb/standalone.sh
//	PORT=$(dc_operand_start ghcr.io/devicechain-io/postgresql-timescaledb:17.10-ts2.28.3-r1 \
//	         dc-fencebench "127.0.0.1::5432")
//	cd backend/services/device-state
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' -bench FenceCost \
//	  -benchtime=2000x -count=6 -p 1 ./processor/
package processor

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// The fence's callback names, as core/rdb/tenant_fence.go registers them. Stated here
// rather than exported from core on purpose: an exported "unregister the fence" is a
// foot-gun in a shipped package. The harness below proves each removal took effect, so a
// rename in core fails here by name instead of silently measuring "on" twice.
var fenceCallbacks = []struct {
	name string
	get  func(*gorm.DB) func(*gorm.DB)
	rm   func(*gorm.DB) error
}{
	{"dc:tenant_fence_create",
		func(db *gorm.DB) func(*gorm.DB) { return db.Callback().Create().Get("dc:tenant_fence_create") },
		func(db *gorm.DB) error { return db.Callback().Create().Remove("dc:tenant_fence_create") }},
	{"dc:tenant_fence_update",
		func(db *gorm.DB) func(*gorm.DB) { return db.Callback().Update().Get("dc:tenant_fence_update") },
		func(db *gorm.DB) error { return db.Callback().Update().Remove("dc:tenant_fence_update") }},
}

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newFenceBenchManager runs device-state's REAL migration chain and REAL callback chain
// (ExecuteInitialize) against database instance, then puts a statement counter on the
// session every write goes through. With fenced false it removes the fence's callbacks —
// from THIS manager's gorm instance only, which is why each leg gets a database of its
// own and a removal cannot leak into the other.
//
// It is a local copy of the helper event-management's benchmark carries, kept
// byte-similar: rdbtest cannot import rdb, and the model packages' helpers are test-only.
func newFenceBenchManager(tb testing.TB, instance string, fenced bool) (*rdb.RdbManager, *rdbtest.StatementCounter) {
	tb.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		tb.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "postgres")
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		tb.Fatalf("create the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "device-state"},
		Migrations:   model.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		tb.Fatalf("run migrations on the real server: %v", err)
	}
	tb.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	counter := rdbtest.NewStatementCounter(rdb.FenceTable)
	counter.Arm(false)
	mgr.Database = mgr.Database.Session(&gorm.Session{Logger: counter})
	for _, cb := range fenceCallbacks {
		if cb.get(mgr.Database) == nil {
			tb.Fatalf("callback %q is not registered: core renamed or dropped it, so this harness "+
				"no longer knows how to turn the fence off", cb.name)
		}
		if fenced {
			continue
		}
		if err := cb.rm(mgr.Database); err != nil {
			tb.Fatalf("removing %q: %v", cb.name, err)
		}
		if cb.get(mgr.Database) != nil {
			tb.Fatalf("callback %q is still registered after its removal", cb.name)
		}
	}
	return mgr, counter
}

// newBenchProcessor wraps a manager in a StateProcessor built by the real constructor.
func newBenchProcessor(mgr *rdb.RdbManager) *StateProcessor {
	return NewStateProcessor(deviceStateMicroservice, nil, core.NewNoOpLifecycleCallbacks(),
		model.NewApi(mgr), NewStateMetrics(deviceStateMicroservice),
		deviceStateMicroservice.NewPeriodicTaskMetrics("fence_bench"))
}

// benchMeasurement is a consumed 3-metric measurement for device under tenant.
func benchMeasurement(tb testing.TB, tenant, device string, at time.Time, ack messaging.Acknowledger) messaging.Message {
	tb.Helper()
	event := &dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: device,
		EventType:         esmodel.Measurement,
		OccurredTime:      at,
		ProcessedTime:     at,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries: []dmmodel.ResolvedMeasurementEntry{
				{Name: "temperature", Value: "21.5"},
				{Name: "humidity", Value: "40.25"},
				{Name: "pressure", Value: "1013.5"},
			},
		}}},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		tb.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.NewConsumedMessage("instance1."+tenant+".resolved-events", encoded, 1, nil, ack)
}

// plantOn stands a fence for tenant in this manager's area, under a system context.
func plantOn(tb testing.TB, mgr *rdb.RdbManager, tenant string) {
	tb.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := mgr.DB(core.WithSystemContext(context.Background())).
		Create(&rdb.PurgedTenant{Token: tenant, Epoch: now, PlantedAt: now}).Error; err != nil {
		tb.Fatalf("plant a fence for %q: %v", tenant, err)
	}
}

func liftOn(tb testing.TB, mgr *rdb.RdbManager, tenant string) {
	tb.Helper()
	if err := mgr.DB(core.WithSystemContext(context.Background())).Model(&rdb.PurgedTenant{}).
		Where("token = ? AND completed_at IS NULL", tenant).
		Update("completed_at", time.Now().UTC()).Error; err != nil {
		tb.Fatalf("lift the fence for %q: %v", tenant, err)
	}
}

// medianRTT is the median of 1000 `SELECT 1` round trips on the manager's own pool.
func medianRTT(tb testing.TB, mgr *rdb.RdbManager) float64 {
	tb.Helper()
	sqldb, err := mgr.Database.DB()
	if err != nil {
		tb.Fatalf("pool: %v", err)
	}
	samples := make([]int64, 1000)
	var one int
	for i := range samples {
		start := time.Now()
		if err := sqldb.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
			tb.Fatalf("SELECT 1: %v", err)
		}
		samples[i] = time.Since(start).Nanoseconds()
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return float64(samples[len(samples)/2])
}

// The fence refuses a device-state write on the real server, over the real migration
// chain and callback chain: the event is left unacked, the projection does not move, and
// exactly one fence read is made. Lifting the fence is the negative control.
func TestTheFenceRefusesADeviceStateWriteOnPostgres(t *testing.T) {
	mgr, counter := newFenceBenchManager(t, "dsfencetest", true)
	sp := newBenchProcessor(mgr)
	// A tenant of its own per run, so a fence a previous run left standing cannot decide
	// this one.
	tenant := fmt.Sprintf("fr%d", time.Now().UnixNano())
	t0 := time.Now().UTC().Truncate(time.Second)
	t1 := t0.Add(time.Minute)

	ack := &recordingAck{}
	sp.mergeOne(context.Background(), benchMeasurement(t, tenant, "fr-device", t0, ack))
	if ack.n.Load() != 1 {
		t.Fatalf("the seeding merge was not acked")
	}
	lastActivity := func() time.Time {
		var ds model.DeviceState
		if err := mgr.DB(core.WithTenant(context.Background(), tenant)).
			Where("device_token = ?", "fr-device").First(&ds).Error; err != nil {
			t.Fatalf("load device state: %v", err)
		}
		return ds.LastActivityTime.Time
	}

	plantOn(t, mgr, tenant)
	ack = &recordingAck{}
	counter.Reset()
	counter.Arm(true)
	sp.mergeOne(context.Background(), benchMeasurement(t, tenant, "fr-device", t1, ack))
	counter.Arm(false)
	all, fence := counter.Counts()
	if ack.n.Load() != 0 {
		t.Fatalf("a merge for a fenced tenant was acked")
	}
	if all != 2 || fence != 1 {
		t.Errorf("a refused merge made %d statements with %d fence reads; want 2 with 1", all, fence)
	}
	if got := lastActivity(); !got.Equal(t0) {
		t.Errorf("LastActivityTime moved to %v under a standing fence; want %v", got, t0)
	}

	liftOn(t, mgr, tenant)
	ack = &recordingAck{}
	sp.mergeOne(context.Background(), benchMeasurement(t, tenant, "fr-device", t1, ack))
	if ack.n.Load() != 1 {
		t.Fatalf("with the fence lifted the merge was not acked")
	}
	if got := lastActivity(); !got.Equal(t1) {
		t.Errorf("with the fence lifted LastActivityTime = %v, want %v", got, t1)
	}
}

// BenchmarkFenceCostMergeMeasurementEvent times mergeOne on a 3-metric measurement with
// the fence on and off, for a newer event (every row updated) and a stale one (only the
// device-state row saved).
//
// It reports only what it measured: ns/op, statements and fence reads per op (counted on
// this server during calibration), and the median loopback round trip. Round trips per
// op add two per transaction to the statements (gorm does not trace BEGIN/COMMIT); a
// merge opens two, one per projection.
func BenchmarkFenceCostMergeMeasurementEvent(b *testing.B) {
	const tenant = "tenant1"
	// The refusal probe below logs the refusal it provokes, and a log line written while a
	// sub-benchmark runs lands on the same output line as its name, where benchstat can no
	// longer attribute the result. The processor's logging is silenced for the run.
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	// Both managers are opened here, in the parent, and NOT inside a sub-benchmark: the
	// runner calls each sub-benchmark body at least twice (N=1, then the target N, and
	// again for every -count), which would run the migration chain each time. Note that
	// -count repeats each sub-benchmark back to back rather than interleaving the legs, so
	// host drift over the run is not cancelled between on and off.
	legs := map[string]*StateProcessor{}
	counters := map[string]*rdbtest.StatementCounter{}
	mgrs := map[string]*rdb.RdbManager{}
	for _, leg := range []struct {
		name   string
		db     string
		fenced bool
	}{{"on", "dsfenceon", true}, {"off", "dsfenceoff", false}} {
		mgr, counter := newFenceBenchManager(b, leg.db, leg.fenced)
		legs[leg.name], counters[leg.name], mgrs[leg.name] = newBenchProcessor(mgr), counter, mgr
	}

	for _, c := range []struct {
		name      string
		wantFence int64 // fence reads per op with the fence on
	}{{"newer", 4}, {"stale", 1}} {
		for _, fence := range []string{"on", "off"} {
			b.Run("case="+c.name+"/fence="+fence, func(b *testing.B) {
				sp, counter, mgr := legs[fence], counters[fence], mgrs[fence]
				device := fmt.Sprintf("fb-%d", time.Now().UnixNano())
				t0 := time.Now().UTC().Truncate(time.Second)
				ack := &recordingAck{}
				merge := func(m messaging.Message) error {
					before := ack.n.Load()
					sp.mergeOne(context.Background(), m)
					if ack.n.Load() != before+1 {
						return fmt.Errorf("the merge was not acked")
					}
					return nil
				}
				if err := merge(benchMeasurement(b, tenant, device, t0, ack)); err != nil {
					b.Fatalf("seed: %v", err)
				}
				at := func(i int) time.Time {
					if c.name == "stale" {
						return t0.Add(-time.Hour)
					}
					return t0.Add(time.Duration(i+1) * time.Second)
				}

				// 1. Calibration: the fence reads per op must be what this leg claims.
				want := c.wantFence
				if fence == "off" {
					want = 0
				}
				stmts, err := counter.Calibrate(3, func(i int) error {
					return merge(benchMeasurement(b, tenant, device, at(i), ack))
				}, want)
				if err != nil {
					b.Fatalf("fence=%s calibration: %v", fence, err)
				}

				// 2. Refusal probe: a standing fence for a sacrificial tenant must refuse on
				// the on leg and must NOT on the off leg — the second half is what proves
				// the off leg is off rather than failing for some other reason.
				probe := fmt.Sprintf("fencedprobe%d", time.Now().UnixNano())
				plantOn(b, mgr, probe)
				pack := &recordingAck{}
				sp.mergeOne(context.Background(), benchMeasurement(b, probe, "probe", t0, pack))
				if refused := pack.n.Load() == 0; refused != (fence == "on") {
					b.Fatalf("fence=%s: refusal probe refused=%v", fence, refused)
				}
				liftOn(b, mgr, probe)

				// 3. The loopback round trip, for reading ns/op against.
				rtt := medianRTT(b, mgr)

				msgs := make([]messaging.Message, b.N)
				for i := range msgs {
					msgs[i] = benchMeasurement(b, tenant, device, at(10+i), ack)
				}
				start := ack.n.Load()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					sp.mergeOne(context.Background(), msgs[i])
				}
				b.StopTimer()
				if got := ack.n.Load() - start; int(got) != b.N {
					b.Fatalf("only %d of %d merges were acked; a refused write would time as a fast one", got, b.N)
				}
				b.ReportMetric(stmts, "stmts/op")
				b.ReportMetric(float64(want), "fence/op")
				b.ReportMetric(rtt, "rtt-ns")
			})
		}
	}
}
