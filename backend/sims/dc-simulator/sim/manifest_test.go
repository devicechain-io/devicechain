// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "testing"

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
		if first[i] != second[i] {
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
}
