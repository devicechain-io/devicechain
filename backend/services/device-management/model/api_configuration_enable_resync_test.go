// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 A DEVICE THAT SYNCED WHILE DISABLED WAS REFUSED IN SILENCE AND CANNOT TELL THAT FROM
// "NOTHING NEW". Enabling it therefore sends its latest revision itself, exactly once.
func TestEnablingADeviceRedeliversItsLatestRevisionOnce(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := pub.snapshot(); len(got) != 0 {
		t.Fatalf("disabling published %+v", got)
	}
	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got := pub.snapshot()
	if len(got) != 1 || got[0].token != "dev" || got[0].revision != rev.Revision || got[0].digest != rev.Digest {
		t.Fatalf("enabling published %+v, want exactly revision %d to dev", got, rev.Revision)
	}

	// Enabling an enabled device, and an update that names no `enabled`, resend nothing.
	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := pub.snapshot(); len(got) != 1 {
		t.Fatalf("a non-toggling update published again: %+v", got)
	}
}

// A device with nothing minted has nothing to resend on enable.
func TestEnablingADeviceWithNoRevisionPublishesNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	for _, enabled := range []bool{false, true} {
		if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(enabled)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := pub.snapshot(); len(got) != 0 {
		t.Fatalf("published %+v for a device with no revision", got)
	}
}

// A revision minted while the device is disabled is held: it is stored, not published, and the
// enable delivers the LATEST state (one message, not one per revision minted meanwhile).
func TestRevisionMintedWhileDisabledIsHeldUntilEnable(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatal(err)
	}
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "turbo")
	if got := pub.snapshot(); len(got) != 0 {
		t.Fatalf("revisions minted while disabled were published: %+v", got)
	}
	latest := readConfig(t, api, ctx).Desired
	if latest == nil || latest.Revision != 2 {
		t.Fatalf("the revisions must still be minted while disabled, got %+v", latest)
	}

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatal(err)
	}
	got := pub.snapshot()
	if len(got) != 1 || got[0].revision != 2 || got[0].document != `{"mode":"turbo"}` {
		t.Fatalf("enable published %+v, want exactly revision 2", got)
	}
}
