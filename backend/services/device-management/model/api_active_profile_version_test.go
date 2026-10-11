// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"reflect"
	"testing"
)

// ActiveDeviceProfileVersion is what the console shows as "the declaration this device
// sees", so it must name the SAME version row the configuration delivery path builds the
// device's document from — across publishes and a rollback, and with another profile's
// versions in the table. A reader that returned the latest or the first version, or that
// dropped the profile predicate, would agree with delivery at one step and not another.
func TestActiveDeviceProfileVersionIsTheOneDeliveryReads(t *testing.T) {
	api, ctx := configTestApi(t)

	// Another profile published FIRST, so its version 1 row precedes ours by id.
	seedProfileWithRule(t, api, ctx, "other", "ro", false)
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "other",
		[]ConfigurationKey{{Key: "foreign", ValueType: "STRING"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "other", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}

	v1 := []ConfigurationKey{{Key: "mode", ValueType: "STRING"}}
	v2 := []ConfigurationKey{{Key: "rate", ValueType: "LONG"}}
	dev := seedConfiguredDevice(t, api, ctx, v1)
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", v2); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}

	check := func(step string, wantVersion int32, want []ConfigurationKey) {
		t.Helper()
		profiles, err := api.DeviceProfilesByToken(ctx, []string{"prof"})
		if err != nil || len(profiles) != 1 {
			t.Fatalf("%s: load profile: %v", step, err)
		}
		active, err := api.ActiveDeviceProfileVersion(ctx, profiles[0])
		if err != nil || active == nil {
			t.Fatalf("%s: active version: %v, %v", step, active, err)
		}
		snap, err := ParseProfileSnapshot(active.Snapshot)
		if err != nil {
			t.Fatalf("%s: parse: %v", step, err)
		}
		delivered, deliveredId, err := activeConfigurationDeclaration(api.RDB.DB(ctx), dev.ID)
		if err != nil || deliveredId == nil {
			t.Fatalf("%s: delivery declaration: %v", step, err)
		}
		if active.Version != wantVersion || active.ID != *deliveredId {
			t.Fatalf("%s: active version %d (row %d), delivery reads row %d; want version %d",
				step, active.Version, active.ID, *deliveredId, wantVersion)
		}
		if !reflect.DeepEqual(snap.Configuration, want) || !reflect.DeepEqual(delivered, want) {
			t.Fatalf("%s: shown %v, delivered %v, want %v", step, snap.Configuration, delivered, want)
		}
	}
	check("after second publish", 2, v2)

	if _, err := api.RollbackDeviceProfile(ctx, "prof", 1); err != nil {
		t.Fatal(err)
	}
	check("after rollback", 1, v1)
}

// An active pointer naming a missing version is an invariant breach. Delivery refuses to
// build a document from it, so this reader must fail too rather than read as "never
// published" — a null would tell the console the device sees nothing.
func TestActiveDeviceProfileVersionDanglingPointerFails(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, nil)
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"prof"})
	if err != nil || len(profiles) != 1 {
		t.Fatalf("load profile: %v", err)
	}
	p := profiles[0]
	p.ActiveVersion.Int32 = 99
	if v, err := api.ActiveDeviceProfileVersion(ctx, p); err == nil {
		t.Fatalf("dangling active version read as %v, want an error", v)
	}
}
