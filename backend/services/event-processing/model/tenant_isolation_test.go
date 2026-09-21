// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// isolationDB builds one in-memory database carrying every projection in this area, with
// the tenant-scope callback registered exactly as the service registers it.
func isolationDB(t *testing.T) *rdb.RdbManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DetectRule{}, &DeviceRoster{}, &ProfileActive{},
		&DeviceAttribute{}, &DeviceAttributeDeletion{}, &RuleStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &rdb.RdbManager{Database: db}
}

// 🔴 THE GUARANTEE THIS AREA DID NOT HAVE. Its six projections spell the tenant `tenant`
// rather than `tenant_id`, and the fail-closed scope callback recognised only the second
// spelling — so these tables were classified like migration bookkeeping and got no
// predicate at all. Isolation rested entirely on a hand-written `WHERE tenant = ?` in
// each read, and nothing checked that the clause was there. Delete one and every test in
// the repository still passed.
//
// The clauses are gone now: each store puts its tenant in the context and the callback
// injects the predicate, which is what every other area has always had. This is the test
// that says so — and, more to the point, the test that FAILS if the callback ever stops
// recognising this spelling again. Without it the change would be a refactor with no
// guarantee behind it, which is the shape of gate this repository keeps catching itself
// shipping.
//
// Every case seeds two tenants and reads as one. A case that returned the reader's own
// row but not the other tenant's is the only passing answer: asserting merely that the
// intruder's row is absent would also pass against a read that returns nothing at all.
//
// 🔴 THE CONTEXT DELIBERATELY DISAGREES WITH THE ARGUMENT wherever the store takes a
// tenant parameter, and that is what makes these cases mean anything. Each such store
// derives the scoping context from its OWN argument, so handing it a context that
// already holds the right tenant tests nothing — the read would come out the same if the
// store dropped the derivation entirely. Mutation caught exactly that: removing the
// WithTenant from LoadDevice left this test green until the contexts were made to
// disagree. LoadByID is the exception and takes `mine`, because there the context IS the
// only tenant the read has.
func TestOneTenantCannotReadAnothersProjections(t *testing.T) {
	mgr := isolationDB(t)
	mine := dccore.WithTenant(context.Background(), "mine")
	theirs := dccore.WithTenant(context.Background(), "theirs")
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	rules := NewDetectRuleStore(mgr)
	rosters := NewDeviceRosterStore(mgr)
	profiles := NewProfileActiveStore(mgr)
	attrs := NewDeviceAttributeStore(mgr)
	stats := NewRuleStatStore(mgr)

	for _, tenant := range []string{"mine", "theirs"} {
		if err := rules.Upsert(context.Background(), []DetectRule{{
			RuleId: tenant + "/p@1/hot", Tenant: tenant, ProfileVersionToken: "p@1",
			RuleToken: "hot", Definition: `{"actions":[]}`,
		}}); err != nil {
			t.Fatalf("seed rule for %s: %v", tenant, err)
		}
		if err := rosters.Upsert(context.Background(), &DeviceRoster{
			Tenant: tenant, DeviceToken: "shared-token", ProfileToken: "p", ExpectedSince: at,
		}); err != nil {
			t.Fatalf("seed roster for %s: %v", tenant, err)
		}
		if err := profiles.Upsert(context.Background(), &ProfileActive{
			Tenant: tenant, ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: at,
		}); err != nil {
			t.Fatalf("seed profile for %s: %v", tenant, err)
		}
		if err := attrs.Upsert(context.Background(), &DeviceAttribute{
			Tenant: tenant, DeviceToken: "shared-token", Scope: "SHARED", AttrKey: "k",
			Value: 1, LastEventAt: at,
		}); err != nil {
			t.Fatalf("seed attribute for %s: %v", tenant, err)
		}
		if err := stats.RecordFire(context.Background(), tenant+"/p@1/hot", tenant, at, "raised"); err != nil {
			t.Fatalf("seed stat for %s: %v", tenant, err)
		}
	}

	t.Run("DetectRuleStore.LoadByProfileVersion", func(t *testing.T) {
		got, err := rules.LoadByProfileVersion(theirs, "mine", "p@1")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(got) != 1 || got[0].RuleId != "mine/p@1/hot" {
			t.Fatalf("got %d rows %v, want exactly mine/p@1/hot", len(got), got)
		}
	})

	t.Run("DetectRuleStore.LoadByID", func(t *testing.T) {
		if _, found, err := rules.LoadByID(mine, "theirs/p@1/hot"); err != nil || found {
			t.Fatalf("another tenant's rule resolved by id: found=%v err=%v", found, err)
		}
		if _, found, err := rules.LoadByID(mine, "mine/p@1/hot"); err != nil || !found {
			t.Fatalf("own rule did not resolve: found=%v err=%v", found, err)
		}
	})

	t.Run("DeviceRosterStore.Load", func(t *testing.T) {
		row, live, err := rosters.Load(theirs, "mine", "shared-token")
		if err != nil || !live {
			t.Fatalf("own roster row: live=%v err=%v", live, err)
		}
		if row.Tenant != "mine" {
			t.Fatalf("read another tenant's roster row: %q", row.Tenant)
		}
	})

	t.Run("ProfileActiveStore.Load", func(t *testing.T) {
		row, found, err := profiles.Load(theirs, "mine", "p")
		if err != nil || !found {
			t.Fatalf("own profile row: found=%v err=%v", found, err)
		}
		if row.Tenant != "mine" {
			t.Fatalf("read another tenant's profile row: %q", row.Tenant)
		}
	})

	t.Run("DeviceAttributeStore.LoadDevice", func(t *testing.T) {
		got, err := attrs.LoadDevice(theirs, "mine", "shared-token")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(got) != 1 || got[0].Tenant != "mine" {
			t.Fatalf("got %d rows %v, want exactly mine's", len(got), got)
		}
	})

	t.Run("RuleStatStore.LoadByIDs", func(t *testing.T) {
		got, err := stats.LoadByIDs(theirs, "mine", []string{"mine/p@1/hot", "theirs/p@1/hot"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if _, leaked := got["theirs/p@1/hot"]; leaked {
			t.Fatal("another tenant's rule stat was returned for an id the caller asked about")
		}
		if _, ok := got["mine/p@1/hot"]; !ok {
			t.Fatal("own rule stat was not returned")
		}
	})
}

// A write carrying one tenant cannot land under another's key. The scope callback stamps
// the context's tenant onto a create, and for these projections the tenant is part of the
// PRIMARY KEY — so a silent rewrite would not merely relabel the row, it would move it to
// a key the caller's ON CONFLICT target does not name.
func TestAProjectionWriteCannotLandUnderAnotherTenant(t *testing.T) {
	mgr := isolationDB(t)
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	rosters := NewDeviceRosterStore(mgr)

	err := mgr.DB(dccore.WithTenant(context.Background(), "mine")).Create(&DeviceRoster{
		Tenant: "theirs", DeviceToken: "d", ProfileToken: "p", ExpectedSince: at, LastEventAt: at,
	}).Error
	if !errors.Is(err, rdb.ErrTenantMismatch) {
		t.Fatalf("a row naming another tenant: got %v, want rdb.ErrTenantMismatch", err)
	}

	// The counterweight: the same write under its own tenant is accepted, so the refusal
	// is about the disagreement and not about writing this table at all.
	if err := rosters.Upsert(context.Background(), &DeviceRoster{
		Tenant: "theirs", DeviceToken: "d", ProfileToken: "p", ExpectedSince: at,
	}); err != nil {
		t.Fatalf("the same row under its own tenant was refused: %v", err)
	}
}

// A scoped read with no tenant anywhere is refused rather than answered. This is the
// property the hand-written WHERE clauses could never have: a forgotten clause returned
// every tenant's rows and looked like a working read, where a forgotten context now stops
// the statement.
func TestAProjectionReadWithNoTenantIsRefused(t *testing.T) {
	mgr := isolationDB(t)
	var rules []DetectRule
	err := mgr.DB(context.Background()).Where("profile_version_token = ?", "p@1").Find(&rules).Error
	if !errors.Is(err, dccore.ErrNoTenant) {
		t.Fatalf("a scoped read with no tenant in context: got %v, want core.ErrNoTenant", err)
	}
}

// 🔴 A SYSTEM CONTEXT BEATS A TENANT ONE, and the order is worth pinning because nothing
// about core.WithTenant says so: it adds a value, it does not clear the system marker, so
// wrapping a system context in WithTenant yields a context that is BOTH and the callback
// takes the bypass. That is the documented contract — scopedTenant tests the marker before
// it classifies anything — but it means a store handed a system context would derive its
// tenant, inject no predicate, and read every tenant's rows.
//
// No caller does that today: each of the four system contexts in this area is created
// inside the LoadAll that uses it and never escapes. This test exists so that if one ever
// does escape, the behaviour it runs into is one somebody wrote down rather than one
// nobody knew about.
func TestASystemContextBeatsATenantWrappedAroundIt(t *testing.T) {
	mgr := isolationDB(t)
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	rosters := NewDeviceRosterStore(mgr)

	for _, tenant := range []string{"mine", "theirs"} {
		if err := rosters.Upsert(context.Background(), &DeviceRoster{
			Tenant: tenant, DeviceToken: "shared-token", ProfileToken: "p", ExpectedSince: at,
		}); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}

	both := dccore.WithTenant(dccore.WithSystemContext(context.Background()), "mine")
	var rows []DeviceRoster
	if err := mgr.DB(both).Find(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: the system marker must win over a tenant wrapped "+
			"around it, so no predicate is injected", len(rows))
	}

	// The counterweight: the same read with only the tenant is scoped, so the result above
	// is the system marker's doing and not a broken predicate.
	rows = nil
	if err := mgr.DB(dccore.WithTenant(context.Background(), "mine")).Find(&rows).Error; err != nil {
		t.Fatalf("scoped read: %v", err)
	}
	if len(rows) != 1 || rows[0].Tenant != "mine" {
		t.Fatalf("got %d rows %v, want exactly mine's", len(rows), rows)
	}
}
