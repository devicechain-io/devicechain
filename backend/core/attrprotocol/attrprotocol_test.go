// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package attrprotocol

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A root handle is not a transaction: a transaction-local setting there would mark nothing.
func TestMarkRefusesARootHandle(t *testing.T) {
	db := openTestDB(t)
	if err := Mark(db); !errors.Is(err, ErrNotInTransaction) {
		t.Fatalf("Mark on a root handle = %v, want ErrNotInTransaction", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return Mark(tx) }); err != nil {
		t.Fatalf("Mark inside a transaction must succeed: %v", err)
	}
}

func TestMarkerIsTransactionLocal(t *testing.T) {
	if MarkerSQL != "SELECT set_config('devicechain.attr_protocol', '1', true)" {
		t.Fatalf("the marker must be transaction-local: %s", MarkerSQL)
	}
}

// Only a plan that deletes the protocol's tables gains the hook, and an existing hook runs first.
func TestRegisterPurge(t *testing.T) {
	other := &tenantpurge.Plan{Entries: []tenantpurge.Entry{{Table: tenantpurge.Table{Schema: "event-management", Name: "events"}}}}
	RegisterPurge(other)
	if other.BeforeDelete != nil {
		t.Fatal("a plan without the protocol's tables must not be marked")
	}
	for _, table := range []string{AttributesTable, GenerationsTable} {
		calls := []string{}
		plan := &tenantpurge.Plan{
			Entries:      []tenantpurge.Entry{{Table: tenantpurge.Table{Schema: Schema, Name: table}}},
			BeforeDelete: func(*gorm.DB, string) error { calls = append(calls, "previous"); return nil },
		}
		RegisterPurge(plan)
		if plan.BeforeDelete == nil {
			t.Fatalf("%s: the plan must be marked", table)
		}
		db := openTestDB(t)
		if err := db.Transaction(func(tx *gorm.DB) error { return plan.BeforeDelete(tx, "t") }); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if len(calls) != 1 {
			t.Fatalf("%s: the existing hook must still run once, ran %d times", table, len(calls))
		}
		if err := plan.BeforeDelete(db, "t"); !errors.Is(err, ErrNotInTransaction) {
			t.Fatalf("%s: the purge hook must refuse a root handle: %v", table, err)
		}
	}
}
