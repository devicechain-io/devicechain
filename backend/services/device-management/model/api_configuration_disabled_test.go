// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 A DISABLED DEVICE IS CUT OFF FROM CONFIGURATION TOO. A session it already had open when it
// was disabled stays up, so the broker cannot be what stops it: every sync and every
// config.status is refused here, per message, and refusing writes nothing and publishes
// nothing. Enabling restores both with no residue.
func TestADisabledDeviceCanNeitherSyncNorReportConfiguration(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	clock := fakeSyncClock(t)

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	got, err := api.SyncDeviceConfiguration(ctx, "dev", 0)
	if got != nil || refusalReason(err) != ReportRejectedDeviceDisabled {
		t.Fatalf("sync from a disabled device = (%v, %v), want a %q refusal", got, err, ReportRejectedDeviceDisabled)
	}
	if len(pub.snapshot()) != 0 {
		t.Fatalf("a disabled device was sent %+v", pub.snapshot())
	}
	if st := reportedState(t, api, ctx); st != nil {
		t.Fatalf("a refused sync recorded state %+v", st)
	}
	err = api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil)
	if refusalReason(err) != ReportRejectedDeviceDisabled {
		t.Fatalf("config.status from a disabled device = %v, want a %q refusal", err, ReportRejectedDeviceDisabled)
	}
	if st := reportedState(t, api, ctx); st != nil {
		t.Fatalf("a refused report recorded state %+v", st)
	}

	if _, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	clock.advance(SyncCooldown + 1)
	if got, err := api.SyncDeviceConfiguration(ctx, "dev", 0); err != nil || got == nil || got.Revision != rev.Revision {
		t.Fatalf("sync after enabling = (%v, %v), want revision %d", got, err, rev.Revision)
	}
	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil); err != nil {
		t.Fatalf("config.status after enabling: %v", err)
	}
}
