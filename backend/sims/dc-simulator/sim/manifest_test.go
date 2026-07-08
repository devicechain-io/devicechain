// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"reflect"
	"testing"
)

func testManifest() SimManifest {
	return SimManifest{
		Name: "test",
		Seed: 42,
		Profiles: []ProfileSpec{
			{Token: "test-profile", Name: "Test Profile", Category: "test",
				Metrics: []MetricSpec{{Key: "speed_kph", Name: "Speed", DataType: "DOUBLE", Unit: "kph"}}},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: "test-vehicle", Name: "Test Vehicle", ProfileToken: "test-profile"},
		},
		Populations: []PopulationSpec{
			{OfType: "test-vehicle", Count: 3, TokenPattern: "car-{n:05d}", ExternalIdPattern: "VIN-{n:05d}"},
		},
	}
}

// TestExpandDeterministic verifies the ADR-050 hard requirement: the same
// (manifest, seed) always renders identical tokens/externalIds/credentials —
// this is what makes bootstrap idempotent and reset safe to re-run.
func TestExpandDeterministic(t *testing.T) {
	m := testManifest()

	first := m.Expand(m.Seed)
	second := m.Expand(m.Seed)

	if len(first) != 3 {
		t.Fatalf("expected 3 expanded devices, got %d", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("expansion size differs across runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		// DeviceInstance now carries a []Assignment field, so it is no longer
		// comparable with == — reflect.DeepEqual is the direct equivalent.
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Fatalf("device %d differs across runs with the same seed: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// TestExpandTokenRendering checks the token/externalId pattern rendering
// itself (zero-padded index substitution).
func TestExpandTokenRendering(t *testing.T) {
	m := testManifest()
	devices := m.Expand(m.Seed)

	want := []struct {
		token      string
		externalId string
	}{
		{"car-00001", "VIN-00001"},
		{"car-00002", "VIN-00002"},
		{"car-00003", "VIN-00003"},
	}
	for i, w := range want {
		if devices[i].Token != w.token {
			t.Errorf("device %d token = %q, want %q", i, devices[i].Token, w.token)
		}
		if devices[i].ExternalId != w.externalId {
			t.Errorf("device %d externalId = %q, want %q", i, devices[i].ExternalId, w.externalId)
		}
		if devices[i].DeviceTypeToken != "test-vehicle" {
			t.Errorf("device %d deviceTypeToken = %q, want %q", i, devices[i].DeviceTypeToken, "test-vehicle")
		}
	}
}

// TestExpandDifferentSeedsDivergeCredentials checks that credential material
// (which has no pattern of its own) still varies with the seed, even though
// the pattern-derived token/externalId do not.
func TestExpandDifferentSeedsDivergeCredentials(t *testing.T) {
	m := testManifest()
	m.Seed = 1
	a := m.Expand(1)
	b := m.Expand(2)

	if a[0].Token != b[0].Token {
		t.Fatalf("token should be seed-independent (pure pattern formatting): %q vs %q", a[0].Token, b[0].Token)
	}
	if a[0].CredentialId == b[0].CredentialId {
		t.Fatalf("credential id should differ across seeds, got the same value %q", a[0].CredentialId)
	}
}

// TestManifestValidate exercises the manifest-shape checks Provision relies on
// to fail fast before any network call.
func TestManifestValidate(t *testing.T) {
	valid := testManifest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid manifest to pass, got: %v", err)
	}

	badRef := testManifest()
	badRef.Populations[0].OfType = "unknown-type"
	if err := badRef.Validate(); err == nil {
		t.Fatal("expected validation error for population referencing unknown device type")
	}

	badToken := testManifest()
	badToken.Profiles[0].Token = "bad token with spaces"
	if err := badToken.Validate(); err == nil {
		t.Fatal("expected validation error for grammar-unsafe profile token")
	}
}

// TestDevicepulseManifestShape checks the slice-1 built-in scenario matches
// the spec: exactly one population with Count 1, one profile with one numeric
// metric, one device type.
func TestDevicepulseManifestShape(t *testing.T) {
	m := NewDevicepulse(1).Manifest()

	if len(m.Populations) != 1 || m.Populations[0].Count != 1 {
		t.Fatalf("expected exactly one population with Count 1, got %+v", m.Populations)
	}
	if len(m.Profiles) != 1 || len(m.Profiles[0].Metrics) != 1 {
		t.Fatalf("expected exactly one profile with one metric, got %+v", m.Profiles)
	}
	if len(m.DeviceTypes) != 1 {
		t.Fatalf("expected exactly one device type, got %+v", m.DeviceTypes)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("devicepulse manifest failed validation: %v", err)
	}

	devices := m.Expand(m.Seed)
	if len(devices) != 1 {
		t.Fatalf("expected exactly one expanded device, got %d", len(devices))
	}
	if len(devices[0].Assignments) != 0 {
		t.Fatalf("devicepulse declares no areas/customers, expected no assignments, got %+v", devices[0].Assignments)
	}
}

// manifestWithAssignments extends testManifest() with an area hierarchy (2
// areas) and a customer, and turns on distributeAcross:"area" on the one
// population — the shape TestExpand*Assignments* and TestValidate* below
// exercise.
func manifestWithAssignments() SimManifest {
	m := testManifest()
	m.AreaTypes = []AreaTypeSpec{{Token: "test-area-type", Name: "Test Area Type"}}
	m.Areas = []AreaSpec{
		{Token: "area-01", Name: "Area 1", AreaTypeToken: "test-area-type"},
		{Token: "area-02", Name: "Area 2", AreaTypeToken: "test-area-type"},
	}
	m.CustomerTypes = []CustomerTypeSpec{{Token: "test-customer-type", Name: "Test Customer Type"}}
	m.Customers = []CustomerSpec{{Token: "cust-01", Name: "Customer 1", CustomerTypeToken: "test-customer-type"}}
	m.Populations[0].DistributeAcross = []string{"area"}
	return m
}

// TestExpandAssignmentsRoundRobinAreaAndFixedCustomer checks Expand's
// assignment rendering: distributeAcross:"area" round-robins devices across
// SimManifest.Areas by (n-1) mod len(areas), and every device additionally
// gets a fixed assignment to the manifest's one customer — both with the
// "assign-<deviceToken>-<targetToken>" relationship token the spec mandates.
func TestExpandAssignmentsRoundRobinAreaAndFixedCustomer(t *testing.T) {
	m := manifestWithAssignments()
	devices := m.Expand(m.Seed)

	wantArea := []string{"area-01", "area-02", "area-01"} // (n-1) mod 2 for n=1,2,3
	if len(devices) != len(wantArea) {
		t.Fatalf("expected %d devices, got %d", len(wantArea), len(devices))
	}

	for i, d := range devices {
		if len(d.Assignments) != 2 {
			t.Fatalf("device %d: expected 2 assignments (area+customer), got %d: %+v", i, len(d.Assignments), d.Assignments)
		}
		var gotArea, gotCustomer *Assignment
		for j := range d.Assignments {
			switch d.Assignments[j].TargetType {
			case "area":
				gotArea = &d.Assignments[j]
			case "customer":
				gotCustomer = &d.Assignments[j]
			}
		}
		if gotArea == nil {
			t.Fatalf("device %d: no area assignment rendered", i)
		}
		if gotArea.TargetToken != wantArea[i] {
			t.Errorf("device %d area assignment target = %q, want %q", i, gotArea.TargetToken, wantArea[i])
		}
		wantAreaRelToken := "assign-" + d.Token + "-" + wantArea[i]
		if gotArea.RelationshipToken != wantAreaRelToken {
			t.Errorf("device %d area relationship token = %q, want %q", i, gotArea.RelationshipToken, wantAreaRelToken)
		}

		if gotCustomer == nil {
			t.Fatalf("device %d: no customer assignment rendered", i)
		}
		if gotCustomer.TargetToken != "cust-01" {
			t.Errorf("device %d customer assignment target = %q, want %q", i, gotCustomer.TargetToken, "cust-01")
		}
		wantCustRelToken := "assign-" + d.Token + "-cust-01"
		if gotCustomer.RelationshipToken != wantCustRelToken {
			t.Errorf("device %d customer relationship token = %q, want %q", i, gotCustomer.RelationshipToken, wantCustRelToken)
		}
	}
}

// TestExpandAssignmentsDeterministic re-checks the ADR-050 determinism
// requirement specifically for the newly-rendered Assignment set (not just
// token/externalId/credential, which TestExpandDeterministic already covers).
func TestExpandAssignmentsDeterministic(t *testing.T) {
	m := manifestWithAssignments()
	first := m.Expand(m.Seed)
	second := m.Expand(m.Seed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("assignments differ across runs with the same seed:\n%+v\nvs\n%+v", first, second)
	}
}

// TestValidateRejectsDistributeAcrossAreaWithNoAreas exercises the specific
// failure the spec calls out: distributeAcross:["area"] declared on a
// population whose manifest has no areas at all must fail fast, not silently
// render zero assignments.
func TestValidateRejectsDistributeAcrossAreaWithNoAreas(t *testing.T) {
	m := testManifest()
	m.Populations[0].DistributeAcross = []string{"area"}
	// Deliberately no AreaTypes/Areas declared.
	if err := m.Validate(); err == nil {
		t.Fatal(`expected validation error for distributeAcross:["area"] with no areas declared`)
	}
}

// TestValidateRejectsUnsupportedDistributeAcross checks the fail-closed
// handling of any distributeAcross value other than "area" (customer/asset
// spreading is a documented, deliberately unbuilt extension — Validate should
// reject it outright rather than silently ignoring it).
func TestValidateRejectsUnsupportedDistributeAcross(t *testing.T) {
	m := manifestWithAssignments()
	m.Populations[0].DistributeAcross = []string{"customer"}
	if err := m.Validate(); err == nil {
		t.Fatal(`expected validation error for unsupported distributeAcross value "customer"`)
	}
}

// TestValidateRejectsAssignmentToMissingTarget exercises validateAssignments
// directly (see its doc comment for why: Expand never produces a dangling
// reference on its own, so this is the only way to prove the check itself —
// not just its unreachable-in-practice call site — actually rejects one).
func TestValidateRejectsAssignmentToMissingTarget(t *testing.T) {
	d := DeviceInstance{
		Token: "device-01",
		Assignments: []Assignment{
			{TargetType: "area", TargetToken: "no-such-area", RelationshipToken: "assign-device-01-no-such-area"},
		},
	}
	err := validateAssignments(d, map[string]bool{}, map[string]bool{}, map[string]bool{})
	if err == nil {
		t.Fatal("expected validation error for an assignment referencing a missing area")
	}

	// Sanity check the positive case with the same helper, so the negative
	// case above is proven against real "it does accept a valid one" behavior
	// rather than a helper that just always errors.
	ok := validateAssignments(d, map[string]bool{"no-such-area": true}, map[string]bool{}, map[string]bool{})
	if ok != nil {
		t.Fatalf("expected no error once the target exists, got: %v", ok)
	}
}

// TestValidateRejectsUnsupportedAssignmentTargetType checks the entity-type
// registry gate itself (a "device" target — valid in the real registry, but
// out of scope for this slice's assignment targets).
func TestValidateRejectsUnsupportedAssignmentTargetType(t *testing.T) {
	d := DeviceInstance{
		Token: "device-01",
		Assignments: []Assignment{
			{TargetType: "device", TargetToken: "some-other-device", RelationshipToken: "assign-device-01-some-other-device"},
		},
	}
	if err := validateAssignments(d, map[string]bool{}, map[string]bool{}, map[string]bool{}); err == nil {
		t.Fatal(`expected validation error for unsupported assignment target type "device"`)
	}
}

// TestBuildingpulseManifestShape checks the slice-2 built-in scenario matches
// the spec table: 12 thermostats (3 buildings x 4/building), 3 areas, 3
// assets, one customer, one profile with 4 metrics + 1 alarm def, one
// dashboard — and that it passes Validate end to end.
func TestBuildingpulseManifestShape(t *testing.T) {
	m := NewBuildingpulse(1).Manifest()

	if len(m.Populations) != 1 || m.Populations[0].Count != 12 {
		t.Fatalf("expected exactly one population with Count 12, got %+v", m.Populations)
	}
	if len(m.Areas) != 3 {
		t.Fatalf("expected 3 areas, got %d: %+v", len(m.Areas), m.Areas)
	}
	if len(m.Assets) != 3 {
		t.Fatalf("expected 3 assets, got %d: %+v", len(m.Assets), m.Assets)
	}
	if len(m.Customers) != 1 {
		t.Fatalf("expected 1 customer, got %d: %+v", len(m.Customers), m.Customers)
	}
	if len(m.Profiles) != 1 || len(m.Profiles[0].Metrics) != 4 {
		t.Fatalf("expected exactly one profile with 4 metrics, got %+v", m.Profiles)
	}
	if len(m.Profiles[0].Alarms) != 1 {
		t.Fatalf("expected exactly one alarm def, got %+v", m.Profiles[0].Alarms)
	}
	if len(m.Dashboards) != 1 {
		t.Fatalf("expected exactly one dashboard, got %+v", m.Dashboards)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("buildingpulse manifest failed validation: %v", err)
	}

	devices := m.Expand(m.Seed)
	if len(devices) != 12 {
		t.Fatalf("expected 12 expanded devices, got %d", len(devices))
	}
	for i, d := range devices {
		if len(d.Assignments) != 2 {
			t.Errorf("device %d (%s): expected 2 assignments (area+customer), got %d: %+v",
				i, d.Token, len(d.Assignments), d.Assignments)
		}
	}
}

// TestRegistry checks the manifest-id -> Sim constructor lookup main.go relies
// on to pick a driver from the handshake's ManifestId.
func TestRegistry(t *testing.T) {
	for _, id := range []string{"devicepulse", "buildingpulse"} {
		ctor, ok := Registry[id]
		if !ok {
			t.Fatalf("expected manifest id %q to be registered", id)
		}
		s := ctor(1)
		if s == nil {
			t.Fatalf("constructor for %q returned a nil Sim", id)
		}
		if got := s.Manifest().Name; got != id {
			t.Errorf("manifest id %q constructor built a Sim named %q", id, got)
		}
	}
	if _, ok := Registry["no-such-manifest"]; ok {
		t.Fatal("expected an unknown manifest id to be absent from the registry")
	}
}
