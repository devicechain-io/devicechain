// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// RecordFireBatch on a real PostgreSQL.
//
// sqlite cannot stand in here, and the reasons are specific: it resolves an unqualified column
// inside DO UPDATE without complaint (PostgreSQL rejects it as ambiguous), it allows one INSERT
// to update a row twice (PostgreSQL refuses), it has a different bind-parameter ceiling, and it
// has no row locks for two batches to deadlock on. So those are tested here, on the REAL
// event-processing migrations run through the REAL core/rdb path (production callback chain).
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run Postgres -v
package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newPostgresRuleStatStore runs the real migrations on the server and returns a rule-stat store
// with the rule-stat and fence tables emptied, plus its raw handle.
func newPostgresRuleStatStore(tb testing.TB) (*RuleStatStore, *gorm.DB) {
	tb.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		tb.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"), itEnv("DC_IT_PGPASSWORD", "postgres")
	const instance = "dcrulestat"
	if err := rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""); err != nil {
		tb.Fatalf("creating the instance database: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type:          "postgres95",
			Configuration: map[string]interface{}{"hostname": host, "port": port, "username": user, "password": pass},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		tb.Fatalf("run the event-processing migrations on the real server: %v", err)
	}
	mgr.Database.Config.Logger = logger.Discard
	tb.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	for _, table := range []string{"rule_stats", rdb.FenceTable} {
		if err := mgr.Database.Exec(`TRUNCATE TABLE "event-processing".` + table).Error; err != nil {
			tb.Fatalf("truncate %s: %v", table, err)
		}
	}
	return NewRuleStatStore(mgr), mgr.Database
}

func sysDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
}

func pgLoad(t *testing.T, s *RuleStatStore, tenant, id string) (RuleStat, bool) {
	t.Helper()
	m, err := s.LoadByIDs(context.Background(), tenant, []string{id})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st, ok := m[id]
	return st, ok
}

// Additive, monotonic, later-wins-a-tie, and duplicates consolidated — on the server whose
// rules (qualified columns, one update per row per statement) sqlite does not enforce.
func TestRecordFireBatchOnPostgres(t *testing.T) {
	s, _ := newPostgresRuleStatStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	a, b := "acme/p@1/a", "acme/p@1/b"
	if err := s.RecordFire(ctx, a, "acme", t0, "raised"); err != nil {
		t.Fatalf("single fire: %v", err)
	}
	n, err := s.RecordFireBatch(ctx, "acme", []RuleFireDelta{
		{RuleID: b, Count: 2, LastFiredAt: t0, LastEdge: "raised"},
		{RuleID: a, Count: 3, LastFiredAt: t0.Add(time.Second), LastEdge: "resolved"},
		{RuleID: b, Count: 4, LastFiredAt: t0, LastEdge: "resolved"}, // tie, later: wins
		{RuleID: a, Count: 1, LastFiredAt: t0, LastEdge: "raised"},   // older: counted only
	})
	if err != nil || n != 2 {
		t.Fatalf("RecordFireBatch = %d, %v; want 2 rows", n, err)
	}
	if st, _ := pgLoad(t, s, "acme", a); st.FireCount != 5 || st.LastEdge != "resolved" || !st.LastFiredAt.Equal(t0.Add(time.Second)) {
		t.Errorf("rule a: %+v; want 5 fires, last resolved at t0+1s", st)
	}
	if st, _ := pgLoad(t, s, "acme", b); st.FireCount != 6 || st.LastEdge != "resolved" {
		t.Errorf("rule b: %+v; want 6 fires, last resolved", st)
	}
	// An equal-time batch against the STORED row: the incoming edge wins (>=).
	if _, err := s.RecordFireBatch(ctx, "acme", []RuleFireDelta{{RuleID: a, Count: 1, LastFiredAt: t0.Add(time.Second), LastEdge: "raised"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := pgLoad(t, s, "acme", a); st.FireCount != 6 || st.LastEdge != "raised" {
		t.Errorf("rule a after an equal-time batch: %+v; want 6 fires, last raised", st)
	}
	// An OLDER batch adds its count and does not rewind.
	if _, err := s.RecordFireBatch(ctx, "acme", []RuleFireDelta{{RuleID: a, Count: 10, LastFiredAt: t0.Add(-time.Hour), LastEdge: "resolved"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := pgLoad(t, s, "acme", a); st.FireCount != 16 || st.LastEdge != "raised" || !st.LastFiredAt.Equal(t0.Add(time.Second)) {
		t.Errorf("rule a after an older batch: %+v", st)
	}
}

// 🔴 WHY THE CONSOLIDATION EXISTS: PostgreSQL refuses one INSERT ... ON CONFLICT that reaches a
// row twice. The same two rows through RecordFireBatch are merged first and land.
func TestPostgresRefusesAnUnconsolidatedDuplicate(t *testing.T) {
	s, db := newPostgresRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	rows := []RuleStat{
		{RuleId: "acme/p@1/a", Tenant: "acme", FireCount: 1, LastFiredAt: t0, LastEdge: "raised"},
		{RuleId: "acme/p@1/a", Tenant: "acme", FireCount: 1, LastFiredAt: t0, LastEdge: "resolved"},
	}
	raw := rdb.AllowTenantlessUpsert(db.WithContext(core.WithTenant(context.Background(), "acme")).Clauses(ruleStatBatchUpsert()), "test: negative control")
	err := raw.Create(&rows).Error
	if err == nil || !strings.Contains(err.Error(), "second time") {
		t.Fatalf("an unconsolidated duplicate returned %v; want PostgreSQL's refusal to update a row twice", err)
	}
	if _, err := s.RecordFireBatch(context.Background(), "acme", []RuleFireDelta{
		{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"},
		{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "resolved"},
	}); err != nil {
		t.Fatalf("the same rows through RecordFireBatch: %v", err)
	}
	if st, _ := pgLoad(t, s, "acme", "acme/p@1/a"); st.FireCount != 2 || st.LastEdge != "resolved" {
		t.Fatalf("%+v; want 2 fires, last resolved", st)
	}
}

// statementCounter counts INSERTs into rule_stats.
type statementCounter struct {
	logger.Interface
	mu      sync.Mutex
	inserts int
}

func (c *statementCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if sql, _ := fc(); strings.HasPrefix(sql, "INSERT INTO") && strings.Contains(sql, "rule_stats") {
		c.mu.Lock()
		c.inserts++
		c.mu.Unlock()
	}
}

// A batch larger than one statement can bind is split by rdb.CreateChunked inside the same
// transaction, and every row lands.
func TestRecordFireBatchChunksPastTheParameterLimit(t *testing.T) {
	s, db := newPostgresRuleStatStore(t)
	counter := &statementCounter{Interface: logger.Discard}
	db.Config.Logger = counter
	const n = 20_000 // 5 parameters a row: 100,000 > 65,535
	deltas := make([]RuleFireDelta, n)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for i := range deltas {
		deltas[i] = RuleFireDelta{RuleID: fmt.Sprintf("acme/p@1/r%05d", i), Count: 2, LastFiredAt: t0, LastEdge: "raised"}
	}
	written, err := s.RecordFireBatch(context.Background(), "acme", deltas)
	if err != nil || written != n {
		t.Fatalf("RecordFireBatch = %d, %v; want %d", written, err, n)
	}
	if counter.inserts < 2 {
		t.Fatalf("%d INSERT statements for %d rows; the batch was not chunked", counter.inserts, n)
	}
	var total int64
	if err := sysDB(db).Model(&RuleStat{}).Select("COALESCE(SUM(fire_count),0)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 2*n {
		t.Fatalf("stored %d fires, want %d", total, 2*n)
	}
}

// Two tenants' batches stay in their own rows, and a batch carrying another tenant's rule is
// refused before anything is sent.
func TestRecordFireBatchTenantIsolationOnPostgres(t *testing.T) {
	s, db := newPostgresRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for _, tenant := range []string{"acme", "beta"} {
		if _, err := s.RecordFireBatch(context.Background(), tenant, []RuleFireDelta{
			{RuleID: tenant + "/p@1/hot", Count: 3, LastFiredAt: t0, LastEdge: "raised"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordFireBatch(context.Background(), "acme", []RuleFireDelta{
		{RuleID: "beta/p@1/hot", Count: 100, LastFiredAt: t0, LastEdge: "resolved"}}); !errors.Is(err, ErrInvalidFireBatch) {
		t.Fatalf("a cross-tenant batch returned %v; want ErrInvalidFireBatch", err)
	}
	if st, _ := pgLoad(t, s, "beta", "beta/p@1/hot"); st.FireCount != 3 || st.Tenant != "beta" || st.LastEdge != "raised" {
		t.Fatalf("beta's row was touched: %+v", st)
	}
	if _, ok := pgLoad(t, s, "acme", "beta/p@1/hot"); ok {
		t.Fatal("acme read beta's row")
	}
	var n int64
	if err := sysDB(db).Model(&RuleStat{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d rows, want 2", n)
	}
}

// Overlapping batches from concurrent writers take their row locks in one order (rows are
// sorted), so they serialize instead of deadlocking, and no count is lost.
func TestConcurrentOverlappingBatchesDoNotDeadlock(t *testing.T) {
	s, db := newPostgresRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 64; i++ {
		ids = append(ids, fmt.Sprintf("acme/p@1/r%02d", i))
	}
	const writers, rounds = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				deltas := make([]RuleFireDelta, len(ids))
				for i := range ids {
					id := ids[i]
					if w%2 == 1 { // half the writers hand their rows over in reverse
						id = ids[len(ids)-1-i]
					}
					deltas[i] = RuleFireDelta{RuleID: id, Count: 1, LastFiredAt: t0, LastEdge: "raised"}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := s.RecordFireBatch(ctx, "acme", deltas)
				cancel()
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent batch failed (a deadlock would be SQLSTATE 40P01): %v", err)
	}
	var total int64
	if err := sysDB(db).Model(&RuleStat{}).Select("COALESCE(SUM(fire_count),0)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if want := int64(writers * rounds * len(ids)); total != want {
		t.Fatalf("stored %d fires, want %d", total, want)
	}
}

// waitForLockWait polls until a backend in this database is waiting on a lock.
func waitForLockWait(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := sysDB(db).Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n).Error; err != nil {
			t.Fatalf("probe pg_stat_activity: %v", err)
		}
		if n >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the batch never blocked on the held row lock")
}

// The fence on the real server, and the one window it does NOT close by itself.
//
//   - A batch that STARTS after the fence is planted is refused (ErrTenantPurged) and writes
//     nothing: the store's fence is what stops a purged tenant coming back, whatever is still
//     queued in memory.
//   - A batch already past its fence read when the fence is planted — here held on a row lock —
//     COMMITS when it is let go, fence or not. That is READ COMMITTED, and core/rdb documents it.
//     It is why the processor cancels and WAITS FOR a purged tenant's in-flight write before it
//     answers the eviction, and counts that write as evicted so the coordinator sweeps again.
//     The cancelled case is shown beside it: the cancelled batch rolls back and the row is not
//     resurrected after the sweep.
func TestTheFenceAndAnInFlightBatchOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"cancelled before the sweep", true}, {"let go (negative control)", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newPostgresRuleStatStore(t)
			ctx := context.Background()
			t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
			const id = "acme/p@1/hot"
			if _, err := s.RecordFireBatch(ctx, "acme", []RuleFireDelta{{RuleID: id, Count: 1, LastFiredAt: t0, LastEdge: "raised"}}); err != nil {
				t.Fatal(err)
			}
			// Another session holds the row, so the batch reads the fence (clear) and then waits.
			holder := sysDB(db).Begin()
			if err := holder.Exec(`SELECT 1 FROM "event-processing".rule_stats WHERE rule_id = ? FOR UPDATE`, id).Error; err != nil {
				t.Fatal(err)
			}
			bctx, bcancel := context.WithCancel(ctx)
			defer bcancel()
			done := make(chan error, 1)
			go func() {
				_, err := s.RecordFireBatch(bctx, "acme", []RuleFireDelta{{RuleID: id, Count: 50, LastFiredAt: t0.Add(time.Minute), LastEdge: "resolved"}})
				done <- err
			}()
			waitForLockWait(t, db)
			now := time.Now().UTC().Truncate(time.Microsecond)
			if err := sysDB(db).Create(&rdb.PurgedTenant{Token: "acme", Epoch: now, PlantedAt: now}).Error; err != nil {
				t.Fatalf("plant the fence: %v", err)
			}
			if tc.cancel {
				bcancel()
				if err := <-done; err == nil {
					t.Fatal("the cancelled batch reported success")
				}
			}
			if err := holder.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if !tc.cancel {
				if err := <-done; err != nil {
					t.Fatalf("the in-flight batch: %v", err)
				}
				st, ok := pgLoad(t, s, "acme", id)
				if !ok || st.FireCount != 51 {
					t.Fatalf("%+v; the negative control expects the in-flight batch to land past the fence", st)
				}
			}
			// The purge's sweep.
			if err := sysDB(db).Where("tenant = ?", "acme").Delete(&RuleStat{}).Error; err != nil {
				t.Fatal(err)
			}
			// Anything that starts now is refused by the fence.
			_, err := s.RecordFireBatch(ctx, "acme", []RuleFireDelta{{RuleID: id, Count: 7, LastFiredAt: t0, LastEdge: "raised"}})
			if !errors.Is(err, rdb.ErrTenantPurged) {
				t.Fatalf("a batch after the fence returned %v; want ErrTenantPurged", err)
			}
			if st, ok := pgLoad(t, s, "acme", id); ok {
				t.Fatalf("the purged tenant's row came back: %+v", st)
			}
		})
	}
}

// A stored count at the int64 ceiling is not wrapped by an additive batch: PostgreSQL's bigint
// addition fails the statement, the transaction rolls back, and the row keeps its value.
func TestAnAdditiveBatchCannotWrapTheStoredCount(t *testing.T) {
	s, db := newPostgresRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	const id = "acme/p@1/hot"
	if _, err := s.RecordFireBatch(context.Background(), "acme", []RuleFireDelta{{RuleID: id, Count: 1, LastFiredAt: t0, LastEdge: "raised"}}); err != nil {
		t.Fatal(err)
	}
	if err := sysDB(db).Exec(`UPDATE "event-processing".rule_stats SET fire_count = 9223372036854775807 WHERE rule_id = ?`, id).Error; err != nil {
		t.Fatal(err)
	}
	_, err := s.RecordFireBatch(context.Background(), "acme", []RuleFireDelta{{RuleID: id, Count: 1, LastFiredAt: t0.Add(time.Second), LastEdge: "resolved"}})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("an overflowing batch returned %v; want bigint out of range", err)
	}
	if st, _ := pgLoad(t, s, "acme", id); st.FireCount != 9223372036854775807 || st.LastEdge != "raised" {
		t.Fatalf("the row moved: %+v", st)
	}
}
