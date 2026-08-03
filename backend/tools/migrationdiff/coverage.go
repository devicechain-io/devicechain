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
		names = append(names, e.Table.String())
	}
	return fmt.Errorf(`%d table(s) cannot be accounted for by the tenant purge:

  %s

Every table in an instance database must be one of:
  - tenant-bearing   — carries a "tenant_id" or "tenant" column of a character type
  - transitive       — has a foreign key into a tenant-bearing table
  - exempt/deferred  — listed in backend/core/tenantpurge/exempt.go with a reason

If the table above holds tenant data, give it a tenant column. If it does not, add an
exemption naming why. If it holds tenant data you are NOT yet erasing, add it as
ClassDeferred so it stays visible in every purge result instead of being filed away
among the tables that hold nothing`, len(unclassified), strings.Join(names, "\n  "))
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
	type counts struct{ direct, transitive, exempt, deferred, unclassified int }
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
		default:
			c.unclassified++
		}
	}
	sort.Strings(schemas)

	fmt.Printf("%-24s %8s %11s %7s %9s %13s\n",
		"area", "direct", "transitive", "exempt", "deferred", "unclassified")
	total := counts{}
	for _, s := range schemas {
		c := perSchema[s]
		fmt.Printf("%-24s %8d %11d %7d %9d %13d\n",
			s, c.direct, c.transitive, c.exempt, c.deferred, c.unclassified)
		total.direct += c.direct
		total.transitive += c.transitive
		total.exempt += c.exempt
		total.deferred += c.deferred
		total.unclassified += c.unclassified
	}
	fmt.Printf("%-24s %8d %11d %7d %9d %13d\n",
		"TOTAL", total.direct, total.transitive, total.exempt, total.deferred, total.unclassified)

	// Deferred tables are printed in full rather than counted. They are the tables that
	// hold tenant data the purge does not erase, and a number in a column is exactly how
	// that stops being read.
	if deferred := plan.OfClass(tenantpurge.ClassDeferred); len(deferred) > 0 {
		fmt.Printf("\n%d table(s) hold tenant data the purge does NOT erase:\n", len(deferred))
		for _, e := range deferred {
			fmt.Printf("  %s\n      %s\n", e.Table, wrap(e.Reason, 78, "      "))
		}
	}
}

// wrap re-flows a reason to a width so a long one stays readable in CI output.
func wrap(s string, width int, indent string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+indent)
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
