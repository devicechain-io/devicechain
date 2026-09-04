// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// fencePlanOf builds a plan from (schema, table) pairs. Only the table identity matters to
// fenceSchemas — it reads the catalog's shape, not any classification.
func fencePlanOf(pairs ...[2]string) *Plan {
	p := &Plan{}
	for _, pair := range pairs {
		p.Entries = append(p.Entries, Entry{Table: Table{Schema: pair[0], Name: pair[1]}})
	}
	return p
}

func fenceArea(schema string) [2]string {
	return [2]string{schema, strings.ReplaceAll(schema, "-", "_") + "_migrations"}
}

func fenceOf(schema string) [2]string { return [2]string{schema, rdb.FenceTable} }

func TestTheAreaSetIsDerivedFromThePlanRatherThanListed(t *testing.T) {
	plan := fencePlanOf(
		fenceArea("device-management"), fenceOf("device-management"), [2]string{"device-management", "devices"},
		fenceArea("event-processing"), fenceOf("event-processing"), [2]string{"event-processing", "device_rosters"},
	)
	got, err := fenceSchemas(plan)
	if err != nil {
		t.Fatalf("fenceSchemas: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want both areas", got)
	}
}

// 🔴 A tenant-bearing schema that is not a functional area — TimescaleDB's internal
// schema, where an aggregate's materialization lives — carries no fence table and must
// not be looked for there.
func TestANonAreaSchemaIsNotExpectedToCarryAFence(t *testing.T) {
	plan := fencePlanOf(
		fenceArea("event-management"), fenceOf("event-management"),
		[2]string{"_timescaledb_internal", "_materialized_hypertable_7"},
	)
	got, err := fenceSchemas(plan)
	if err != nil {
		t.Fatalf("fenceSchemas: %v", err)
	}
	if len(got) != 1 || got[0] != "event-management" {
		t.Fatalf("got %v, want only the functional area", got)
	}
}

// 🔴 A FUNCTIONAL AREA WITH NO FENCE TABLE IS AN ERROR, NOT A SKIP. Skipping would ack
// "swept and fenced" for a schema that has no fence — the silence ADR-077 decision 6
// exists to make impossible.
func TestAnAreaMissingItsFenceTableFailsByName(t *testing.T) {
	plan := fencePlanOf(
		fenceArea("device-management"), fenceOf("device-management"),
		fenceArea("device-state"),
	)
	_, err := fenceSchemas(plan)
	if err == nil {
		t.Fatal("an area with no fence table was accepted")
	}
	if !strings.Contains(err.Error(), "device-state") {
		t.Fatalf("the refusal does not name the area at fault: %v", err)
	}
	if strings.Contains(err.Error(), "device-management") {
		t.Fatalf("the refusal names an area that is fine: %v", err)
	}
}

func TestAPlanNamingNoFunctionalAreaIsRefused(t *testing.T) {
	if _, err := fenceSchemas(fencePlanOf([2]string{"public", "some_table"})); err == nil {
		t.Fatal("a plan with no functional area was accepted")
	}
	if _, err := fenceSchemas(nil); err == nil {
		t.Fatal("a nil plan was accepted")
	}
}

// The fence table is exempt in every schema, by a derived rule rather than ten entries —
// so an area added later is covered without anyone remembering to add it.
func TestTheFenceTableIsExemptInEverySchema(t *testing.T) {
	for _, schema := range []string{"device-management", "event-processing", "an-area-added-later"} {
		e, ok := exemptionFor(Table{Schema: schema, Name: rdb.FenceTable})
		if !ok {
			t.Fatalf("the fence table is unclassified in %q, which fails every purge", schema)
		}
		if e.Class != ClassExempt {
			t.Fatalf("the fence table in %q is %v, want ClassExempt", schema, e.Class)
		}
		if e.Reason == "" {
			t.Fatalf("the fence exemption in %q states no reason", schema)
		}
	}
}

// 🔴 The exemption must be keyed on the fence table's exact name, not on a pattern that
// a real tenant-bearing table could match — the same narrowing migrationTableExemption
// documents for `*_migrations`.
func TestTheFenceExemptionDoesNotCoverLookalikeTables(t *testing.T) {
	for _, name := range []string{"purged_tenant", "purged_tenants_archive", "tenants"} {
		if _, ok := fenceTableExemption(Table{Schema: "device-management", Name: name}); ok {
			t.Fatalf("%q was exempted by the fence rule", name)
		}
	}
}

func timeZero() time.Time { return time.Time{} }

// 🔴 THE PRECONDITION IS THE GUARD AGAINST BRICKING A LIVE TENANT, and until this test
// existed nothing drove it: the drill passes nil and the unit tests never reached the
// transaction. A fence misdirected at a live tenant deletes nothing and refuses every
// write it makes from that moment, with no row missing to make anyone look.
func TestPlantingAndLiftingRunTheirPreconditionInsideTheTransaction(t *testing.T) {
	db := fenceDB(t)
	// "main" is SQLite's own name for its default database, which is what makes the
	// quoted `"schema"."table"` rendering the real path uses resolvable here — the same
	// device the sweep tests in this package use.
	plan := fencePlanOf(fenceArea("main"), fenceOf("main"))
	refused := errors.New("that token names a LIVE tenant")
	calls := 0
	pre := func(*gorm.DB) error { calls++; return refused }

	_, err := PlantFence(context.Background(), db, plan, "acme", time.Now(), time.Now(), pre)
	if !errors.Is(err, refused) {
		t.Fatalf("plant ignored its precondition: got %v", err)
	}
	var planted int64
	db.Table(rdb.FenceTable).Count(&planted)
	if planted != 0 {
		t.Fatalf("a refused plant still wrote %d fence row(s)", planted)
	}

	if err := LiftFence(context.Background(), db, plan, "acme", time.Now(), pre); !errors.Is(err, refused) {
		t.Fatalf("lift ignored its precondition: got %v", err)
	}
	if calls != 2 {
		t.Fatalf("the precondition ran %d time(s), want once per call", calls)
	}
}

// The positive control: with a precondition that passes, the same plant writes its row.
// Without it, a plant that could never write anything satisfies the assertion above.
func TestAPassingPreconditionLetsThePlantThrough(t *testing.T) {
	db := fenceDB(t)
	plan := fencePlanOf(fenceArea("main"), fenceOf("main"))
	ok := func(*gorm.DB) error { return nil }

	if _, err := PlantFence(context.Background(), db, plan, "acme", time.Now(), time.Now(), ok); err != nil {
		t.Fatalf("plant: %v", err)
	}
	var planted int64
	db.Table(rdb.FenceTable).Count(&planted)
	if planted != 1 {
		t.Fatalf("the plant wrote %d rows, so the refusal test above proves nothing", planted)
	}
}

// fenceDB is an in-memory database carrying the fence table under the unqualified name
// the tests address it by. It cannot model schema qualification — sqlite has no schemas —
// so the statements PlantFence builds are exercised for their SEMANTICS here and for
// their SQL against real Postgres by the migration harness's purge drill.
func fenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: false},
	})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	if err := db.AutoMigrate(&rdb.PurgedTenant{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return db
}

func TestPlantingRefusesAnEmptyToken(t *testing.T) {
	plan := fencePlanOf(fenceArea("main"), fenceOf("main"))
	if _, err := PlantFence(nil, nil, plan, "  ", timeZero(), timeZero(), nil); err == nil {
		t.Fatal("an empty token was accepted for planting")
	}
	if err := LiftFence(nil, nil, plan, "", timeZero(), nil); err == nil {
		t.Fatal("an empty token was accepted for lifting")
	}
}
