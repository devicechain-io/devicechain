// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
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
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "alice", Tenant: "A"})

	w := &widget{Name: "w1"}
	if err := db.WithContext(ctx).Create(w).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.WithContext(ctx).Model(w).Where("id = ?", w.ID).Update("name", "w2").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := db.WithContext(ctx).Delete(w).Error; err != nil {
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
		if row.Actor != "alice" {
			t.Errorf("row %d: actor = %q, want alice", i, row.Actor)
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
		// 🔴 THE CLAIM IN THIS TEST'S OWN DOC COMMENT, WHICH IT DID NOT CHECK UNTIL NOW.
		// It said "with the affected row's primary key" and asserted every other column,
		// while driving the update through Model(&widget{}) — the one shape that loses
		// the key. So the assertion that would have failed was the assertion missing, on
		// the statement that needed it. A test named for a property it never reads is
		// indistinguishable from one that holds.
		if row.EntityPK != fmt.Sprint(w.ID) {
			t.Errorf("row %d (%s): entity_pk = %q, want %q — the journal recorded that "+
				"something changed without recording which row", i, row.Operation, row.EntityPK, fmt.Sprint(w.ID))
		}
	}
}

// The other half, and the reason the assertion above is about a SPECIFIC shape rather
// than about auditing in general: when no single row is identified, an empty primary key
// is the TRUTHFUL entry and RowsAffected carries what is known instead.
//
// Keeping both pinned is what makes the pair meaningful. "Always records a key" would be
// wrong here and is not what the journal promises; "never records one" was the defect.
// The difference between them is whether the caller had the identity to give.
func TestAuditRecordsNoPrimaryKeyWhereNoSingleRowIsIdentified(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "alice", Tenant: "A"})

	for _, name := range []string{"a", "b", "c"} {
		if err := db.WithContext(ctx).Create(&widget{Name: name}).Error; err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	before := len(readAudit(t, db))

	// A condition-only update over several rows: no one key describes what changed.
	if err := db.WithContext(ctx).Model(&widget{}).
		Where("name IN ?", []string{"a", "b"}).
		Update("name", "z").Error; err != nil {
		t.Fatalf("bulk update: %v", err)
	}

	rows := readAudit(t, db)
	if len(rows) != before+1 {
		t.Fatalf("expected one audit row for the bulk update, got %d new", len(rows)-before)
	}
	row := rows[len(rows)-1]
	if row.EntityPK != "" {
		t.Errorf("entity_pk = %q, want empty: two rows changed, so naming one of them "+
			"would be a less accurate record than naming none", row.EntityPK)
	}
	if row.RowsAffected != 2 {
		t.Errorf("rows_affected = %d, want 2 — it is the only quantity the journal can "+
			"honestly report here", row.RowsAffected)
	}
}

// RecordAuthEvent writes an auth-category row directly (no mutation), tolerating
// the no-tenant case of a failed login for an unknown user, and is itself not
// re-audited.
func TestRecordAuthEvent(t *testing.T) {
	db := newAuditTestDB(t)
	mgr := &RdbManager{Database: db}

	if err := mgr.RecordAuthEvent(context.Background(), AuditOpLogin, "alice", "A"); err != nil {
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
	if rows[0].Operation != AuditOpLogin || rows[0].Actor != "alice" || rows[0].TenantId != "A" {
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
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "alice", Tenant: "A"})

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

// 🔴 THE JOURNAL ROW AND THE MUTATION ARE ONE WRITE: a journal write that fails fails the
// mutation AND rolls it back.
//
// This test used to pin the opposite, and the old pin is the defect: the journal's hooks
// ran past the commit callback, so a mutation whose audit row could not land reported
// FAILURE for a change that had ALREADY HAPPENED and was never journaled. The hooks now
// run inside the statement's transaction (see RegisterAuditJournal), so the error is
// truthful: the change is absent.
//
// It drives it by taking the journal's table away and then mutating. Both halves are
// required, because an assertion on the error alone would also pass for the old
// behaviour. It runs over all three operations because they are three separate
// registrations, and the placement each is given is what puts the journal write before
// the commit. Delete is the arm downstream reasoning leans on: the erasure fence excludes
// the journal precisely because a refusal of its row would now roll back the sweeper's
// delete.
func TestAuditJournalFailureRollsBackTheMutation(t *testing.T) {
	for _, tc := range []struct {
		op string
		// mutate performs, on a database whose journal table has been dropped, the
		// mutation whose audit row therefore cannot land.
		mutate func(db *gorm.DB, ctx context.Context, seeded *widget) error
		// landed asserts the mutation did NOT take effect.
		landed func(t *testing.T, db *gorm.DB, ctx context.Context, seeded *widget)
	}{
		{
			op: "create",
			mutate: func(db *gorm.DB, ctx context.Context, _ *widget) error {
				return db.WithContext(ctx).Create(&widget{Name: "created"}).Error
			},
			landed: func(t *testing.T, db *gorm.DB, ctx context.Context, _ *widget) {
				if n := countWidgets(t, db, ctx, "created"); n != 0 {
					t.Fatalf("the created widget matched %d rows, want 0 — a create whose "+
						"journal row failed must roll back, not report failure over a saved row", n)
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
				if n := countWidgets(t, db, ctx, "moved"); n != 0 {
					t.Fatalf("the updated widget matched %d rows, want 0 — an update whose "+
						"journal row failed must roll back, not report failure over a saved change", n)
				}
				if n := countWidgets(t, db, ctx, "seeded"); n != 1 {
					t.Fatalf("the seeded widget matched %d rows after the rolled-back update, want 1", n)
				}
			},
		},
		{
			op: "delete",
			mutate: func(db *gorm.DB, ctx context.Context, seeded *widget) error {
				return db.WithContext(ctx).Delete(&widget{}, seeded.ID).Error
			},
			landed: func(t *testing.T, db *gorm.DB, ctx context.Context, _ *widget) {
				if n := countWidgets(t, db, ctx, "seeded"); n != 1 {
					t.Fatalf("the seeded widget matched %d rows, want 1 — a delete whose "+
						"journal row failed must roll back, not report failure over a removed row", n)
				}
			},
		},
	} {
		t.Run(tc.op, func(t *testing.T) {
			db := newAuditTestDB(t)
			ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"),
				&auth.Claims{Username: "alice", Tenant: "A"})

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

func sysWidgetCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.WithContext(core.WithSystemContext(context.Background())).Model(&widget{}).Count(&n).Error; err != nil {
		t.Fatalf("counting widgets: %v", err)
	}
	return n
}

// The success path writes both, and an explicit transaction that rolls back takes the
// audit row with it: there is no journal entry for a change that did not happen.
func TestAuditRowSharesTheCallersTransaction(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "alice", Tenant: "A"})

	if err := db.WithContext(ctx).Create(&widget{Name: "kept"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, a := sysWidgetCount(t, db), len(readAudit(t, db)); n != 1 || a != 1 {
		t.Fatalf("widgets=%d audit=%d; want one of each", n, a)
	}

	rollback := fmt.Errorf("rollback")
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&widget{Name: "undone"}).Error; err != nil {
			return err
		}
		return rollback
	})
	if err != rollback {
		t.Fatalf("transaction error = %v", err)
	}
	if n, a := sysWidgetCount(t, db), len(readAudit(t, db)); n != 1 || a != 1 {
		t.Fatalf("after a rolled-back transaction widgets=%d audit=%d; want 1 and 1", n, a)
	}
}

// The ordering is the whole mechanism, so it is asserted directly: the journal callbacks
// sit between the statement and the commit callback.
func TestAuditCallbacksRunBeforeTheCommit(t *testing.T) {
	db := newAuditTestDB(t)
	// gorm exposes no ordered listing, so observe it: a probe registered Before the
	// commit callback and After the journal must see the audit row inside the open
	// transaction.
	seen := -1
	if err := db.Callback().Create().After("dc:audit_create").Before("gorm:commit_or_rollback_transaction").
		Register("probe_after_audit", func(tx *gorm.DB) {
			var n int64
			s := tx.Session(&gorm.Session{NewDB: true, Context: core.WithSystemContext(tx.Statement.Context)})
			if err := s.Model(&AuditEvent{}).Count(&n).Error; err == nil {
				seen = int(n)
			}
		}); err != nil {
		t.Fatalf("registering probe: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "A")
	if err := db.WithContext(ctx).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if seen != 1 {
		t.Fatalf("a callback ordered After the journal and Before the commit saw %d audit rows, want 1", seen)
	}
}

// WithAuditActor names the actor of a write that has no user behind it, in a system context
// (a device registering itself) and under a tenant alike.
func TestWithAuditActorNamesAWriteWithNoUser(t *testing.T) {
	db := newAuditTestDB(t)
	for name, base := range map[string]context.Context{
		"system": core.WithSystemContext(context.Background()),
		"tenant": core.WithTenant(context.Background(), "A"),
	} {
		ctx := WithAuditActor(base, "provisioning-profile:factory-a")
		if err := db.WithContext(ctx).Create(&gadget{Name: name}).Error; err != nil {
			t.Fatalf("%s: create gadget: %v", name, err)
		}
	}
	rows := readAudit(t, db)
	if len(rows) != 2 {
		t.Fatalf("expected 2 audit rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Actor != "provisioning-profile:factory-a" {
			t.Errorf("actor = %q, want the named actor", r.Actor)
		}
	}
}

// Claims win: naming an actor can never relabel an authenticated user's write.
func TestWithAuditActorNeverOverridesClaims(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "A"), &auth.Claims{Username: "alice", Tenant: "A"})
	ctx = WithAuditActor(ctx, "provisioning-profile:factory-a")
	if err := db.WithContext(ctx).Create(&widget{Name: "w1"}).Error; err != nil {
		t.Fatalf("create widget: %v", err)
	}
	rows := readAudit(t, db)
	if len(rows) != 1 || rows[0].Actor != "alice" {
		t.Fatalf("want one row by alice, got %+v", rows)
	}
}

// With nothing named, a bare context is still unattributed, and an empty name changes
// nothing.
func TestWithAuditActorEmptyLeavesTheContextAlone(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := WithAuditActor(core.WithSystemContext(context.Background()), "")
	if err := db.WithContext(ctx).Create(&gadget{Name: "g"}).Error; err != nil {
		t.Fatalf("create gadget: %v", err)
	}
	if rows := readAudit(t, db); len(rows) != 1 || rows[0].Actor != "system" {
		t.Fatalf("want the system actor, got %+v", rows)
	}
}
