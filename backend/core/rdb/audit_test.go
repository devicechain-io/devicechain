// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
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
	// Register the audit callbacks AFTER migrating so AutoMigrate's own statements
	// are not audited (it runs before the callbacks exist).
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
