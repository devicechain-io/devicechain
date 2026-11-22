// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// SweepStranded on a real PostgreSQL: the predicate, the bound, the advisory lock and the
// re-check under a concurrent fire are all properties of the server, so none of them is tested
// on sqlite. Run as the other integration tests in this package are (see
// rule_stat_batch_integration_test.go).
package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

var sweepNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// sweepBefore is the cut-off the tests sweep with: rows that last fired before it are quiet.
var sweepBefore = sweepNow.Add(-7 * 24 * time.Hour)

func newSweepFixture(t *testing.T) (*RuleStatStore, *gorm.DB) {
	t.Helper()
	s, db := newPostgresRuleStatStore(t)
	if err := db.Exec(`TRUNCATE TABLE "event-processing".profile_actives`).Error; err != nil {
		t.Fatalf("truncate profile_actives: %v", err)
	}
	return s, db
}

func seedActive(t *testing.T, db *gorm.DB, tenant, profile, active string) {
	t.Helper()
	row := ProfileActive{Tenant: tenant, ProfileToken: profile, ActiveVersionToken: active, PublishedAt: sweepNow.Add(-30 * 24 * time.Hour)}
	if err := db.WithContext(core.WithTenant(context.Background(), tenant)).Create(&row).Error; err != nil {
		t.Fatalf("seed active %s/%s: %v", tenant, active, err)
	}
}

func seedStat(t *testing.T, db *gorm.DB, tenant, id string, at time.Time) {
	t.Helper()
	row := RuleStat{RuleId: id, Tenant: tenant, LastFiredAt: at, FireCount: 3, LastEdge: "raised"}
	if err := db.WithContext(core.WithTenant(context.Background(), tenant)).Create(&row).Error; err != nil {
		t.Fatalf("seed stat %s: %v", id, err)
	}
}

func statIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	if err := sysDB(db).Model(&RuleStat{}).Order("rule_id").Pluck("rule_id", &ids).Error; err != nil {
		t.Fatalf("list stats: %v", err)
	}
	return ids
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	w := append([]string(nil), want...)
	sort.Strings(w)
	for i := range got {
		if got[i] != w[i] {
			return false
		}
	}
	return true
}

// A superseded version's quiet row is deleted; every row that could still be read or still be
// firing is kept, and the sweep touches only the tenant it was asked about.
func TestSweepStrandedDeletesOnlyStrandedRowsOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	old, recent := sweepBefore.Add(-time.Hour), sweepBefore.Add(time.Hour)

	seedActive(t, db, "acme", "p", "p@2")
	seedActive(t, db, "acme", "r", "r@1")
	seedActive(t, db, "beta", "p", "p@2")

	keep := []string{
		"acme/p@2/a", // the active version, quiet: the health view reads it
		"acme/p@1/b", // superseded but fired after the cut-off: still firing
		"acme/r@1/a", // active r@1, quiet
		"acme/z@1/a", // a profile with no active-version row: not guessed about
	}
	gone := []string{
		"acme/p@1/a",  // superseded, quiet
		"acme/r@10/a", // version 10 of r: active "r@1" without its trailing '/' would match it
		"acme/r@1x/a", // the same, with a non-digit
	}
	for _, id := range []string{"acme/p@2/a", "acme/r@1/a", "acme/z@1/a", "acme/p@1/a", "acme/r@10/a", "acme/r@1x/a"} {
		seedStat(t, db, "acme", id, old)
	}
	seedStat(t, db, "acme", "acme/p@1/b", recent)
	seedStat(t, db, "beta", "beta/p@1/a", old) // stranded, but another tenant's

	n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100)
	if err != nil || n != int64(len(gone)) {
		t.Fatalf("SweepStranded(acme) = %d, %v; want %d deleted", n, err, len(gone))
	}
	want := append(append([]string(nil), keep...), "beta/p@1/a")
	if got := statIDs(t, db); !sameIDs(got, want) {
		t.Fatalf("after sweeping acme: %v; want %v", got, want)
	}

	// The other tenant's stranded row goes when that tenant is swept, and nothing else does.
	if n, err := s.SweepStranded(context.Background(), "beta", sweepBefore, 100); err != nil || n != 1 {
		t.Fatalf("SweepStranded(beta) = %d, %v; want 1", n, err)
	}
	if got := statIDs(t, db); !sameIDs(got, keep) {
		t.Fatalf("after sweeping beta: %v; want %v", got, keep)
	}
	// A second pass finds nothing.
	if n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v; want 0", n, err)
	}
}

// An active-version row in one tenant is not protected by another tenant's active versions,
// and the reverse: the profile and the active version are matched within the row's tenant.
func TestSweepStrandedMatchesActiveVersionsWithinTheTenantOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	old := sweepBefore.Add(-time.Hour)
	seedActive(t, db, "acme", "p", "p@2")
	seedActive(t, db, "beta", "p", "p@1") // beta's active p@1 must not keep acme's p@1
	seedStat(t, db, "acme", "acme/p@1/a", old)
	seedStat(t, db, "acme", "acme/q@1/a", old) // acme has no active q; beta's p does not count
	if n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100); err != nil || n != 1 {
		t.Fatalf("SweepStranded = %d, %v; want 1", n, err)
	}
	if got := statIDs(t, db); !sameIDs(got, []string{"acme/q@1/a"}) {
		t.Fatalf("after the sweep: %v; want only acme/q@1/a", got)
	}
}

// One call deletes at most limit rows, the lowest rule ids first, and repeated calls finish.
func TestSweepStrandedIsBoundedOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	seedActive(t, db, "acme", "p", "p@2")
	for i := 0; i < 5; i++ {
		seedStat(t, db, "acme", fmt.Sprintf("acme/p@1/r%d", i), sweepBefore.Add(-time.Hour))
	}
	if n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 2); err != nil || n != 2 {
		t.Fatalf("first batch = %d, %v; want 2", n, err)
	}
	if got := statIDs(t, db); !sameIDs(got, []string{"acme/p@1/r2", "acme/p@1/r3", "acme/p@1/r4"}) {
		t.Fatalf("after one batch of 2: %v; want r2..r4", got)
	}
	for _, want := range []int64{2, 1, 0} {
		if n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 2); err != nil || n != want {
			t.Fatalf("batch = %d, %v; want %d", n, err, want)
		}
	}
}

// While another session holds the sweep lock the sweep deletes nothing and reports the pass
// skipped; once it is released the sweep runs.
func TestSweepStrandedSkipsWhileAnotherReplicaHoldsTheLockOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	seedActive(t, db, "acme", "p", "p@2")
	seedStat(t, db, "acme", "acme/p@1/a", sweepBefore.Add(-time.Hour))

	holder := sysDB(db).Begin()
	if err := holder.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, RuleStatSweepLock).Error; err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100)
	if !errors.Is(err, core.ErrPassSkipped) || n != 0 {
		holder.Rollback()
		t.Fatalf("with the lock held: %d, %v; want 0 and ErrPassSkipped", n, err)
	}
	if got := statIDs(t, db); len(got) != 1 {
		holder.Rollback()
		t.Fatalf("a skipped sweep deleted rows: %v", got)
	}
	holder.Rollback()
	if n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100); err != nil || n != 1 {
		t.Fatalf("after release = %d, %v; want 1", n, err)
	}
}

// 🔴 A fire that lands on a row the sweep has chosen must keep it. The fire's transaction
// advances last_fired_at and holds the row; the sweep's DELETE waits on it, and when the fire
// commits the DELETE re-checks the predicate against the new value and leaves the row.
func TestSweepStrandedKeepsARowAConcurrentFireAdvancedOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	seedActive(t, db, "acme", "p", "p@2")
	seedStat(t, db, "acme", "acme/p@1/a", sweepBefore.Add(-time.Hour))
	seedStat(t, db, "acme", "acme/p@1/b", sweepBefore.Add(-time.Hour))

	fire := sysDB(db).Begin()
	if err := fire.Exec(`UPDATE rule_stats SET last_fired_at = ?, fire_count = fire_count + 1 WHERE rule_id = ?`,
		sweepNow, "acme/p@1/a").Error; err != nil {
		fire.Rollback()
		t.Fatalf("fire: %v", err)
	}
	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 100)
		done <- result{n, err}
	}()
	// Wait until the sweep is blocked on the fire's row lock, so the order is the one under test.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := sysDB(db).Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'DELETE FROM rule_stats%'`).Scan(&waiting).Error; err != nil {
			fire.Rollback()
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			fire.Rollback()
			t.Fatalf("the sweep finished without waiting on the fire's row: %d, %v", r.n, r.err)
		default:
		}
		if time.Now().After(deadline) {
			fire.Rollback()
			t.Fatal("the sweep never waited on the fire's row lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := fire.Commit().Error; err != nil {
		t.Fatalf("commit the fire: %v", err)
	}
	r := <-done
	if r.err != nil || r.n != 1 {
		t.Fatalf("SweepStranded = %d, %v; want 1 (only the row no fire touched)", r.n, r.err)
	}
	if got := statIDs(t, db); !sameIDs(got, []string{"acme/p@1/a"}) {
		t.Fatalf("after the sweep: %v; want the fired row kept", got)
	}
}

// Two replicas sweeping at once: every call either runs or is skipped, nothing errs, every
// stranded row goes exactly once (the deleted counts sum to the stranded total) and no kept
// row goes.
func TestTwoReplicasSweepingConcurrentlyOnPostgres(t *testing.T) {
	s, db := newSweepFixture(t)
	seedActive(t, db, "acme", "p", "p@2")
	const stranded = 600
	rows := make([]RuleStat, 0, stranded)
	for i := 0; i < stranded; i++ {
		rows = append(rows, RuleStat{RuleId: fmt.Sprintf("acme/p@1/r%04d", i), Tenant: "acme",
			LastFiredAt: sweepBefore.Add(-time.Hour), FireCount: 1, LastEdge: "raised"})
	}
	if err := db.WithContext(core.WithTenant(context.Background(), "acme")).CreateInBatches(&rows, 200).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedStat(t, db, "acme", "acme/p@2/live", sweepBefore.Add(-time.Hour))

	var (
		mu               sync.Mutex
		total            int64
		skipped, batches int
		wg               sync.WaitGroup
		errs             []error
	)
	start := make(chan struct{})
	for replica := 0; replica < 2; replica++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for idle := 0; idle < 50; {
				n, err := s.SweepStranded(context.Background(), "acme", sweepBefore, 25)
				mu.Lock()
				switch {
				case errors.Is(err, core.ErrPassSkipped):
					skipped++
				case err != nil:
					errs = append(errs, err)
				default:
					total += n
					batches++
				}
				mu.Unlock()
				if err != nil && !errors.Is(err, core.ErrPassSkipped) {
					return
				}
				if n == 0 {
					idle++
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("a replica's sweep failed: %v", errs)
	}
	if total != stranded {
		t.Fatalf("the replicas deleted %d rows in all; want exactly %d", total, stranded)
	}
	if got := statIDs(t, db); !sameIDs(got, []string{"acme/p@2/live"}) {
		t.Fatalf("after both replicas: %d rows left (%v...); want only the live row", len(got), got[:min(3, len(got))])
	}
	t.Logf("%d batches ran, %d calls skipped on the lock", batches, skipped)
}
