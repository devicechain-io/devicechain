// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
)

// Device.enabled and Device.disabledAt read the stored disabled-since instant: a NULL is an
// enabled device with no disabledAt, a value is a disabled one with the instant as RFC 3339.
// The console's Disabled badge and Enable/Disable action decide from these two fields, so a
// resolver that answered `enabled: true` for a disabled device would hide the quarantine.
func TestDeviceEnabledAndDisabledAtReadTheStoredInstant(t *testing.T) {
	enabled := &DeviceResolver{M: model.Device{}}
	if !enabled.Enabled() || enabled.DisabledAt() != nil {
		t.Fatalf("a device with no disabled-since instant reads enabled=%v disabledAt=%v", enabled.Enabled(), enabled.DisabledAt())
	}

	at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	disabled := &DeviceResolver{M: model.Device{DisabledAt: sql.NullTime{Time: at, Valid: true}}}
	if disabled.Enabled() {
		t.Fatal("a disabled device reads as enabled")
	}
	got := disabled.DisabledAt()
	if got == nil {
		t.Fatal("a disabled device has no disabledAt")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, *got); err != nil || !parsed.Equal(at) {
		t.Fatalf("disabledAt = %q (%v), want %s", *got, err, at.Format(time.RFC3339))
	}
}
