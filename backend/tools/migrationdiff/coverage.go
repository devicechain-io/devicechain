// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

// runCoverage asserts that every table the migrations create can be accounted for by
// the tenant purge (ADR-077) — that each one either carries a tenant column, reaches
// tenant-bearing rows through a foreign key, or is named in the purge's exemption
// registry with a stated reason.
//
// # Why this check lives here
//
// The purge classifies the POSTGRES CATALOG, so the only honest test of its coverage is
// a real database with every area's migrations applied — which is precisely what this
// tool already builds, on both supported Postgres majors, on every PR. A unit test
// against a synthetic catalog can prove the classification RULES work; only this can
// prove they cover the schema the platform actually ships.
//
// # What it catches
//
// A migration adding a table that holds tenant data without a tenant column. That table
// would be swept by nothing, and no test anywhere else would notice: the area's own
// tests pass, the golden diff shows the new table as an expected change, and the purge
// would report success while retaining the data forever. Here it fails the build, in
// the same run that first sees the new table.
func runCoverage(ctx context.Context, host string, port int, user, password, db string, selected []area, filtered bool) error {
	// A filtered run classifies a database missing whole areas, and would report
	// "0 unclassified" for the ones that were never migrated. That reads exactly like
	// a clean result, so the filter is refused rather than trusted.
	if filtered {
		return fmt.Errorf("coverage must run over every area: a filtered run classifies a database " +
			"that is missing schemas, and reports the areas it never migrated as clean")
	}

	for _, a := range selected {
		if err := migrateChain(ctx, a, host, port, user, password, db); err != nil {
			return fmt.Errorf("running %s migrations: %w", a.name, err)
		}
	}

	conn, err := openDatabase(host, port, user, password, db)
	if err != nil {
		return err
	}
	defer closeDatabase(conn)

	plan, err := tenantpurge.Classify(ctx, conn)
	if err != nil {
		return fmt.Errorf("classifying %s: %w", db, err)
	}

	// 🔴 THE NEGATIVE CONTROL, and it is not optional. "No unclassified tables" is also
	// what an empty plan says — a connection to the wrong database, a catalog query that
	// silently returned nothing, a migration loop that ran zero areas. Each of those
	// produces a green coverage run that has checked nothing at all. So the areas are
	// asserted present with tables of their own BEFORE the absence of findings is
	// allowed to mean anything.
	if err := assertPlanIsPopulated(plan, selected); err != nil {
		return err
	}

	report(plan)
	return unclassifiedError(plan)
}

// unclassifiedError turns the plan's unclassified tables into the message a maintainer
// sees when their migration trips this gate, or nil when there are none.
//
// Split out from runCoverage so the message can be asserted on without a database. The
// message is the whole value of the gate: whoever hits it has just added a table and
// has no reason to know this package exists, so it has to name the table AND say what
// the three ways out are.
func unclassifiedError(plan *tenantpurge.Plan) error {
	unclassified := plan.OfClass(tenantpurge.ClassUnclassified)
	if len(unclassified) == 0 {
		return nil
	}
	var names []string
	for _, e := range unclassified {
		// Describe rather than the bare name: a continuous aggregate's materialization
		// reaches here as "_timescaledb_internal._materialized_hypertable_7", which
		// tells whoever tripped the gate nothing about what they changed.
		names = append(names, e.Describe())
	}
	return fmt.Errorf(`%d table(s) cannot be accounted for by the tenant purge:

  %s

Every table in an instance database must be one of:
  - tenant-bearing  — carries a "tenant_id" or "tenant" column of a character type
  - transitive      — has a foreign key into a tenant-bearing table
  - registered      — listed in backend/core/tenantpurge/exempt.go with a reason, as one
                      of ClassExempt, ClassDeferred or ClassExternal

If the table above holds tenant data, give it a tenant column. If it does not, add a
ClassExempt entry naming why. If it holds tenant data that some OTHER mechanism erases —
because no SQL predicate can reach inside it — add it as ClassExternal and name the purge
store that does; the store's own test asserts the name is registered, so the claim is
checked rather than taken. Only if it holds tenant data that NOTHING erases is it
ClassDeferred, which keeps it visible in every purge result and stops any purge on this
instance completing until it is closed`, len(unclassified), strings.Join(names, "\n  "))
}

// assertPlanIsPopulated fails unless every migrated area contributed tables to the plan.
func assertPlanIsPopulated(plan *tenantpurge.Plan, selected []area) error {
	perSchema := map[string]int{}
	for _, e := range plan.Entries {
		perSchema[e.Table.Schema]++
	}
	var missing []string
	for _, a := range selected {
		if perSchema[a.name] == 0 {
			missing = append(missing, a.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the classification found no tables at all in %d of %d migrated areas (%s), "+
			"so a clean result would mean nothing — check the tool is connected to the database the "+
			"migrations ran against", len(missing), len(selected), strings.Join(missing, ", "))
	}
	return nil
}

// report prints the classification, area by area, so a reviewer can see what the purge
// believes about each schema rather than only whether it passed.
func report(plan *tenantpurge.Plan) {
	type counts struct{ direct, transitive, exempt, deferred, external, unclassified int }
	perSchema := map[string]*counts{}
	schemas := []string{}
	for _, e := range plan.Entries {
		c, ok := perSchema[e.Table.Schema]
		if !ok {
			c = &counts{}
			perSchema[e.Table.Schema] = c
			schemas = append(schemas, e.Table.Schema)
		}
		switch e.Class {
		case tenantpurge.ClassDirect:
			c.direct++
		case tenantpurge.ClassTransitive:
			c.transitive++
		case tenantpurge.ClassExempt:
			c.exempt++
		case tenantpurge.ClassDeferred:
			c.deferred++
		case tenantpurge.ClassExternal:
			c.external++
		default:
			c.unclassified++
		}
	}
	sort.Strings(schemas)

	fmt.Printf("%-24s %8s %11s %7s %9s %9s %13s\n",
		"area", "direct", "transitive", "exempt", "deferred", "external", "unclassified")
	total := counts{}
	for _, s := range schemas {
		c := perSchema[s]
		fmt.Printf("%-24s %8d %11d %7d %9d %9d %13d\n",
			s, c.direct, c.transitive, c.exempt, c.deferred, c.external, c.unclassified)
		total.direct += c.direct
		total.transitive += c.transitive
		total.exempt += c.exempt
		total.deferred += c.deferred
		total.external += c.external
		total.unclassified += c.unclassified
	}
	fmt.Printf("%-24s %8d %11d %7d %9d %9d %13d\n",
		"TOTAL", total.direct, total.transitive, total.exempt, total.deferred, total.external, total.unclassified)

	// Deferred tables are printed in full rather than counted. They are the tables that
	// hold tenant data the purge does not erase, and a number in a column is exactly how
	// that stops being read.
	if deferred := plan.OfClass(tenantpurge.ClassDeferred); len(deferred) > 0 {
		fmt.Printf("\n%d table(s) hold tenant data the purge does NOT erase:\n", len(deferred))
		for _, e := range deferred {
			fmt.Printf("  %s\n      %s\n", e.Table, e.Reason)
		}
	}

	// External tables are printed in full for the same reason, and they are NOT good
	// news to be skimmed past: each one holds the tenant's data and is erased by a
	// store this report cannot see. Printing the store name is what makes the next
	// question askable — is that store registered, and what did it report? — rather
	// than leaving "handled elsewhere" to be read as "handled".
	if external := plan.OfClass(tenantpurge.ClassExternal); len(external) > 0 {
		fmt.Printf("\n%d table(s) hold tenant data erased by a purge store rather than by this sweep:\n",
			len(external))
		for _, e := range external {
			fmt.Printf("  %s  (store: %s)\n      %s\n", e.Table, e.Store, e.Reason)
		}
	}
}

// openDatabase opens a plain connection to the migrated instance database.
//
// No search_path is pinned and no gorm table prefix is set, deliberately: this
// connection reads the catalog across EVERY area's schema, which is the one view no
// service ever has. Every identifier the purge emits is schema-qualified and quoted, so
// it needs no search_path to resolve.
func openDatabase(host string, port int, user, password, db string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, db)
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", db, err)
	}
	return conn, nil
}

func closeDatabase(conn *gorm.DB) {
	if sqldb, err := conn.DB(); err == nil {
		_ = sqldb.Close()
	}
}
