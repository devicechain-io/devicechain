// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fleetAreas is a separate list from areas, and the tenant machinery keeps iterating areas only.
func TestFleetAreasAreSeparateFromTenantAreas(t *testing.T) {
	if len(fleetAreas) == 0 {
		t.Fatal("fleetAreas is empty")
	}
	for _, f := range fleetAreas {
		for _, a := range areas {
			if a.name == f.name {
				t.Errorf("%q is in both areas and fleetAreas", f.name)
			}
		}
		if f.dbSuffix == "" {
			t.Errorf("fleet area %q shares the tenant database: the purge classification would report its tables as unclassified", f.name)
		}
		if len(f.migrations) == 0 {
			t.Errorf("fleet area %q has an empty chain", f.name)
		}
	}
	for _, a := range areas {
		if a.dbSuffix != "" {
			t.Errorf("tenant area %q has a database suffix", a.name)
		}
	}
	if got, want := len(allAreas()), len(areas)+len(fleetAreas); got != want {
		t.Fatalf("allAreas() returned %d, want %d", got, want)
	}
}

func TestSelectAreasVisitsFleetExceptForCoverage(t *testing.T) {
	has := func(sel []area, name string) bool {
		for _, a := range sel {
			if a.name == name {
				return true
			}
		}
		return false
	}
	for _, mode := range []string{"snapshot", "verify", "replay"} {
		sel, err := selectAreas("", mode)
		if err != nil {
			t.Fatal(err)
		}
		if !has(sel, "fleet") || len(sel) != len(areas)+len(fleetAreas) {
			t.Errorf("%s did not visit every area including fleet: %d", mode, len(sel))
		}
	}
	sel, err := selectAreas("", "coverage")
	if err != nil {
		t.Fatal(err)
	}
	if has(sel, "fleet") || len(sel) != len(areas) {
		t.Errorf("coverage must iterate the tenant areas only, got %d", len(sel))
	}
	if _, err := selectAreas("nonesuch", "verify"); err == nil {
		t.Error("an unknown area was accepted")
	}
	if sel, err := selectAreas("fleet", "verify"); err != nil || len(sel) != 1 {
		t.Errorf("-only fleet: %v %d", err, len(sel))
	}
}

func TestAssertEveryAreaSelectedRefusesAShortRun(t *testing.T) {
	all := allAreas()
	if err := assertEveryAreaSelected("verify", false, all); err != nil {
		t.Fatalf("a full run was refused: %v", err)
	}
	if err := assertEveryAreaSelected("verify", false, all[:len(all)-1]); err == nil {
		t.Fatal("a run that covered one schema fewer than the registry holds was accepted")
	}
	// coverage never visits fleet, so the count is not asserted there.
	if err := assertEveryAreaSelected("coverage", false, areas); err != nil {
		t.Fatalf("coverage was refused: %v", err)
	}
	// The empty-list guard: with fleetAreas emptied, the run must refuse.
	saved := fleetAreas
	fleetAreas = nil
	defer func() { fleetAreas = saved }()
	if err := assertEveryAreaSelected("verify", true, areas); err == nil {
		t.Fatal("an empty fleetAreas was accepted")
	}
}

// The golden is the committed record of the fleet schema, and no FLEET-OWNED table in it may
// hold a tenant column. core/rdb creates audit_events and purged_tenants in every area schema, and
// those are tenant-shaped by design: they are core's, not fleet's.
func TestFleetGoldenHoldsNoTenantColumn(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("golden", "fleet.sql"))
	if err != nil {
		t.Fatalf("the fleet golden is missing: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "CREATE TABLE fleet.cloud_identities") {
		t.Fatal("the fleet golden does not describe the fleet schema")
	}
	var owned int
	for _, stmt := range splitNormalized(strings.TrimRight(text, "\n")) {
		if !strings.HasPrefix(stmt, "CREATE TABLE fleet.") {
			continue
		}
		name := strings.Fields(stmt)[2]
		core := false
		for _, c := range coreOwnedTables {
			if name == "fleet."+c {
				core = true
			}
		}
		if core {
			continue
		}
		owned++
		if strings.Contains(stmt, "tenant_id") {
			t.Errorf("%s carries tenant_id: a fleet table has become tenant-scoped", name)
		}
	}
	if owned < 9 {
		t.Fatalf("found %d fleet-owned tables in the golden, want at least 9", owned)
	}
}
