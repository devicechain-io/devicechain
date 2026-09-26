// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The erasure fence on event-management's per-message write path, against a REAL
// TimescaleDB: one test that proves the fence refuses there, and a benchmark that times
// the path with the fence registered and removed.
//
// The test is not decoration: hack/integration-tests.sh fails a module whose tagged run
// starts no tests, and a benchmark does not run without -bench. (This module has other
// tagged files today; the test keeps this one honest on its own.)
//
// Run the benchmark against the operand image (trust auth, so the password is ignored):
//
//	. deploy/images/timescaledb/standalone.sh
//	PORT=$(dc_operand_start ghcr.io/devicechain-io/postgresql-timescaledb:17.10-ts2.28.3-r1 \
//	         dc-fencebench "127.0.0.1::5432")
//	cd backend/services/event-management
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' -bench FenceCost \
//	  -benchtime=2000x -count=6 -p 1 ./processor/
package processor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
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

// newFenceBenchManager runs event-management's REAL migration chain and REAL callback
// chain (ExecuteInitialize) against database instance, then puts a statement counter on
// the session every write goes through. With fenced false it removes the fence's
// callbacks — from THIS manager's gorm instance only, which is why each leg gets a
// database of its own and a removal cannot leak into the other.
//
// It is a local copy of the helper device-state's benchmark carries, kept byte-similar:
// rdbtest cannot import rdb, and the model package's helper is test-only.
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
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
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

// truncateEvents empties the tables a persist writes, so a database reused across runs
// starts from the same state every time.
func truncateEvents(tb testing.TB, mgr *rdb.RdbManager) {
	tb.Helper()
	sys := mgr.DB(core.WithSystemContext(context.Background()))
	for _, table := range []string{
		"events", "location_events", "measurement_events",
		"alert_events", "event_anchors", "state_change_events",
	} {
		if err := sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "event-management".%q`, table)).Error; err != nil {
			tb.Fatalf("truncate %s: %v", table, err)
		}
	}
}

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

// The fence refuses an event-management write on the real server — a hypertable insert
// behind the real migration chain and callback chain — and nothing lands. Lifting the
// fence is the negative control.
func TestTheFenceRefusesAnEventWriteOnPostgres(t *testing.T) {
	mgr, counter := newFenceBenchManager(t, "emfencetest", true)
	ep := &EventPersistenceWorker{Api: model.NewApi(mgr)}
	// A tenant of its own per run, so a fence a previous run left standing cannot decide
	// this one, and a row count cannot include a previous run's rows.
	tenant := fmt.Sprintf("fr%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	event := fenceCostMeasurement("fr-1", time.Now().UTC().Truncate(time.Hour), 1)
	events := func() int64 {
		var n int64
		if err := mgr.DB(ctx).Model(&model.Event{}).Count(&n).Error; err != nil {
			t.Fatalf("count events: %v", err)
		}
		return n
	}

	plantOn(t, mgr, tenant)
	counter.Reset()
	counter.Arm(true)
	_, err := ep.PersistEvent(ctx, event)
	counter.Arm(false)
	if !errors.Is(err, rdb.ErrTenantPurged) {
		t.Fatalf("persist for a fenced tenant returned %v; want ErrTenantPurged", err)
	}
	if all, fence := counter.Counts(); all != 2 || fence != 1 {
		t.Errorf("a refused persist made %d statements with %d fence reads; want 2 with 1", all, fence)
	}
	if n := events(); n != 0 {
		t.Errorf("a refused persist left %d event row(s)", n)
	}

	liftOn(t, mgr, tenant)
	if _, err := ep.PersistEvent(ctx, event); err != nil {
		t.Fatalf("persist with the fence lifted: %v", err)
	}
	if n := events(); n != 1 {
		t.Errorf("with the fence lifted the persist left %d event row(s), want 1", n)
	}
}

// BenchmarkFenceCostPersistMeasurementMessage times PersistEvent on a 3-metric
// measurement with an altId, with and without one anchor, fence on and off.
//
// It reports only what it measured: ns/op, statements and fence reads per op (counted on
// this server during calibration), and the median loopback round trip. Round trips per
// op add two to the statements (gorm does not trace BEGIN/COMMIT; a persist is one
// transaction).
func BenchmarkFenceCostPersistMeasurementMessage(b *testing.B) {
	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	// Opened in the parent, not per sub-benchmark: the runner calls each sub-benchmark body
	// at least twice (N=1, then the target N, and again for every -count). -count repeats
	// each sub-benchmark back to back rather than interleaving the legs, so host drift over
	// the run is not cancelled between on and off.
	workers := map[string]*EventPersistenceWorker{}
	counters := map[string]*rdbtest.StatementCounter{}
	mgrs := map[string]*rdb.RdbManager{}
	for _, leg := range []struct {
		name   string
		db     string
		fenced bool
	}{{"on", "emfenceon", true}, {"off", "emfenceoff", false}} {
		mgr, counter := newFenceBenchManager(b, leg.db, leg.fenced)
		truncateEvents(b, mgr)
		workers[leg.name], counters[leg.name], mgrs[leg.name] = &EventPersistenceWorker{Api: model.NewApi(mgr)}, counter, mgr
	}
	// Every insert falls into ONE existing hypertable chunk: the base is truncated to the
	// hour and ops are a millisecond apart, and a warm-up op below creates the chunk (and
	// primes the driver's statement cache) before the timer starts.
	base := time.Now().UTC().Truncate(time.Hour)
	var invocation int

	for _, c := range []struct {
		anchors   int
		wantFence int64 // fence reads per op with the fence on
	}{{1, 3}, {0, 2}} {
		for _, fence := range []string{"on", "off"} {
			b.Run(fmt.Sprintf("anchors=%d/fence=%s", c.anchors, fence), func(b *testing.B) {
				ep, counter, mgr := workers[fence], counters[fence], mgrs[fence]
				invocation++
				prefix := fmt.Sprintf("fb-%d-%d", invocation, time.Now().UnixNano())
				persist := func(ev dmodel.ResolvedEvent) error {
					res, err := ep.PersistEvent(ctx, ev)
					if err != nil {
						return err
					}
					if len(res.Events) == 0 {
						return errors.New("the persist wrote nothing (deduplicated?)")
					}
					return nil
				}
				op := 0
				next := func() dmodel.ResolvedEvent {
					op++
					return fenceCostMeasurement(fmt.Sprintf("%s-%d", prefix, op),
						base.Add(time.Duration(op)*time.Millisecond), c.anchors)
				}
				if err := persist(next()); err != nil {
					b.Fatalf("warm-up: %v", err)
				}

				want := c.wantFence
				if fence == "off" {
					want = 0
				}
				stmts, err := counter.Calibrate(3, func(int) error { return persist(next()) }, want)
				if err != nil {
					b.Fatalf("fence=%s calibration: %v", fence, err)
				}

				// Refusal probe: must refuse on, must succeed off.
				probe := fmt.Sprintf("fencedprobe%d", time.Now().UnixNano())
				plantOn(b, mgr, probe)
				_, perr := ep.PersistEvent(core.WithTenant(context.Background(), probe), next())
				if refused := errors.Is(perr, rdb.ErrTenantPurged); refused != (fence == "on") {
					b.Fatalf("fence=%s: refusal probe returned %v", fence, perr)
				}
				if fence == "off" && perr != nil {
					b.Fatalf("fence=off: refusal probe failed for another reason: %v", perr)
				}
				liftOn(b, mgr, probe)

				rtt := medianRTT(b, mgr)
				events := make([]dmodel.ResolvedEvent, b.N)
				for i := range events {
					events[i] = next()
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					// A failed or deduplicated persist would otherwise time as a fast one.
					if res, err := ep.PersistEvent(ctx, events[i]); err != nil || len(res.Events) == 0 {
						b.Fatalf("persist %d wrote nothing: %v", i, err)
					}
				}
				b.StopTimer()
				b.ReportMetric(stmts, "stmts/op")
				b.ReportMetric(float64(want), "fence/op")
				b.ReportMetric(rtt, "rtt-ns")
			})
		}
	}
}
