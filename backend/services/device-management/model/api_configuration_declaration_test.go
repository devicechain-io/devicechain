// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func configCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

func activeSnapshot(t *testing.T, api *Api, ctx context.Context, profileId uint) *ProfileSnapshot {
	t.Helper()
	snap, err := api.activeProfileSnapshot(ctx, profileId)
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	return snap
}

// Publishing freezes the draft declaration into the version's snapshot.
func TestPublishFreezesConfigurationDeclaration(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)

	want := []ConfigurationKey{
		{Key: "sampleIntervalSeconds", ValueType: "LONG", Description: "how often"},
		{Key: "mode", ValueType: "STRING"},
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", want); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got := activeSnapshot(t, api, ctx, p.ID).Configuration
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("published snapshot configuration = %+v, want %+v", got, want)
	}
}

// Nothing declared publishes an empty declaration, not an error and not a default.
func TestPublishWithoutDeclarationDeclaresNothing(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := activeSnapshot(t, api, ctx, p.ID).Configuration; len(got) != 0 {
		t.Fatalf("expected nothing declared, got %+v", got)
	}
}

// A version is frozen: editing or clearing the draft afterwards changes only the next
// publish, never the declaration a device already resolves.
func TestDeclarationEditAfterPublishDoesNotTouchPublishedVersion(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	first := []ConfigurationKey{{Key: "mode", ValueType: "STRING"}}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", first); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof",
		[]ConfigurationKey{{Key: "other", ValueType: "LONG"}}); err != nil {
		t.Fatal(err)
	}
	if got := activeSnapshot(t, api, ctx, p.ID).Configuration; len(got) != 1 || got[0] != first[0] {
		t.Fatalf("published declaration moved after a draft edit: %+v", got)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", nil); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := api.deviceProfileByToken(ctx, "prof")
	if keys, _ := reloaded.ConfigurationKeys(); len(keys) != 0 {
		t.Fatalf("clearing the draft left %+v", keys)
	}
	if got := activeSnapshot(t, api, ctx, p.ID).Configuration; len(got) != 1 {
		t.Fatalf("published declaration moved after the draft was cleared: %+v", got)
	}
}

// A snapshot stored before the declaration existed has no "configuration" key. It must
// decode to nothing declared. A fixture, not a round trip: this is the bytes an earlier
// release wrote.
func TestSnapshotWithoutConfigurationKeyDeclaresNothing(t *testing.T) {
	old := datatypes.JSON(`{"metrics":[],"commands":[],"rules":[],"location":{"expectedAccuracyMeters":5}}`)
	snap, err := parseProfileSnapshot(old)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Configuration != nil {
		t.Fatalf("pre-existing snapshot decoded to %+v, want nil", snap.Configuration)
	}
}

func TestConfigurationDeclarationIsRefusedWhenInvalid(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)

	tooMany := make([]ConfigurationKey, MaxConfigurationKeys+1)
	for i := range tooMany {
		tooMany[i] = ConfigurationKey{Key: fmt.Sprintf("k%d", i), ValueType: "LONG"}
	}
	cases := map[string][]ConfigurationKey{
		"empty key":        {{Key: "", ValueType: "LONG"}},
		"bad grammar":      {{Key: "has space", ValueType: "LONG"}},
		"leading dash":     {{Key: "-x", ValueType: "LONG"}},
		"path-ish":         {{Key: "a/b", ValueType: "LONG"}},
		"duplicate":        {{Key: "a", ValueType: "LONG"}, {Key: "a", ValueType: "STRING"}},
		"bad value type":   {{Key: "a", ValueType: "FLOAT"}},
		"empty value type": {{Key: "a"}},
		"long description": {{Key: "a", ValueType: "LONG", Description: strings.Repeat("x", MaxConfigurationDescriptionLen+1)}},
		"too many keys":    tooMany,
	}
	for name, keys := range cases {
		if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", keys); err == nil {
			t.Errorf("%s: declaration was accepted", name)
		}
	}
	// The cap itself is inclusive.
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", tooMany[:MaxConfigurationKeys]); err != nil {
		t.Errorf("exactly %d keys must be accepted: %v", MaxConfigurationKeys, err)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "missing",
		[]ConfigurationKey{{Key: "a", ValueType: "LONG"}}); err == nil {
		t.Error("unknown profile was accepted")
	}
}

// Publish re-validates the stored draft: a declaration that bypassed the setter (a bad
// row, an older release) must not be frozen into a version.
func TestPublishRefusesAnInvalidStoredDeclaration(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	bad := datatypes.JSON(`[{"key":"has space","valueType":"LONG"}]`)
	if err := api.RDB.DB(ctx).Model(p).Where("id = ?", p.ID).
		Update("configuration_declaration", bad).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err == nil {
		t.Fatal("publish froze an invalid declaration")
	}
}

// UpdateDeviceProfile reads the profile, then saves it whole. A declaration set between
// that read and the save must survive: the column is written only by its own setter.
func TestUpdateDeviceProfileDoesNotRevertAConcurrentDeclaration(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := configCtx()
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)

	want := `[{"key":"mode","valueType":"STRING"}]`
	fired := false
	cb := api.RDB.DB(ctx).Callback().Update().Before("gorm:update")
	if err := cb.Register("test:set_declaration_mid_update", func(tx *gorm.DB) {
		if fired {
			return
		}
		fired = true
		// The racing writer: lands after UpdateDeviceProfile's read, before its save.
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec(
			"UPDATE device_profiles SET configuration_declaration = ? WHERE token = ?", want, "prof").Error; err != nil {
			t.Errorf("racing write: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the racing write never ran, so nothing was proved")
	}
	reloaded, err := api.deviceProfileByToken(ctx, "prof")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := reloaded.ConfigurationKeys()
	if len(keys) != 1 || keys[0].Key != "mode" {
		t.Fatalf("UpdateDeviceProfile reverted the declaration set in between: %+v", keys)
	}
}
