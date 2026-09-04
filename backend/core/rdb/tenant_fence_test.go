// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// projection is event-processing's shape: a plain Tenant column in the primary key, no
// TenantScoped embed, so the scoping callbacks never fire for it and nothing puts a
// tenant in its context. Its writes are the ON CONFLICT upserts ADR-077 lists as
// resurrection vector 4.
type projection struct {
	Tenant      string `gorm:"primaryKey"`
	DeviceToken string `gorm:"primaryKey"`
	Value       string
}

// Every real event-processing model opts out of the audit journal, and this one has to as
// well or the test would be about a shape that does not exist: the journal's own row is
// tenant-scoped, so writing one for a statement that carries no tenant in its context
// fails closed before the fence is ever consulted.
func (projection) AuditExempt() bool { return true }

// newFenceDB opens an in-memory database with BOTH the scoping and the fence callbacks
// registered, in the order production registers them.
func newFenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("registering tenant scoping: %v", err)
	}
	if err := RegisterTenantFence(db); err != nil {
		t.Fatalf("registering the fence: %v", err)
	}
	// 🔴 THE AUDIT JOURNAL IS REGISTERED HERE BECAUSE PRODUCTION REGISTERS IT, and leaving
	// it out made the delete test below pass against a callback set that does not exist.
	// The journal's insert runs After("gorm:create"), which gorm sorts after the commit
	// callback, so it is the one write that reaches the fence with its own mutation
	// already committed.
	if err := RegisterAuditJournal(db); err != nil {
		t.Fatalf("registering the audit journal: %v", err)
	}
	if err := db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}, &gadget{}, &projection{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return db
}

// plant stands a fence for a token, as the purge's first pass does.
func plant(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	row := &PurgedTenant{Token: token, Epoch: time.Now().UTC().Truncate(time.Microsecond),
		PlantedAt: time.Now().UTC()}
	// Under a system context, as the purge coordinator runs: the audit journal's own row
	// is tenant-scoped and would otherwise fail closed with no tenant in context.
	if err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).Create(row).Error; err != nil {
		t.Fatalf("planting a fence for %q: %v", token, err)
	}
}

// lift completes every standing fence for a token, as the pass that releases it does.
func lift(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	now := time.Now().UTC()
	err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).Model(&PurgedTenant{}).
		Where("token = ? AND completed_at IS NULL", token).
		Update("completed_at", now).Error
	if err != nil {
		t.Fatalf("lifting the fence for %q: %v", token, err)
	}
}

func TestAWriteForAFencedTenantIsRefused(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")
	ctx := core.WithTenant(context.Background(), "acme")

	err := db.WithContext(ctx).Create(&widget{Name: "resurrected"}).Error
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("create for a fenced tenant: got %v, want ErrTenantPurged", err)
	}

	// 🔴 COUNT UNDER A SYSTEM CONTEXT. A tenant-scoped Count with no tenant in context
	// fails closed with ErrNoTenant and never reaches the database, so the count stays at
	// its zero value and the assertion holds however many rows were written.
	if n := widgetCount(t, db); n != 0 {
		t.Fatalf("a refused create still wrote %d row(s)", n)
	}
}

// widgetCount counts rows without the tenant predicate, so the count is a fact about the
// table rather than about whether the query was allowed to run.
func widgetCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).
		Model(&widget{}).Count(&n).Error
	if err != nil {
		t.Fatalf("counting widgets: %v", err)
	}
	return n
}

// The negative control for widgetCount: with the fence lifted, the identical create must
// leave a row the identical count can see. Without it, a count that could never see
// anything would satisfy the assertion above.
func TestTheRefusedWriteCountCanSeeARowThatWasWritten(t *testing.T) {
	db := newFenceDB(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := db.WithContext(ctx).Create(&widget{Name: "admitted"}).Error; err != nil {
		t.Fatalf("unfenced create: %v", err)
	}
	if n := widgetCount(t, db); n != 1 {
		t.Fatalf("the count saw %d rows after one admitted write, so it can never fail", n)
	}
}

func TestAnUpdateForAFencedTenantIsRefused(t *testing.T) {
	db := newFenceDB(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := db.WithContext(ctx).Create(&widget{Name: "before"}).Error; err != nil {
		t.Fatalf("seeding before the fence: %v", err)
	}

	plant(t, db, "acme")

	err := db.WithContext(ctx).Model(&widget{}).Where("name = ?", "before").
		Update("name", "after").Error
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("update for a fenced tenant: got %v, want ErrTenantPurged", err)
	}
}

// 🔴 THE SWEEP IS A DELETE, so fencing Delete would stop the erasure the fence exists to
// protect. notification-management's retention sweeper is the same shape.
func TestTheFenceDoesNotStopDeletesOrReads(t *testing.T) {
	db := newFenceDB(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := db.WithContext(ctx).Create(&widget{Name: "doomed"}).Error; err != nil {
		t.Fatalf("seeding: %v", err)
	}
	plant(t, db, "acme")

	var found []widget
	if err := db.WithContext(ctx).Find(&found).Error; err != nil {
		t.Fatalf("reading behind a fence: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("read behind a fence returned %d rows, want 1", len(found))
	}

	if err := db.WithContext(ctx).Where("name = ?", "doomed").Delete(&widget{}).Error; err != nil {
		t.Fatalf("deleting behind a fence: %v", err)
	}
	if n := widgetCount(t, db); n != 0 {
		t.Fatalf("the delete reported success but left %d row(s)", n)
	}

	// 🔴 AND THE JOURNAL'S OWN ROW LANDED. The audit insert runs after the mutation has
	// committed, so fencing it would hand the caller a refusal for a delete that happened.
	// Asserting only that Delete returned nil would pass with the row missing.
	var audits int64
	if err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).
		Model(&AuditEvent{}).Where("operation = ?", "delete").Count(&audits).Error; err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if audits != 1 {
		t.Fatalf("the delete behind a fence wrote %d audit row(s), want 1", audits)
	}
}

// A lifted fence is what lets a successor tenant at the released token write. If this
// ever fails, completing a purge bricks the token instead of releasing it.
func TestALiftedFenceLetsASuccessorWrite(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")
	lift(t, db, "acme")
	ctx := core.WithTenant(context.Background(), "acme")

	if err := db.WithContext(ctx).Create(&widget{Name: "successor"}).Error; err != nil {
		t.Fatalf("successor write behind a lifted fence: %v", err)
	}
}

// The negative control for the test above: with the fence still standing, the identical
// write must fail. Without this, a fence that never refused anything would pass it.
func TestTheLiftedFenceTestWouldFailWithTheFenceStanding(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")
	ctx := core.WithTenant(context.Background(), "acme")

	if err := db.WithContext(ctx).Create(&widget{Name: "successor"}).Error; !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("standing fence admitted the write: got %v", err)
	}
}

// 🔴 THE SPELLING THAT MATTERS MOST. event-processing carries the tenant as a plain
// column with no embed, so the scoping callbacks do not fire and its consumer path puts
// no tenant in the context. Its tenant is in the ROW, and a fence reading only the
// context would be silently off for the area holding two of the four verified
// resurrection vectors.
func TestARowCarriedTenantIsFencedWithNoTenantInContext(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")

	err := db.Create(&projection{Tenant: "acme", DeviceToken: "sensor-1", Value: "42"}).Error
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("row-carried tenant: got %v, want ErrTenantPurged", err)
	}

	if err := db.Create(&projection{Tenant: "other", DeviceToken: "sensor-1"}).Error; err != nil {
		t.Fatalf("an unfenced tenant's projection write was refused: %v", err)
	}
}

// A batch insert is one statement, and one fenced row in it must refuse the whole thing —
// a per-statement check that looked only at the first row would admit the rest.
func TestABatchIsRefusedWhenAnyRowNamesAFencedTenant(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")

	batch := []projection{
		{Tenant: "other", DeviceToken: "a"},
		{Tenant: "acme", DeviceToken: "b"},
		{Tenant: "third", DeviceToken: "c"},
	}
	if err := db.Create(&batch).Error; !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("batch containing a fenced tenant: got %v, want ErrTenantPurged", err)
	}

	var count int64
	db.Session(&gorm.Session{NewDB: true}).Model(&projection{}).Count(&count)
	if count != 0 {
		t.Fatalf("a refused batch still wrote %d row(s)", count)
	}
}

func TestAnUnfencedTenantIsUnaffected(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")
	ctx := core.WithTenant(context.Background(), "beta")

	if err := db.WithContext(ctx).Create(&widget{Name: "fine"}).Error; err != nil {
		t.Fatalf("an unfenced tenant's write was refused: %v", err)
	}
}

func TestAModelWithNoTenantIsNotFenced(t *testing.T) {
	db := newFenceDB(t)
	plant(t, db, "acme")

	sys := core.WithSystemContext(context.Background())
	if err := db.WithContext(sys).Create(&gadget{Name: "instance-level"}).Error; err != nil {
		t.Fatalf("a model with no tenant was fenced: %v", err)
	}
}

// 🔴 FAIL CLOSED. An unreadable fence is not an absent one — and this is the direction a
// remote gate cannot fail in, which is the whole reason the fence is local.
func TestAnUnreadableFenceRefusesTheWrite(t *testing.T) {
	db := newFenceDB(t)
	if err := db.Migrator().DropTable(&PurgedTenant{}); err != nil {
		t.Fatalf("dropping the fence table: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")

	err := db.WithContext(ctx).Create(&widget{Name: "unchecked"}).Error
	if err == nil {
		t.Fatal("a write was admitted with no fence table to check it against")
	}
	if errors.Is(err, ErrTenantPurged) {
		t.Fatalf("an unreadable fence should not claim the tenant is purged: %v", err)
	}
}

// Updates(map) names no tenant in its destination, so the context is the only source.
func TestAMapUpdateIsFencedFromTheContext(t *testing.T) {
	db := newFenceDB(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := db.WithContext(ctx).Create(&widget{Name: "before"}).Error; err != nil {
		t.Fatalf("seeding: %v", err)
	}
	plant(t, db, "acme")

	err := db.WithContext(ctx).Model(&widget{}).Where("name = ?", "before").
		Updates(map[string]any{"name": "after"}).Error
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("map update behind a fence: got %v, want ErrTenantPurged", err)
	}
}

func TestDestTenantsReadsEveryShapeAStatementCanCarry(t *testing.T) {
	one := projection{Tenant: "a"}
	many := []projection{{Tenant: "a"}, {Tenant: "b"}}
	for name, tc := range map[string]struct {
		dest any
		want int
	}{
		"struct":                    {one, 1},
		"pointer":                   {&one, 1},
		"slice":                     {many, 2},
		"pointer to slice":          {&many, 2},
		"column map":                {map[string]any{"tenant_id": "a"}, 1},
		"column map (plain tenant)": {map[string]any{"tenant": "a"}, 1},
		"map with no tenant":        {map[string]any{"name": "a"}, 0},
		"nil":                       {nil, 0},
		"unrelated":                 {42, 0},
	} {
		t.Run(name, func(t *testing.T) {
			got := destTenants(tc.dest, "Tenant")
			if len(got) != tc.want {
				t.Fatalf("destTenants(%s) = %v, want %d value(s)", name, got, tc.want)
			}
		})
	}
}
