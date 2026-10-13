// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

func boolp(b bool) *bool { return &b }

// newDisabledDeviceFixture is a credential fixture plus the command tables the enqueue gate
// reads, so one database answers every door a disabled device is refused at.
func newDisabledDeviceFixture(t *testing.T) credFixture {
	t.Helper()
	tables := append(append([]any{}, deviceProfileTables...), &DeviceCredential{})
	api := newPartialUpdateApi(t, tables...)
	return seedCredentialFixture(t, api, countQueries(t, api))
}

func disabledOf(t *testing.T, f credFixture, token string) *Device {
	t.Helper()
	rows, err := f.api.DevicesByToken(f.ctx, []string{token})
	return requireOne(t, "device "+token, rows, err)
}

// A device is created enabled unless the request says otherwise, on the single create and
// on the list create alike. An omitted flag must read as enabled: a create that quietly
// quarantined its device would look like a working device that never reports.
func TestADeviceIsCreatedEnabledUnlessToldOtherwise(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	for _, tc := range []struct {
		token        string
		enabled      *bool
		wantDisabled bool
	}{
		{"omitted", nil, false},
		{"explicit-true", boolp(true), false},
		{"explicit-false", boolp(false), true},
	} {
		if _, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: tc.token, DeviceTypeToken: "dt",
			Enabled: tc.enabled}); err != nil {
			t.Fatalf("create %s: %v", tc.token, err)
		}
		if got := disabledOf(t, f, tc.token).IsDisabled(); got != tc.wantDisabled {
			t.Errorf("CreateDevice %s: disabled = %v, want %v", tc.token, got, tc.wantDisabled)
		}
	}

	if _, err := f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{
		{Token: "list-on", DeviceTypeToken: "dt"},
		{Token: "list-off", DeviceTypeToken: "dt", Enabled: boolp(false)},
	}); err != nil {
		t.Fatalf("create list: %v", err)
	}
	if disabledOf(t, f, "list-on").IsDisabled() || !disabledOf(t, f, "list-off").IsDisabled() {
		t.Errorf("CreateDevices did not honour each request's enabled flag")
	}
}

// Disabling is a partial update: omitted leaves the device alone, false disables it, true
// enables it, and null is refused. Disabling an already-disabled device keeps the instant
// it was FIRST disabled — "disabled since" must not move because someone pressed the
// button twice.
func TestDisablingADeviceIsAPartialUpdate(t *testing.T) {
	f := newDisabledDeviceFixture(t)

	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if disabledOf(t, f, "dev").IsDisabled() {
		t.Fatalf("an update that did not mention enabled disabled the device")
	}

	updated, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !updated.IsDisabled() {
		t.Fatalf("the update's own answer reads the device as enabled")
	}
	first := disabledOf(t, f, "dev").DisabledAt
	if !first.Valid {
		t.Fatalf("disable did not store when the device was disabled")
	}

	time.Sleep(5 * time.Millisecond)
	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable again: %v", err)
	}
	if again := disabledOf(t, f, "dev").DisabledAt; !again.Time.Equal(first.Time) {
		t.Errorf("disabling a disabled device moved its instant from %v to %v", first.Time, again.Time)
	}

	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Description: dcgraphql.OptionalStringOf("still off")}); err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !disabledOf(t, f, "dev").IsDisabled() {
		t.Fatalf("an update that did not mention enabled re-enabled the device")
	}

	_, err = f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.ClearedBool()})
	if err == nil {
		t.Fatalf("an explicit null for enabled was accepted")
	}
	if !disabledOf(t, f, "dev").IsDisabled() {
		t.Fatalf("a refused null still changed the device")
	}

	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if disabledOf(t, f, "dev").IsDisabled() {
		t.Fatalf("enable left the device disabled")
	}
}

// Every lookup that hands a device to a door that refuses a disabled one must carry the
// flag. The credential lookups read through column allowlists, and a column missing from
// one reads as "enabled" (the stored form is a nullable timestamp), so this is the test
// that a narrow read cannot quietly let a quarantined device through.
func TestADisabledDeviceIsRefusedThroughEveryLookup(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	now := time.Now()
	secret := "s3cret-0123456789"

	lookups := map[string]func() (*Device, error){
		"DevicesByToken": func() (*Device, error) {
			rows, err := f.api.DevicesByToken(f.ctx, []string{"dev"})
			if err != nil || len(rows) != 1 {
				return nil, fmt.Errorf("rows %d: %v", len(rows), err)
			}
			return rows[0], nil
		},
		"AuthenticateDevice (event, access token)": func() (*Device, error) {
			return f.api.AuthenticateDevice(f.ctx, accessToken("tok-1"), now)
		},
		"CachedApi.AuthenticateDevice (event, access token)": func() (*Device, error) {
			return f.capi.AuthenticateDevice(f.ctx, accessToken("tok-1"), now)
		},
		"AuthenticateDeviceConnect (connect, access token)": func() (*Device, error) {
			return f.api.AuthenticateDeviceConnect(f.ctx, accessToken("tok-1"), now)
		},
		"ResolveDeviceCredential (connect, password)": func() (*Device, error) {
			d, _, err := f.api.ResolveDeviceCredential(f.ctx, &PresentedCredential{
				CredentialType: string(CredentialMqttBasic), CredentialId: "cred-1", Secret: &secret}, now)
			return d, err
		},
	}
	for name, lookup := range lookups {
		d, err := lookup()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !d.IsDisabled() {
			t.Errorf("%s returned the disabled device as enabled", name)
		}
	}
}

// Commands to a disabled device are refused at the enqueue gate, single and batch, with a
// code of their own. The literal is written out because the value crosses a service
// boundary: command-delivery relays it and REACT decides from it not to retry.
func TestEnqueueToADisabledDeviceIsRefusedWithItsOwnCode(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "on", DeviceTypeToken: "dt"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	v, err := f.api.ValidateCommandEnqueue(f.ctx, "dev", "reboot", nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if v.Allowed || string(v.Code) != "DEVICE_DISABLED" {
		t.Fatalf("allowed=%v code=%q, want a rejection coded DEVICE_DISABLED", v.Allowed, v.Code)
	}

	// The counterweight: the enabled device beside it is still allowed.
	if v, err := f.api.ValidateCommandEnqueue(f.ctx, "on", "reboot", nil); err != nil || !v.Allowed {
		t.Fatalf("an enabled device was refused: %+v, %v", v, err)
	}

	refusals, err := f.api.ValidateCommandEnqueueBatch(f.ctx, []string{"on", "dev"}, "reboot", nil)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(refusals) != 1 || refusals[0].DeviceToken != "dev" || string(refusals[0].Code) != "DEVICE_DISABLED" {
		t.Fatalf("batch refusals = %+v, want exactly dev refused with DEVICE_DISABLED", refusals)
	}
}

// Disabling a device evicts what the resolve path holds of it — its credentials and its
// by-token entry — on the write itself, so the next event on any replica reads the new
// state rather than a copy that still says enabled.
func TestDisablingADeviceEvictsWhatTheResolverHolds(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	ev := &captureEvictor{}
	f.api.CacheEvictor = ev
	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if len(ev.credentialEvicts) != 1 || len(ev.credentialEvicts[0].deviceIds) != 1 || ev.credentialEvicts[0].deviceIds[0] != f.devId {
		t.Errorf("credential evictions = %+v, want the disabled device's", ev.credentialEvicts)
	}
	if len(ev.deviceTokenEvicts) != 1 || ev.deviceTokenEvicts[0] != "dev" {
		t.Errorf("device-by-token evictions = %v, want [dev]", ev.deviceTokenEvicts)
	}
}

// The change is journaled: an update of the device row, by the caller, keyed to the device.
func TestDisablingADeviceIsJournaled(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	var rows []rdb.AuditEvent
	if err := f.api.RDB.DB(f.ctx).Where("table_name = ? AND operation = ? AND entity_pk = ?",
		"devices", "update", fmt.Sprint(f.devId)).Find(&rows).Error; err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("journal holds %d update rows for the device, want 1", len(rows))
	}
}

// An update that does not name `enabled` must not write disabled_at at all. UpdateDevice
// loads the device, edits it and saves it; a disable that commits in between would
// otherwise be overwritten with the enabled state the rename loaded, silently re-enabling
// a device an operator had just quarantined. The disable is injected between the load and
// the save, inside the save's own statement path, which is exactly that interleaving.
func TestARenameRacingADisableDoesNotReEnableTheDevice(t *testing.T) {
	f := newDisabledDeviceFixture(t)
	db := f.api.RDB.Database
	armed := true
	if err := db.Callback().Update().Before("gorm:update").Register("test:racing-disable", func(d *gorm.DB) {
		row, ok := d.Statement.Dest.(*Device)
		if !armed || !ok || row.Token != "dev" {
			return
		}
		armed = false
		_ = d.Session(&gorm.Session{NewDB: true}).Model(&Device{}).Where("id = ?", row.ID).
			UpdateColumn("disabled_at", time.Now().UTC()).Error
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:racing-disable") })

	if _, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if armed {
		t.Fatal("the racing disable never ran; the test proves nothing")
	}
	if !disabledOf(t, f, "dev").IsDisabled() {
		t.Fatal("a rename that did not mention enabled re-enabled a device disabled while it ran")
	}
}
