// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
)

// isFleetArea reports whether a is registered in fleetAreas (a non-tenant instance type).
func isFleetArea(a area) bool {
	for _, f := range fleetAreas {
		if f.name == a.name {
			return true
		}
	}
	return false
}

// assertEveryAreaSelected is the guard that keeps the fleet list from silently emptying and the
// schema gates from silently covering fewer areas than are registered.
//
// 🔴 A fleet registry that is empty, or a registry loop that visited fewer schemas than both lists
// hold, reports every area it DID visit as ok and exits 0, which reads exactly like a run that
// covered the fleet. So the count is asserted before anything runs, for the modes that visit it.
func assertEveryAreaSelected(mode string, filtered bool, selected []area) error {
	if mode == "coverage" {
		return nil
	}
	if len(fleetAreas) == 0 {
		return fmt.Errorf("fleetAreas is empty: the fleet schema would not be %sed, and the run would still "+
			"report every tenant area green. Restore the entry in registry.go", mode)
	}
	if filtered {
		return nil // an explicit -only subset is the caller's intent; assertGoldensCovered says so
	}
	want := len(areas) + len(fleetAreas)
	if len(selected) != want {
		return fmt.Errorf("this %s run selected %d area schema(s) but the registry holds %d tenant + %d fleet = %d; "+
			"a run that covers fewer is not a full run", mode, len(selected), len(areas), len(fleetAreas), want)
	}
	return nil
}

// coreOwnedTables are created in EVERY area schema by core/rdb's ExecuteInitialize (the audit
// journal and the erasure fence), ahead of the area's own chain. They carry a tenant column
// because they serve tenant areas; in a fleet schema they are core's, not fleet's, and are not
// what this assertion is about.
var coreOwnedTables = []string{"audit_events", "purged_tenants"}

// assertNoTenantColumns fails when any FLEET-OWNED table of the given schemas carries a tenant_id
// column (core-owned tables, see coreOwnedTables, are excluded).
// Fleet is a different instance type and none of its tables is tenant-scoped; a fleet table that
// later becomes tenant-scoped must fail here until someone decides where it belongs, because the
// tenant purge, the previous-release fixtures and the state-writer check iterate the tenant areas
// only and would never see it.
func assertNoTenantColumns(ctx context.Context, host string, port int, user, password, db string, schemas []string) error {
	conn, err := openDatabase(host, port, user, password, db)
	if err != nil {
		return err
	}
	defer closeDatabase(conn)

	var tables int
	if err := conn.WithContext(ctx).Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema IN ?`, schemas).Scan(&tables).Error; err != nil {
		return fmt.Errorf("counting fleet tables: %w", err)
	}
	if tables == 0 {
		// "no tenant column" is also what a connection to the wrong database says.
		return fmt.Errorf("found no tables in schema(s) %s of %s, so a clean tenant_id check would mean nothing",
			strings.Join(schemas, ", "), db)
	}
	var hits []string
	if err := conn.WithContext(ctx).Raw(`SELECT table_schema || '.' || table_name FROM information_schema.columns
		WHERE table_schema IN ? AND column_name IN ('tenant_id', 'tenant') AND table_name NOT IN ?`, schemas, coreOwnedTables).Scan(&hits).Error; err != nil {
		return fmt.Errorf("looking for tenant columns: %w", err)
	}
	if len(hits) > 0 {
		return fmt.Errorf("fleet table(s) carry a tenant column: %s\n"+
			"Fleet tables are not tenant-scoped, and the tenant-purge machinery iterates the tenant areas only. "+
			"If a fleet table must now be tenant-scoped, decide where it belongs; do not just move it.", strings.Join(hits, ", "))
	}
	return nil
}
