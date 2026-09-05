// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The per-fence position ceiling's grandfathering baseline is re-read INSIDE the update's
// transaction under a row lock, and until this file existed nothing tested that.
//
// 🔴 WHY IT CANNOT LIVE WITH THE OTHER GEOFENCE TESTS. They run on in-memory sqlite, which has
// neither the row-level locking the fix uses nor the concurrency it defends against. In a
// SEQUENTIAL test the pre-transaction read and the locked re-read return the same bytes, so the
// fix has no observable behaviour at all and a green suite is evidence about nothing. The code
// says so at the fix; this is the coverage that comment asks for.
//
// Tagged `integration` so it stays out of the default `go test ./...`, which must run with no
// server. Run it against a throwaway server:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run TOCTOU -v
package model

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newPostgresGeoFenceApi runs the REAL migration chain through the REAL core/rdb path against a
// live PostgreSQL and returns an Api on it, so the schema under test is the deployed one.
//
// 🔴 THE PASSWORD DEFAULT IS "postgres" AND THAT IS NOT ARBITRARY. hack/migration-diff.sh starts
// its throwaway server with it, and core/rdb's and migrationdiff's integration tests hardcode it.
// event-management's suite alone defaults to "devicechain", which is why running the whole
// integration tag against one container used to fail three suites out of four for a reason that
// looked like a code defect and was a harness defect.
func newPostgresGeoFenceApi(t *testing.T) *Api {
	t.Helper()
	mgr := newPostgresRdbManager(t)

	// Start from empty every run. The instance id is fixed, so a server that has run this
	// before still holds its rows, and a fence token collides on the second run — which would
	// fail as though the code had regressed.
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"geo_fence_set_versions", "geo_fence_geometry_blobs", "geo_fences"} {
		if err := sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-management".%q CASCADE`, table)).Error; err != nil {
			t.Fatalf("truncate %s before the run: %v", table, err)
		}
	}
	return NewApi(mgr)
}

// newPostgresRdbManager runs the migration chain and hands back the manager, TRUNCATING
// NOTHING. Split out of newPostgresGeoFenceApi for the backfill tests, which have to seed rows
// and then run the chain OVER them — the one thing a helper that empties the tables first
// cannot be used for.
func newPostgresRdbManager(t *testing.T) *rdb.RdbManager {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	if err != nil {
		t.Fatalf("DC_IT_PGPORT must be numeric: %v", err)
	}
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{
			InstanceId:     "dctoctou",
			FunctionalArea: "device-management",
		},
		Migrations: schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres95",
			Configuration: map[string]interface{}{
				"hostname": itEnv("DC_IT_PGHOST", "localhost"),
				"port":     port,
				"username": itEnv("DC_IT_PGUSER", "postgres"),
				"password": itEnv("DC_IT_PGPASSWORD", "postgres"),
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	if err := mgr.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("run the device-management migrations on the real server: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return mgr
}

// waitForBlockedBackend polls until at least one backend in this database is waiting on a lock.
//
// 🔴 IT IS THE SYNCHRONISATION, NOT A CONVENIENCE. Sleeping a fixed interval and hoping the
// goroutine has reached the row lock is how this test would pass while proving nothing: if the
// update had not yet entered its transaction, the row would change BEFORE its outer read and the
// scenario under test would never be built. The poll is what makes "it is blocked on the lock" a
// fact rather than an assumption.
func waitForBlockedBackend(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n).Error; err != nil {
			t.Fatalf("probe pg_stat_activity: %v", err)
		}
		if n >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no backend ever blocked on a lock; the update never reached its transaction, so the " +
		"interleaving this test exists to build was never built")
}

// Two writers, one fence: the ceiling must be applied to what the update COMMITS OVER, not to a
// copy read before its transaction opened.
//
// The scenario is the one the fix's comment describes. A fence is grandfathered at 1024 positions
// against a ceiling since lowered to 512. One writer replaces it with a DIFFERENT 1024-position
// geometry — not growth against the value it read outside the transaction, so on the old code it
// skipped the ceiling check entirely. Another writer shrinks the fence to 8 first. The replacement
// then lands on top of the shrink and takes the fence from 8 to 1024 with no ceiling check at all,
// an outcome neither serial order allows.
//
// 🔴 THE INTERLEAVING IS FORCED, NOT RACED. An earlier design started both writers and relied on
// PostgreSQL granting the row lock in wait order; that makes the test depend on lock-queue
// fairness, and its failure mode is a flake indistinguishable from the defect. Here a single
// external transaction holds the row lock, the update blocks on it, and the SHRINK IS APPLIED
// INSIDE THAT SAME TRANSACTION before it commits. The update therefore provably re-reads a row
// that changed under it, with no second writer to schedule.
func TestTOCTOUAnUpdateIsMeteredAgainstWhatItCommitsOver(t *testing.T) {
	api := newPostgresGeoFenceApi(t)
	caps := generousCaps()
	resolver := &stubCapsResolver{caps: caps}
	refusals := newCountingRefusals()
	api.GeoFenceCapsResolver = resolver
	api.GeoFenceCapRefusals = refusals
	ctx := core.WithTenant(context.Background(), "acme")

	// 🔴 THE FIXTURES ARE SMALL ON PURPOSE, AND THE FIRST VERSION OF THIS TEST WAS NOT.
	// It used 1024-position rings — the platform maximum — to make the scenario look like the
	// real one, and was refused before it ever reached the race: at full float64 precision a
	// 1024-position ring canonicalises to 42,002 bytes, over the 32 KiB MaxGeoFenceGeometryBytes
	// that was sized when the ceiling was 512. The race is scale-INDEPENDENT — all it needs is a
	// replacement whose count equals the stored count and exceeds the tenant's ceiling — so
	// borrowing the maximum bought nothing and coupled this test to an unrelated bound.
	const (
		token          = "racer"
		fencePositions = 64
		loweredCeiling = 16
		shrunkTo       = 8
	)
	grandfathered := ringOf(fencePositions, 0.01)
	replacement := ringOf(fencePositions, 0.02) // same count, different shape, so a different hash
	shrunk := ringOf(shrunkTo, 0.005)
	if positionsOf(t, grandfathered) != fencePositions || positionsOf(t, replacement) != fencePositions {
		t.Fatalf("the fixtures are not both at %d positions (%d and %d); the whole scenario "+
			"depends on the replacement not being growth against the stored value", fencePositions,
			positionsOf(t, grandfathered), positionsOf(t, replacement))
	}
	if grandfathered == replacement {
		t.Fatal("the two fixtures are byte-identical, so the replacement would be a no-op edit " +
			"rather than a same-size replacement")
	}

	if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
		Token: token, Geometry: grandfathered,
	}); err != nil {
		t.Fatalf("seeding the grandfathered fence at the platform maximum: %v", err)
	}

	// The tier is lowered AFTER the fence exists. That is the grandfathering case: the fence is
	// legal history, and what the ceiling refuses from here is growth.
	caps.PositionCeiling = loweredCeiling
	resolver.caps = caps

	// THE COUNTERWEIGHT, FIRST. With nothing racing it, the same replacement SUCCEEDS — it is not
	// growth against the stored 1024, and a grandfathered fence must stay editable. Without this
	// the refusal below would be satisfied by an update that simply always fails.
	if _, err := api.UpdateGeoFence(ctx, token, geoFenceEdit(replacement)); err != nil {
		t.Fatalf("an uncontended replacement of a grandfathered fence, at the same position count, "+
			"was refused: %v. The ceiling refuses GROWTH, not size", err)
	}
	if n := refusals.total(); n != 0 {
		t.Fatalf("the uncontended replacement produced %d cap refusals; it must produce none", n)
	}

	// Now the interleaving. T takes the row lock and holds it.
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	tx := sys.Begin()
	if tx.Error != nil {
		t.Fatalf("open the holding transaction: %v", tx.Error)
	}
	var lockedIds []int64
	if err := tx.Raw(`SELECT id FROM "device-management".geo_fences WHERE token = ? FOR UPDATE`,
		token).Scan(&lockedIds).Error; err != nil {
		tx.Rollback()
		t.Fatalf("take the row lock: %v", err)
	}
	if len(lockedIds) != 1 {
		tx.Rollback()
		t.Fatalf("expected to lock exactly one fence row, locked %d", len(lockedIds))
	}

	// The writer under test. It reads 1024 outside its transaction, then blocks on T's lock.
	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := api.UpdateGeoFence(ctx, token, geoFenceEdit(ringOf(fencePositions, 0.03)))
		done <- outcome{err: err}
	}()

	waitForBlockedBackend(t, sys)

	// The row changes under it, inside the transaction that holds the lock.
	canonicalShrunk, shrunkPositions, err := validateGeoFenceGeometry(shrunk)
	if err != nil {
		tx.Rollback()
		t.Fatalf("canonicalise the shrunk geometry: %v", err)
	}
	if err := tx.Exec(`UPDATE "device-management".geo_fences SET geometry = ?::jsonb WHERE token = ?`,
		canonicalShrunk, token).Error; err != nil {
		tx.Rollback()
		t.Fatalf("apply the shrink inside the holding transaction: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit the shrink: %v", err)
	}

	var got outcome
	select {
	case got = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the blocked update never returned after the lock was released")
	}

	if got.err == nil {
		t.Fatalf("the update committed over a %d-position fence and took it to %d, past the "+
			"tenant's %d ceiling, with no refusal. The baseline was read BEFORE the transaction, "+
			"where %d > %d is false, so the ceiling check was skipped entirely",
			shrunkPositions, fencePositions, loweredCeiling, fencePositions, fencePositions)
	}
	if n := refusals.count(governance.GeoFencePositionCeilingField); n != 1 {
		t.Fatalf("the refusal was counted %d times against %s; a refusal nothing counts is one an "+
			"operator cannot see", n, governance.GeoFencePositionCeilingField)
	}

	// And the fence is left at the shrink, not at the replacement.
	after, err := api.GeoFencesByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("read the fence back: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected one fence after the race, found %d", len(after))
	}
	stored, err := geoFencePositionsIn([]byte(after[0].Geometry))
	if err != nil {
		t.Fatalf("count the stored positions: %v", err)
	}
	if stored != shrunkPositions {
		t.Fatalf("the fence holds %d positions; the refused update was rolled back, so it must "+
			"hold the shrink's %d", stored, shrunkPositions)
	}
	t.Logf("refused with: %v", got.err)
}
