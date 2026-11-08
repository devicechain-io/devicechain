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

func strOf(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

// createWidgetAs makes one audited mutation under claims and returns its journal row.
func createWidgetAs(t *testing.T, db *gorm.DB, claims *auth.Claims) AuditEvent {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "A")
	if claims != nil {
		ctx = auth.WithClaims(ctx, claims)
	}
	if err := db.WithContext(ctx).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	rows := readAudit(t, db)
	return rows[len(rows)-1]
}

// TestJournalStampsClientIdFromClaims: an agent's mutation is distinguishable from the same
// person's console mutation. The client comes from the client_id claim and the session from
// the sid claim, each independently: a row with both populated and a row with only one pin
// that neither is derived from the other.
func TestJournalStampsClientIdFromClaims(t *testing.T) {
	db := newAuditTestDB(t)

	both := createWidgetAs(t, db, &auth.Claims{
		Username: "alice@example.com", Tenant: "A", ClientId: "claude-desktop",
		SessionID: "sid-123", Scope: "read-only",
	})
	if got := strOf(both.ClientId); got != "claude-desktop" {
		t.Errorf("client_id = %s, want claude-desktop", got)
	}
	if got := strOf(both.SessionId); got != "sid-123" {
		t.Errorf("session_id = %s, want sid-123 (stamped from the sid claim, not the client)", got)
	}

	clientOnly := createWidgetAs(t, db, &auth.Claims{Username: "alice@example.com", Tenant: "A", ClientId: "cli"})
	if got := strOf(clientOnly.ClientId); got != "cli" {
		t.Errorf("client_id = %s, want cli", got)
	}
	if clientOnly.SessionId != nil {
		t.Errorf("a token with no sid must leave session_id NULL (unknown), got %s", strOf(clientOnly.SessionId))
	}

	sidOnly := createWidgetAs(t, db, &auth.Claims{Username: "alice@example.com", Tenant: "A", SessionID: "sid-9"})
	if sidOnly.ClientId != nil {
		t.Errorf("a token with no client_id must leave client_id NULL, got %s", strOf(sidOnly.ClientId))
	}
	if got := strOf(sidOnly.SessionId); got != "sid-9" {
		t.Errorf("session_id = %s, want sid-9", got)
	}
}

// A console token, a system operation and an actor-named write carry neither, and the two
// RESERVED columns stay NULL on every row: nothing writes them until the proposal gate exists.
func TestJournalLeavesIdentityNullWhereNoTokenSaysOtherwise(t *testing.T) {
	db := newAuditTestDB(t)

	console := createWidgetAs(t, db, &auth.Claims{Username: "alice@example.com", Tenant: "A", TokenType: auth.TokenTypeAccess})
	system := func() AuditEvent {
		ctx := core.WithSystemContext(context.Background())
		if err := db.WithContext(ctx).Create(&gadget{Name: "s"}).Error; err != nil {
			t.Fatal(err)
		}
		rows := readAudit(t, db)
		return rows[len(rows)-1]
	}()
	named := func() AuditEvent {
		ctx := WithAuditActor(core.WithTenant(context.Background(), "A"), "provisioning-profile:p")
		if err := db.WithContext(ctx).Create(&widget{Name: "n"}).Error; err != nil {
			t.Fatal(err)
		}
		rows := readAudit(t, db)
		return rows[len(rows)-1]
	}()
	agent := createWidgetAs(t, db, &auth.Claims{Username: "a@example.com", Tenant: "A", ClientId: "c", SessionID: "s"})

	for name, row := range map[string]AuditEvent{"console": console, "system": system, "named actor": named, "agent": agent} {
		if row.OnBehalfOf != nil || row.Proposal != nil {
			t.Errorf("%s row: reserved columns must stay NULL, got on_behalf_of=%s proposal=%s",
				name, strOf(row.OnBehalfOf), strOf(row.Proposal))
		}
	}
	for name, row := range map[string]AuditEvent{"console": console, "system": system, "named actor": named} {
		if row.ClientId != nil || row.SessionId != nil {
			t.Errorf("%s row must carry no agent identity, got client_id=%s session_id=%s",
				name, strOf(row.ClientId), strOf(row.SessionId))
		}
	}
}

// An auth-event row (a login) is not stamped even when the request context carries claims.
func TestAuthEventRowsCarryNoAgentIdentity(t *testing.T) {
	db := newAuditTestDB(t)
	row := AuditEvent{Category: AuditCategoryAuth, Operation: AuditOpLogin, Actor: "a@example.com"}
	if err := db.WithContext(core.WithSystemContext(context.Background())).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ClientId != nil || row.SessionId != nil || row.OnBehalfOf != nil || row.Proposal != nil {
		t.Fatalf("an auth event must not be stamped: %+v", row)
	}
}

// UPGRADE: the columns arrive through the core-owned AutoMigrate on a database that already
// holds journal rows in the previous shape. Existing rows read back NULL (unknown), and an
// INSERT written by an OLD pod, which names none of the new columns, still succeeds against
// the upgraded table — the property a rolling upgrade depends on.
func TestAuditIdentityColumnsAreAdditiveOnAnExistingJournal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The previous shape, spelled out: this is a snapshot, not the live model.
	if err := db.Exec(`CREATE TABLE audit_events (
		id integer primary key autoincrement, occurred_time datetime not null, tenant_id text,
		category text not null, actor text, table_name text, operation text not null,
		entity_pk text, entity_label text, rows_affected integer)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO audit_events (occurred_time, tenant_id, category, actor, operation)
		VALUES (CURRENT_TIMESTAMP, 'A', 'mutation', 'old@example.com', 'create')`).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.AutoMigrate(&AuditEvent{}); err != nil {
		t.Fatalf("AutoMigrate over the previous shape: %v", err)
	}
	for _, col := range []string{"client_id", "session_id", "on_behalf_of", "proposal"} {
		if !db.Migrator().HasColumn(&AuditEvent{}, col) {
			t.Errorf("column %s was not added", col)
		}
	}
	// An old pod's INSERT, naming none of the new columns.
	if err := db.Exec(`INSERT INTO audit_events (occurred_time, tenant_id, category, actor, operation)
		VALUES (CURRENT_TIMESTAMP, 'A', 'mutation', 'old2@example.com', 'update')`).Error; err != nil {
		t.Fatalf("an old-shape INSERT must survive the upgrade: %v", err)
	}
	var rows []AuditEvent
	if err := db.WithContext(core.WithSystemContext(context.Background())).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.ClientId != nil || r.SessionId != nil || r.OnBehalfOf != nil || r.Proposal != nil {
			t.Errorf("a pre-upgrade row must read back NULL in the new columns: %+v", r)
		}
	}
}
