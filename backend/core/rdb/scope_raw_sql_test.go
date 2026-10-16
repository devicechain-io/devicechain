// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// 🔴 A statement whose SQL is ALREADY WRITTEN (Raw) still runs the query and row
// callbacks — only Exec runs on the callback-less Raw processor. For a scoped caller that
// is the one shape in which the scope callback can be reached and still filter nothing:
// it finds the model, finds the rule, ADDS the predicate, and gorm discards the clause
// because BuildQuerySQL only builds when the SQL is empty. The callback has recorded no
// error and the resolver has every reason to believe the read was scoped. The tenant
// callback has the same blind spot and documents it as the price of legitimate raw
// reads; a SCOPED caller has no legitimate raw read, so here it is a refusal.
func TestARawReadIsRefusedForAScopedCaller(t *testing.T) {
	db := newScopeTestDB(t)
	seedReadings(t, db)
	scoped := scopedCtx("A", "customer:acme-corp")

	// Raw + Scan into the ruled model: the callbacks run, the schema is found, and the
	// predicate the rule produces has nowhere to go.
	var scanned []scopedReading
	err := db.WithContext(scoped).Raw("SELECT * FROM scoped_readings").Scan(&scanned).Error
	if !isScopeRefusal(err) {
		t.Fatalf("a scoped Raw().Scan was not refused: err=%v rows=%d (every tenant's rows, unfiltered)", err, len(scanned))
	}

	// Raw + Find, the same path with a different verb.
	var found []scopedReading
	err = db.WithContext(scoped).Raw("SELECT * FROM scoped_readings").Find(&found).Error
	if !isScopeRefusal(err) {
		t.Fatalf("a scoped Raw().Find was not refused: err=%v rows=%d", err, len(found))
	}

	// Raw + Rows: no destination at all, so no schema and no tenant field either — the
	// tenant callback waves it through as "not classifiable, SQL already built".
	rows, err := db.WithContext(scoped).Raw("SELECT * FROM scoped_readings").Rows()
	if err == nil {
		_ = rows.Close()
	}
	if !isScopeRefusal(err) {
		t.Fatalf("a scoped Raw().Rows was not refused: err=%v", err)
	}

	// Negative controls, so the refusals above are the scope and not the fixture:
	// unscoped, the identical raw read is served (the documented tenant-callback
	// gap), and the scoped caller's ORM read of the same table is served, filtered.
	var control []scopedReading
	if err := db.WithContext(unscopedCtx("A")).Raw("SELECT * FROM scoped_readings").Scan(&control).Error; err != nil || len(control) == 0 {
		t.Fatalf("the unscoped control raw read: err=%v rows=%d", err, len(control))
	}
	if got := readingNames(t, db, scoped); len(got) != 1 || got[0] != "acme-pump" {
		t.Fatalf("the scoped ORM control read %v, want [acme-pump]", got)
	}
}

// The system context keeps its bypass for raw SQL under a scoped request too: that is
// how the audit journal and the migrator issue statements no call site can retype, and
// the raw refusal above must not reach them.
func TestASystemContextRawReadIsNotRefusedUnderAScopedRequest(t *testing.T) {
	db := newScopeTestDB(t)
	seedReadings(t, db)
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A",
		Authorities: []string{"*"}, ScopeAnchors: []string{"customer:acme-corp"}}
	ctx := core.WithSystemContext(auth.WithClaims(core.WithTenant(context.Background(), "A"), claims))
	var out []scopedReading
	if err := db.WithContext(ctx).Raw("SELECT * FROM scoped_readings").Scan(&out).Error; err != nil || len(out) == 0 {
		t.Fatalf("a system-context raw read under a scoped request: err=%v rows=%d", err, len(out))
	}
}
