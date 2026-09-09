// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newAuditTestDB spins up an in-memory sqlite database with both the tenant-scope
// and the audit-journal callbacks registered, mirroring how a service's
// RdbManager wires them at startup.
func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&widget{}, &gadget{}, &AuditEvent{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	// Registering the audit callbacks after migrating is incidental, not a
	// requirement: this used to claim it kept AutoMigrate's own statements out of
	// the journal, which cannot happen in either order. The migrator issues DDL
	// through Exec/Raw, and those never reach the Create/Update/Delete processors
	// the journal hooks — so there is nothing for it to audit.
	if err := RegisterAuditJournal(db); err != nil {
		t.Fatalf("failed to register audit journal: %v", err)
	}
	return db
}

// readAudit returns all audit rows ordered by id, read under a system context so
// the tenant-scope query callback does not filter them.
func readAudit(t *testing.T, db *gorm.DB) []AuditEvent {
	t.Helper()
	var rows []AuditEvent
	if err := db.WithContext(core.WithSystemContext(context.Background())).
		Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return rows
}

// A create/update/delete by an authenticated subject each emits exactly one audit
// row, attributed to the subject and the acting tenant, with the affected row's
// primary key — and the journal's own inserts are not themselves audited
// (recursion guard).
func TestAuditCaptureCreateUpdateDelete(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "derek", Tenant: "A"})

	w := &widget{Name: "w1"}
	if err := db.WithContext(ctx).Create(w).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.WithContext(ctx).Model(&widget{}).Where("id = ?", w.ID).Update("name", "w2").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := db.WithContext(ctx).Delete(&widget{}, w.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows := readAudit(t, db)
	if len(rows) != 3 {
		t.Fatalf("expected exactly 3 audit rows (no audit-of-audit recursion), got %d", len(rows))
	}
	wantOps := []string{"create", "update", "delete"}
	for i, row := range rows {
		if row.Operation != wantOps[i] {
			t.Errorf("row %d: operation = %q, want %q", i, row.Operation, wantOps[i])
		}
		if row.Actor != "derek" {
			t.Errorf("row %d: actor = %q, want derek", i, row.Actor)
		}
		if row.TenantId != "A" {
			t.Errorf("row %d: tenant = %q, want A", i, row.TenantId)
		}
		if row.TableName != "widgets" {
			t.Errorf("row %d: table = %q, want widgets", i, row.TableName)
		}
		if row.Category != AuditCategoryMutation {
			t.Errorf("row %d: category = %q, want %q", i, row.Category, AuditCategoryMutation)
		}
		if row.RowsAffected != 1 {
			t.Errorf("row %d: rows_affected = %d, want 1", i, row.RowsAffected)
		}
	}
}

// RecordAuthEvent writes an auth-category row directly (no mutation), tolerating
// the no-tenant case of a failed login for an unknown user, and is itself not
// re-audited.
func TestRecordAuthEvent(t *testing.T) {
	db := newAuditTestDB(t)
	mgr := &RdbManager{Database: db}

	if err := mgr.RecordAuthEvent(context.Background(), AuditOpLogin, "derek", "A"); err != nil {
		t.Fatalf("record login: %v", err)
	}
	// A failed login for an unknown user has no tenant — must still record.
	if err := mgr.RecordAuthEvent(context.Background(), AuditOpLoginFailed, "ghost", ""); err != nil {
		t.Fatalf("record failed login: %v", err)
	}

	rows := readAudit(t, db)
	if len(rows) != 2 {
		t.Fatalf("expected 2 auth rows (no recursion), got %d", len(rows))
	}
	for _, row := range rows {
		if row.Category != AuditCategoryAuth {
			t.Errorf("category = %q, want %q", row.Category, AuditCategoryAuth)
		}
		if row.TableName != "" {
			t.Errorf("auth row should have no table, got %q", row.TableName)
		}
	}
	if rows[0].Operation != AuditOpLogin || rows[0].Actor != "derek" || rows[0].TenantId != "A" {
		t.Errorf("unexpected login row: %+v", rows[0])
	}
	if rows[1].Operation != AuditOpLoginFailed || rows[1].Actor != "ghost" || rows[1].TenantId != "" {
		t.Errorf("unexpected failed-login row: %+v", rows[1])
	}
}

// exemptWidget opts out of the audit journal (stands in for the data-plane
// telemetry / device-state tables).
type exemptWidget struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

func (exemptWidget) AuditExempt() bool { return true }

// A model that implements AuditExempt produces no audit rows, even on a mutation
// that otherwise would be recorded.
func TestAuditExemptModelNotRecorded(t *testing.T) {
	db := newAuditTestDB(t)
	if err := db.AutoMigrate(&exemptWidget{}); err != nil {
		t.Fatalf("migrate exemptWidget: %v", err)
	}
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "derek", Tenant: "A"})

	if err := db.WithContext(ctx).Create(&exemptWidget{Name: "telemetry"}).Error; err != nil {
		t.Fatalf("create exempt: %v", err)
	}
	// A non-exempt mutation in the same db still records, proving the journal is
	// live and the exemption is selective.
	if err := db.WithContext(ctx).Create(&widget{Name: "entity"}).Error; err != nil {
		t.Fatalf("create widget: %v", err)
	}

	rows := readAudit(t, db)
	if len(rows) != 1 {
		t.Fatalf("expected only the non-exempt mutation to be audited, got %d rows", len(rows))
	}
	if rows[0].TableName != "widgets" {
		t.Fatalf("audited the wrong table: %q", rows[0].TableName)
	}
}

// A mutation under a deliberate system context (no JWT) is attributed to "system".
func TestAuditActorSystemContext(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := core.WithSystemContext(context.Background())

	// gadget is not tenant-scoped, so it persists under a system context.
	if err := db.WithContext(ctx).Create(&gadget{Name: "g1"}).Error; err != nil {
		t.Fatalf("create gadget: %v", err)
	}

	rows := readAudit(t, db)
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	if rows[0].Actor != "system" {
		t.Errorf("actor = %q, want system", rows[0].Actor)
	}
	if rows[0].Operation != "create" || rows[0].TableName != "gadgets" {
		t.Errorf("unexpected audit row: %+v", rows[0])
	}
}

// The journal is tenant-scoped on read like any other table: tenant B cannot see
// tenant A's audit rows.
func TestAuditJournalTenantScopedRead(t *testing.T) {
	db := newAuditTestDB(t)
	ctxA := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "ann", Tenant: "A"})
	if err := db.WithContext(ctxA).Create(&widget{Name: "wa"}).Error; err != nil {
		t.Fatalf("create under A: %v", err)
	}

	var bRows []AuditEvent
	ctxB := core.WithTenant(context.Background(), "B")
	if err := db.WithContext(ctxB).Find(&bRows).Error; err != nil {
		t.Fatalf("read under B: %v", err)
	}
	if len(bRows) != 0 {
		t.Fatalf("tenant B must not see tenant A's audit rows, got %d", len(bRows))
	}
}

// 🔴 THE FAIL-CLOSED CONTRACT, WHICH THE TESTS ABOVE STRUCTURALLY CANNOT SEE.
//
// The journal's hooks are After("gorm:create"/"update"/"delete"), which gorm sorts
// past the commit callback, and a failed journal write is reported with
// db.AddError. So a mutation whose audit row cannot land reports FAILURE for a
// change that has ALREADY HAPPENED. Every test above performs a mutation, asserts
// it returned nil, and then reads the journal — none of them can observe an error
// raised over a completed write, so nothing in this package pinned the reporting
// half of the contract.
//
// This drives it: take the journal's table away, then mutate. The error is
// required AND so is the state the mutation left behind, because an assertion on
// the error alone would also pass if the write had been refused or rolled back,
// which is the opposite behaviour — a change that did not happen, reported as
// failed.
//
// It runs over all three operations because the two halves of the contract fail
// differently. The three hooks share one closure, so the fail-closed reporting
// cannot break for one operation alone — but they are three separate
// registrations, and it is the placement each one is given that puts the journal
// write past the commit. Delete is the arm downstream reasoning leans on: the
// erasure fence excludes the journal precisely because the journal runs after the
// sweeper's delete has committed (see RegisterAuditJournal).
func TestAuditJournalFailureFailsTheMutationItAlreadyMade(t *testing.T) {
	for _, tc := range []struct {
		op string
		// mutate performs, on a database whose journal table has been dropped, the
		// mutation whose audit row therefore cannot land.
		mutate func(db *gorm.DB, ctx context.Context, seeded *widget) error
		// landed asserts the mutation took effect anyway, and says what "took
		// effect" means for this operation.
		landed func(t *testing.T, db *gorm.DB, ctx context.Context, seeded *widget)
	}{
		{
			op: "create",
			mutate: func(db *gorm.DB, ctx context.Context, _ *widget) error {
				return db.WithContext(ctx).Create(&widget{Name: "created"}).Error
			},
			landed: func(t *testing.T, db *gorm.DB, ctx context.Context, _ *widget) {
				if n := countWidgets(t, db, ctx, "created"); n != 1 {
					t.Fatalf("the created widget matched %d rows, want 1 — the failure "+
						"reported above is supposed to be one raised over an insert that "+
						"ALREADY COMMITTED; a refused insert is the opposite behaviour", n)
				}
			},
		},
		{
			op: "update",
			mutate: func(db *gorm.DB, ctx context.Context, seeded *widget) error {
				return db.WithContext(ctx).Model(&widget{}).
					Where("id = ?", seeded.ID).Update("name", "moved").Error
			},
			landed: func(t *testing.T, db *gorm.DB, ctx context.Context, _ *widget) {
				if n := countWidgets(t, db, ctx, "moved"); n != 1 {
					t.Fatalf("the updated widget matched %d rows, want 1 — the failure "+
						"reported above is supposed to be one raised over an update that "+
						"ALREADY COMMITTED; a rolled-back update is the opposite behaviour", n)
				}
			},
		},
		{
			op: "delete",
			mutate: func(db *gorm.DB, ctx context.Context, seeded *widget) error {
				return db.WithContext(ctx).Delete(&widget{}, seeded.ID).Error
			},
			landed: func(t *testing.T, db *gorm.DB, ctx context.Context, _ *widget) {
				if n := countWidgets(t, db, ctx, "seeded"); n != 0 {
					t.Fatalf("the deleted widget still matched %d rows, want 0 — the failure "+
						"reported above is supposed to be one raised over a delete that "+
						"ALREADY COMMITTED, which is what lets the erasure fence exclude "+
						"the journal; a rolled-back delete is the opposite behaviour", n)
				}
			},
		},
	} {
		t.Run(tc.op, func(t *testing.T) {
			db := newAuditTestDB(t)
			ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"),
				&auth.Claims{Username: "derek", Tenant: "A"})

			seeded := &widget{Name: "seeded"}
			if err := db.WithContext(ctx).Create(seeded).Error; err != nil {
				t.Fatalf("seed a widget: %v", err)
			}
			if err := db.Migrator().DropTable(&AuditEvent{}); err != nil {
				t.Fatalf("drop the audit journal's table: %v", err)
			}

			err := tc.mutate(db, ctx, seeded)
			if err == nil {
				t.Fatalf("the %s reported success even though its journal write could not "+
					"land — the journal fails closed by design, so a mutation it could "+
					"not record must never report nil", tc.op)
			}
			// The reported error must be the journal's, so this pin cannot be satisfied
			// by some unrelated failure of the mutation itself.
			if !strings.Contains(err.Error(), "audit_events") {
				t.Fatalf("the %s reported %v, which does not name the journal's table — this "+
					"test only pins the contract while the error it observes is the "+
					"journal write's", tc.op, err)
			}

			tc.landed(t, db, ctx, seeded)
		})
	}
}

// countWidgets returns how many widgets carry the given name, read under the
// caller's tenant context like any other tenant-scoped query.
func countWidgets(t *testing.T, db *gorm.DB, ctx context.Context, name string) int {
	t.Helper()
	var rows []widget
	if err := db.WithContext(ctx).Where("name = ?", name).Find(&rows).Error; err != nil {
		t.Fatalf("read widgets named %q: %v", name, err)
	}
	return len(rows)
}
