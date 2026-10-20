// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// captureRoster records the device-roster events emitted so a test can assert the
// create / re-type / type-reprofile paths roster the right devices (ADR-051 slice 4c-2).
type captureRoster struct {
	events []*DeviceRosterEvent
}

func (c *captureRoster) PublishDeviceRoster(_ context.Context, e *DeviceRosterEvent) {
	c.events = append(c.events, e)
}

func newRosterEmitTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{}, &DeviceProfileActivation{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func strp(s string) *string { return &s }

// seedType creates a device type optionally adopting a profile (which is created first
// when profileToken is non-empty), returning nothing — callers address by token.
func seedType(t *testing.T, api *Api, ctx context.Context, typeToken, profileToken string) {
	t.Helper()
	if profileToken != "" {
		if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: profileToken}); err != nil {
			t.Fatalf("seed profile %q: %v", profileToken, err)
		}
	}
	req := &DeviceTypeCreateRequest{Token: typeToken}
	if profileToken != "" {
		req.ProfileToken = strp(profileToken)
	}
	if _, err := api.CreateDeviceType(ctx, req); err != nil {
		t.Fatalf("seed type %q: %v", typeToken, err)
	}
}

// A created device is rostered under its type's stable profile token, with the
// dead-man clock based at the device's creation time.
func TestCreateDevice_EmitsRosterWithCreatedAt(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "sensor", "sensor-profile")
	dev, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	assert.NoError(t, err)

	if assert.Len(t, cap.events, 1, "a create emits exactly one roster fact") {
		ev := cap.events[0]
		assert.Equal(t, "d1", ev.DeviceToken)
		assert.Equal(t, "sensor-profile", ev.ProfileToken)
		assert.True(t, ev.ExpectedSince.Equal(dev.CreatedAt), "expected-since is the device creation time: got %s want %s", ev.ExpectedSince, dev.CreatedAt)
	}
}

// A device whose type adopts no profile still rosters, with an empty profile token — an
// inert entry a later re-type re-homes.
func TestCreateDevice_NoProfileEmitsEmptyProfileToken(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "bare", "")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "bare"})
	assert.NoError(t, err)

	if assert.Len(t, cap.events, 1) {
		assert.Equal(t, "", cap.events[0].ProfileToken)
	}
}

// A re-type re-rosters under the NEW profile and bases the dead-man clock at the
// membership-began instant (now), NOT the original creation time — so an old device moved
// into a long-standing rule gets a fresh grace window instead of firing instantly.
func TestUpdateDevice_RetypeEmitsNewProfileAndMembershipTime(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "t-a", "profile-a")
	seedType(t, api, ctx, "t-b", "profile-b")
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "t-a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Back-date the row so "membership began now" is provably distinct from "created".
	past := time.Now().Add(-24 * time.Hour)
	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", "d1").Update("created_at", past).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	cap.events = nil

	if _, err := api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("t-b")}); err != nil {
		t.Fatalf("retype: %v", err)
	}
	if assert.Len(t, cap.events, 1, "a re-type emits exactly one roster fact") {
		ev := cap.events[0]
		assert.Equal(t, "profile-b", ev.ProfileToken, "re-rostered under the new type's profile")
		assert.False(t, ev.Enabled, "a re-type is not an enable transition")
		assert.True(t, ev.ExpectedSince.After(past.Add(time.Hour)), "expected-since is membership-began (now), not the back-dated creation time: got %s", ev.ExpectedSince)
	}
}

// A metadata-only update (same type) changes no binding and emits nothing.
func TestUpdateDevice_MetadataOnlyDoesNotEmit(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "sensor", "sensor-profile")
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	cap.events = nil

	_, err := api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("sensor"), Name: dcgraphql.OptionalStringOf("renamed")})
	assert.NoError(t, err)
	assert.Empty(t, cap.events, "a metadata-only update must not re-roster")
}

// With no publisher wired, create still succeeds — emission (and the profile-token read
// it would trigger) is a nil-safe no-op.
func TestCreateDevice_NoPublisherIsNoOp(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedType(t, api, ctx, "sensor", "sensor-profile")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"})
	assert.NoError(t, err)
}

// Re-pointing a type's profile fans a roster fact out to every device of that type, under
// the new profile — the migration path that adopts absence detection onto existing devices.
func TestUpdateDeviceType_ReprofileFansOutRoster(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "sensor", "profile-a")
	// A second profile the type will be re-pointed at.
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-b"}); err != nil {
		t.Fatalf("seed profile-b: %v", err)
	}
	for _, tok := range []string{"d1", "d2"} {
		if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tok, DeviceTypeToken: "sensor"}); err != nil {
			t.Fatalf("create %s: %v", tok, err)
		}
	}
	cap.events = nil

	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("profile-b")}); err != nil {
		t.Fatalf("reprofile: %v", err)
	}
	assert.Len(t, cap.events, 2, "the re-profile fans out to both devices")
	got := map[string]string{}
	for _, ev := range cap.events {
		got[ev.DeviceToken] = ev.ProfileToken
	}
	assert.Equal(t, map[string]string{"d1": "profile-b", "d2": "profile-b"}, got)
}

// An UpdateDeviceType that does NOT change the adopted profile emits no roster fan-out.
func TestUpdateDeviceType_NoProfileChangeNoFanout(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "sensor", "profile-a")
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	cap.events = nil

	_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("profile-a"), Name: dcgraphql.OptionalStringOf("renamed")})
	assert.NoError(t, err)
	assert.Empty(t, cap.events, "an unchanged profile must not fan out")
}

// A rollback re-emits the rolled-back-to version's detection rules, so event-processing's
// "most recent rule fact names the active version" invariant survives a rollback.
func TestRollbackDeviceProfile_EmitsRolledBackRules(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	rules := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = rules

	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	rules.events = nil

	if _, err := api.RollbackDeviceProfile(ctx, "prof", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if assert.Len(t, rules.events, 1, "a rollback re-emits exactly one rule fact") {
		ev := rules.events[0]
		assert.Equal(t, "prof@1", ev.ProfileVersionToken, "the fact names the rolled-back-to version")
		assert.False(t, ev.PublishedAt.IsZero(), "re-activation carries a grace-period timestamp")
		if assert.Len(t, ev.Rules, 1) {
			assert.Equal(t, "hot", ev.Rules[0].Token)
		}
	}
}

// A disabled device's events are refused at resolution, so to the detection engine it is
// a device that has gone silent. It must not raise an absence alarm for that: disabling
// re-rosters it with NO profile, which disarms every absence rule for it, and the roster
// page the engine reconciles from says the same. Enabling re-rosters it under its profile
// with the membership beginning at the enable, so it gets a fresh grace window rather than
// an absence fired at once for the time it spent disabled.
//
// Each fact's ExpectedSince must be STRICTLY after the previous one's: event-processing's
// roster projection applies a fact only when it is strictly newer than the row it holds, so
// a disable that kept the old instant would be ignored there and the device would stay
// armed.
func TestUpdateDevice_DisableTakesTheDeviceOutOfAbsenceAndEnableGivesAFreshGrace(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap

	seedType(t, api, ctx, "sensor", "sensor-profile")
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	past := time.Now().Add(-24 * time.Hour)
	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", "d1").Update("created_at", past).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	cap.events = nil

	rosterOf := func() DeviceRosterEntry {
		t.Helper()
		page, err := api.DeviceRosterPage(ctx, 0, 10)
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("roster page: %v (%d entries)", err, len(page.Entries))
		}
		return page.Entries[0]
	}

	if _, err := api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !assert.Len(t, cap.events, 1, "a disable emits exactly one roster fact") {
		return
	}
	disabled := cap.events[0]
	assert.Equal(t, "", disabled.ProfileToken, "a disabled device is rostered with no profile, so no absence rule arms for it")
	assert.True(t, disabled.ExpectedSince.After(past.Add(time.Hour)),
		"the disable fact must be newer than the creation fact or the projection ignores it: got %s", disabled.ExpectedSince)
	assert.True(t, disabled.Disabled, "the disable fact says so explicitly, not only through an empty profile")
	assert.False(t, disabled.Enabled, "a disable is not an enable transition")
	assert.Equal(t, "", rosterOf().ProfileToken, "the reconcile page agrees with the fact")
	assert.True(t, rosterOf().Disabled, "the reconcile page carries the flag too")

	cap.events = nil
	if _, err := api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable again: %v", err)
	}
	assert.Empty(t, cap.events, "disabling a disabled device changes no membership and emits nothing")

	if _, err := api.UpdateDevice(ctx, "d1", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if assert.Len(t, cap.events, 1, "an enable emits exactly one roster fact") {
		enabled := cap.events[0]
		assert.Equal(t, "sensor-profile", enabled.ProfileToken, "an enabled device is rostered under its profile again")
		assert.False(t, enabled.Disabled, "the enable fact clears the flag")
		assert.True(t, enabled.Enabled, "the enable toggle is marked as one")
		assert.True(t, enabled.ExpectedSince.After(disabled.ExpectedSince),
			"the enable fact must be strictly newer than the disable fact: got %s, disable was %s",
			enabled.ExpectedSince, disabled.ExpectedSince)
		entry := rosterOf()
		assert.Equal(t, "sensor-profile", entry.ProfileToken)
		assert.True(t, entry.ExpectedSince.Equal(enabled.ExpectedSince), "the reconcile page agrees with the fact")
	}
}

// A device created disabled is rostered with no profile from the start.
func TestCreateDevice_DisabledIsRosteredWithNoProfile(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap
	off := false

	seedType(t, api, ctx, "sensor", "sensor-profile")
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor", Enabled: &off}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if assert.Len(t, cap.events, 1) {
		assert.Equal(t, "", cap.events[0].ProfileToken)
	}
}
