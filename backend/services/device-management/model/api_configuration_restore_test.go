// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// seedDeclaredDevice publishes profile profileToken with declared, a type adopting it, and
// a device of that type, in ctx's tenant.
func seedDeclaredDevice(t *testing.T, api *Api, ctx context.Context, profileToken, deviceToken string,
	declared []ConfigurationKey) {
	t.Helper()
	seedProfileWithRule(t, api, ctx, profileToken, profileToken+"-rule", false)
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, profileToken, declared); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, profileToken, nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	p := profileToken
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: profileToken + "-type", ProfileToken: &p}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: deviceToken, DeviceTypeToken: profileToken + "-type"}); err != nil {
		t.Fatalf("device: %v", err)
	}
}

func setShared(t *testing.T, api *Api, ctx context.Context, device, key string, vt AttributeValueType, value string) {
	t.Helper()
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: device, Scope: string(AttributeScopeShared),
		AttrKey: key, ValueType: string(vt), Value: &value,
	}); err != nil {
		t.Fatalf("set %s on %s: %v", key, device, err)
	}
}

func desiredOf(t *testing.T, api *Api, ctx context.Context, device string) *DeviceConfigurationRevision {
	t.Helper()
	c, err := api.DeviceConfigurationByToken(ctx, device)
	if err != nil {
		t.Fatalf("read %s: %v", device, err)
	}
	return c.Desired
}

func revisionsOf(t *testing.T, api *Api, ctx context.Context, device string) []DeviceConfigurationRevision {
	t.Helper()
	page, err := api.DeviceConfigurationRevisions(ctx, device, rdb.Pagination{PageNumber: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("revisions of %s: %v", device, err)
	}
	return page.Results
}

func refusalCode(err error) string {
	var refusal *ConfigurationRefusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

var restoreDeclaration = []ConfigurationKey{
	{Key: "interval", ValueType: "LONG"},
	{Key: "gain", ValueType: "DOUBLE"},
	{Key: "enabled", ValueType: "BOOLEAN"},
	{Key: "label", ValueType: "STRING"},
	{Key: "limits", ValueType: "JSON"},
}

// Restoring an old revision mints a NEW revision whose document is byte-for-byte the old
// one, leaves every earlier revision exactly as it was, delivers the new revision, and
// leaves the SHARED attributes saying what the document says — a key the old revision did
// not carry is removed — so the next read is not stale. Every declared value type
// round-trips, a string with escapes included.
func TestRestoringARevisionMintsANewOneWithItsContent(t *testing.T) {
	api, ctx := configTestApi(t)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)

	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	setShared(t, api, ctx, "dev", "gain", AttributeValueDouble, "1.5")
	setShared(t, api, ctx, "dev", "enabled", AttributeValueBoolean, "true")
	setShared(t, api, ctx, "dev", "label", AttributeValueString, "tank \"A\" \\ — ünïcode")
	setShared(t, api, ctx, "dev", "limits", AttributeValueJson, `{"lo": 1, "hi": [2, 3.5]}`)
	target := desiredOf(t, api, ctx, "dev")

	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "60")
	if _, err := api.DeleteEntityAttribute(ctx, string(entity.TypeDevice), "dev", string(AttributeScopeShared), "label"); err != nil {
		t.Fatal(err)
	}
	before := revisionsOf(t, api, ctx, "dev")
	sentBefore := len(pub.snapshot())

	cfg, err := api.RestoreDeviceConfiguration(ctx, "dev", target.Revision)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if cfg.Desired == nil || cfg.Desired.Revision != before[0].Revision+1 {
		t.Fatalf("restore did not mint the next revision: %+v", cfg.Desired)
	}
	if cfg.Desired.Document != target.Document || cfg.Desired.Digest != target.Digest {
		t.Fatalf("restored document %s, want %s", cfg.Desired.Document, target.Document)
	}
	if cfg.Stale {
		t.Fatal("after a restore the attributes still build a different document")
	}

	after := revisionsOf(t, api, ctx, "dev")
	if len(after) != len(before)+1 {
		t.Fatalf("%d revisions after, want %d", len(after), len(before)+1)
	}
	for i, old := range before {
		got := after[i+1]
		if got.Revision != old.Revision || got.Document != old.Document || got.Digest != old.Digest {
			t.Fatalf("revision %d was rewritten: %+v, was %+v", old.Revision, got, old)
		}
	}
	if sent := pub.snapshot(); len(sent) != sentBefore+1 || sent[len(sent)-1].revision != cfg.Desired.Revision {
		t.Fatalf("the restored revision was not delivered: %+v", sent)
	}

	// The next ordinary write builds on the restored attributes, not on what preceded them.
	setShared(t, api, ctx, "dev", "gain", AttributeValueDouble, "2")
	if got, want := desiredOf(t, api, ctx, "dev").Document,
		`{"enabled":true,"gain":2,"interval":30,"label":"tank \"A\" \\ — ünïcode","limits":{"hi":[2,3.5],"lo":1}}`; got != want {
		t.Fatalf("document after a later write = %s, want %s", got, want)
	}
}

// Restoring the content the device already holds mints nothing.
func TestRestoringTheCurrentContentMintsNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	current := desiredOf(t, api, ctx, "dev")

	cfg, err := api.RestoreDeviceConfiguration(ctx, "dev", current.Revision)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if cfg.Desired.Revision != current.Revision || len(revisionsOf(t, api, ctx, "dev")) != 1 {
		t.Fatalf("restoring the current content minted revision %d", cfg.Desired.Revision)
	}
}

// An unknown device or revision is not found.
func TestRestoringAnUnknownRevisionIsNotFound(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", restoreDeclaration)
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", 99); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("an unknown revision: %v, want not found", err)
	}
	if _, err := api.RestoreDeviceConfiguration(ctx, "ghost", 1); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("an unknown device: %v, want not found", err)
	}
}

// A restore of a revision whose keys the device's CURRENT declaration no longer carries is
// refused, whole, with the undeclared-key code; one whose value the current declaration
// types differently is refused with the value-type code. Neither writes anything.
func TestRestoringAgainstAChangedDeclarationIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "dev", []ConfigurationKey{
		{Key: "interval", ValueType: "LONG"}, {Key: "mode", ValueType: "STRING"},
	})
	setShared(t, api, ctx, "dev", "interval", AttributeValueLong, "30")
	setShared(t, api, ctx, "dev", "mode", AttributeValueString, "eco")
	old := desiredOf(t, api, ctx, "dev")

	for _, tc := range []struct {
		name     string
		declared []ConfigurationKey
		code     string
	}{
		{"a key no longer declared", []ConfigurationKey{{Key: "interval", ValueType: "LONG"}}, CodeConfigurationKeyUndeclared},
		{"a key now declared as another type", []ConfigurationKey{
			{Key: "interval", ValueType: "BOOLEAN"}, {Key: "mode", ValueType: "STRING"}}, CodeConfigurationValueType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", tc.declared); err != nil {
				t.Fatal(err)
			}
			if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
				t.Fatal(err)
			}
			n := len(revisionsOf(t, api, ctx, "dev"))
			_, err := api.RestoreDeviceConfiguration(ctx, "dev", old.Revision)
			if got := refusalCode(err); got != tc.code {
				t.Fatalf("refusal code %q (%v), want %q", got, err, tc.code)
			}
			if len(revisionsOf(t, api, ctx, "dev")) != n {
				t.Fatal("a refused restore minted a revision")
			}
		})
	}
}

// Copying makes the target's document the source's latest one, by writing the target's own
// attributes; the source is untouched.
func TestCopyingConfigurationFromAnotherDevice(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "src", restoreDeclaration)
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dst", DeviceTypeToken: "prof-type"}); err != nil {
		t.Fatal(err)
	}
	setShared(t, api, ctx, "src", "interval", AttributeValueLong, "45")
	setShared(t, api, ctx, "src", "label", AttributeValueString, "north")
	setShared(t, api, ctx, "dst", "gain", AttributeValueDouble, "9")
	source := desiredOf(t, api, ctx, "src")

	cfg, err := api.CopyDeviceConfiguration(ctx, "src", "dst")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cfg.Desired == nil || cfg.Desired.Document != source.Document {
		t.Fatalf("target document %+v, want %s", cfg.Desired, source.Document)
	}
	if again := desiredOf(t, api, ctx, "src"); again.Revision != source.Revision {
		t.Fatalf("copying from the source minted a revision on it: %d", again.Revision)
	}
}

// The target's profile must declare every key the source's document carries; a key it does
// not is refused with the undeclared-key code and nothing is written to the target.
func TestCopyingToADeviceThatDoesNotDeclareTheKeysIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "src", restoreDeclaration)
	seedDeclaredDevice(t, api, ctx, "narrow", "dst", []ConfigurationKey{{Key: "interval", ValueType: "LONG"}})
	setShared(t, api, ctx, "src", "interval", AttributeValueLong, "45")
	setShared(t, api, ctx, "src", "label", AttributeValueString, "north")
	setShared(t, api, ctx, "dst", "interval", AttributeValueLong, "5")
	before := desiredOf(t, api, ctx, "dst")

	_, err := api.CopyDeviceConfiguration(ctx, "src", "dst")
	if got := refusalCode(err); got != CodeConfigurationKeyUndeclared {
		t.Fatalf("refusal code %q (%v), want %q", got, err, CodeConfigurationKeyUndeclared)
	}
	if after := desiredOf(t, api, ctx, "dst"); after.Revision != before.Revision || after.Document != before.Document {
		t.Fatalf("a refused copy changed the target: %+v", after)
	}
}

// A source with no revision has nothing to copy: refused as not found, never copied as an
// empty document that would clear the target.
func TestCopyingFromADeviceWithNoRevisionIsNotFound(t *testing.T) {
	api, ctx := configTestApi(t)
	seedDeclaredDevice(t, api, ctx, "prof", "src", restoreDeclaration)
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dst", DeviceTypeToken: "prof-type"}); err != nil {
		t.Fatal(err)
	}
	setShared(t, api, ctx, "dst", "interval", AttributeValueLong, "5")
	if _, err := api.CopyDeviceConfiguration(ctx, "src", "dst"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("copy from a device with no revision: %v, want not found", err)
	}
	if got := desiredOf(t, api, ctx, "dst").Document; got != `{"interval":5}` {
		t.Fatalf("the target changed: %s", got)
	}
}

// Both devices resolve in the caller's tenant: another tenant's device, as source or as
// target, is not found, and nothing is read from or written to it.
func TestCopyingAcrossTenantsIsNotFound(t *testing.T) {
	api, acme := configTestApi(t)
	beta := core.WithTenant(context.Background(), "beta")
	seedDeclaredDevice(t, api, acme, "prof", "dev", restoreDeclaration)
	seedDeclaredDevice(t, api, beta, "prof", "foreign", restoreDeclaration)
	setShared(t, api, acme, "dev", "interval", AttributeValueLong, "1")
	setShared(t, api, beta, "foreign", "interval", AttributeValueLong, "2")
	foreignBefore := desiredOf(t, api, beta, "foreign")

	if _, err := api.CopyDeviceConfiguration(acme, "foreign", "dev"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("copy FROM another tenant's device: %v, want not found", err)
	}
	if _, err := api.CopyDeviceConfiguration(acme, "dev", "foreign"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("copy TO another tenant's device: %v, want not found", err)
	}
	if got := desiredOf(t, api, acme, "dev").Document; got != `{"interval":1}` {
		t.Fatalf("this tenant's device changed: %s", got)
	}
	if after := desiredOf(t, api, beta, "foreign"); after.Revision != foreignBefore.Revision {
		t.Fatalf("the other tenant's device changed: %+v", after)
	}
}
