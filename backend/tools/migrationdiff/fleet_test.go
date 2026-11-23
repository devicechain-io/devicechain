// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
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

// prevReleaseTag is the tag the previous-release compatibility fixtures were captured from. It
// duplicates the integration-tagged prevRelease constant on purpose: this guard has to run in the
// plain unit build, and TestPrevReleaseTagAgreesWithTheIntegrationConstant keeps the two in step.
const prevReleaseTag = "v1.9.0"

// TestPrevReleaseFixturesCoverFleetOnceAReleaseShipsIt is the guard the fleet spec (section 4.2)
// promises: from the release after the one that first ships the fleet migration, the previous-release
// fixtures must cover fleet. When the previous-release tag contains the fleet schema package and the
// fixture directory has no fleet.sql, this fails and names the fix.
func TestPrevReleaseFixturesCoverFleetOnceAReleaseShipsIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available; the release-fixture guard cannot run here")
	}
	if err := exec.Command("git", "rev-parse", "--verify", "--quiet", prevReleaseTag+"^{commit}").Run(); err != nil {
		t.Skipf("tag %s is not present in this checkout (shallow clone without tags); the release-fixture guard cannot run here", prevReleaseTag)
	}
	err := exec.Command("git", "cat-file", "-e", prevReleaseTag+":backend/fleet/schema/migrations.go").Run()
	shipped := err == nil
	_, statErr := os.Stat(filepath.Join("testdata", "prev-release", prevReleaseTag, "fleet.sql"))
	hasFixture := statErr == nil
	if shipped && !hasFixture {
		t.Fatalf("release %s contains the fleet migration but testdata/prev-release/%s has no fleet.sql: capture the fleet fixture (and extend the previous-release tests to cover fleetAreas)", prevReleaseTag, prevReleaseTag)
	}
	if !shipped && hasFixture {
		t.Fatalf("testdata/prev-release/%s/fleet.sql exists but release %s does not contain the fleet migration: the fixture was captured from the wrong tag", prevReleaseTag, prevReleaseTag)
	}
}

func TestPrevReleaseTagAgreesWithTheIntegrationConstant(t *testing.T) {
	src, err := os.ReadFile("prev_release_columns_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `const prevRelease = "`+prevReleaseTag+`"`) {
		t.Fatalf("prev_release_columns_test.go no longer declares prevRelease = %q; update prevReleaseTag in fleet_test.go with it", prevReleaseTag)
	}
}
