// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tests cover the device profile's position declaration (ADR-078). The shape
// under test is the one thing most likely to be built wrong: it is a SINGULAR,
// NULLABLE struct, not a fourth definition list, and nil is a value with meaning
// ("this kind of device does not report position") rather than an absence. So the
// assertions are as much about nil surviving as about values surviving.

func newLocationTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DeviceProfile{}, &DeviceProfileVersion{}, &MetricDefinition{},
		&CommandDefinition{}, &DetectionRule{}, &DeviceType{}, &DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func floatOf(v float64) *float64 { return &v }
func int32Of(v int32) *int32     { return &v }

// snapshotOfVersion reads a published version's frozen snapshot back through the
// production parse path, so what a test inspects is what a device would resolve.
func snapshotOfVersion(t *testing.T, api *Api, ctx context.Context, profileToken string, version int32) *ProfileSnapshot {
	t.Helper()
	profile, err := api.deviceProfileByToken(ctx, profileToken)
	require.NoError(t, err)
	var row DeviceProfileVersion
	require.NoError(t, api.RDB.DB(ctx).
		Where("device_profile_id = ? AND version = ?", profile.ID, version).First(&row).Error)
	snap, err := parseProfileSnapshot(row.Snapshot)
	require.NoError(t, err)
	return snap
}

// A declaration set on the draft must survive a publish and come back out of the
// frozen snapshot with both of its stated expectations intact. The two values are
// deliberately distinct and non-round: identical or zero-ish values would let a
// capture that crossed accuracy with the update interval pass unnoticed.
func TestLocationDeclarationRoundTripsThroughPublish(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
		Token: "tracker",
		Location: &LocationDeclaration{
			ExpectedAccuracyMeters:        floatOf(2.5),
			ExpectedUpdateIntervalSeconds: int32Of(30),
		},
	})
	require.NoError(t, err)

	_, err = api.PublishDeviceProfile(ctx, "tracker", nil, nil, "tester")
	require.NoError(t, err)

	snap := snapshotOfVersion(t, api, ctx, "tracker", 1)
	require.NotNil(t, snap.Location, "the declaration was dropped between the draft and the frozen version")
	require.NotNil(t, snap.Location.ExpectedAccuracyMeters, "expected accuracy was dropped")
	assert.Equal(t, 2.5, *snap.Location.ExpectedAccuracyMeters)
	require.NotNil(t, snap.Location.ExpectedUpdateIntervalSeconds, "expected update interval was dropped")
	assert.Equal(t, int32(30), *snap.Location.ExpectedUpdateIntervalSeconds)

	// And the same declaration is what a DEVICE resolves — through its type's profile's
	// ACTIVE published version, which is the only path ingest ever reads.
	deviceType := &DeviceType{}
	deviceType.Token = "tracker-type"
	profile, err := api.deviceProfileByToken(ctx, "tracker")
	require.NoError(t, err)
	deviceType.ProfileId = &profile.ID
	require.NoError(t, api.RDB.DB(ctx).Create(deviceType).Error)

	resolved, err := api.LocationDeclarationByDeviceType(ctx, deviceType.ID)
	require.NoError(t, err)
	require.NotNil(t, resolved, "a device whose profile declares a location must resolve that declaration")
	require.NotNil(t, resolved.ExpectedAccuracyMeters)
	assert.Equal(t, 2.5, *resolved.ExpectedAccuracyMeters)
}

// 🔴 The central property. A profile that declares nothing must snapshot a NIL
// location, and that nil must stay distinguishable from a profile that declares a
// location while stating no expectations. If these two ever collapse into one value
// the distinction is gone for good: a published snapshot is immutable, so no later
// backfill can recover which of the two an already-frozen version meant.
func TestUndeclaredLocationIsNilAndDistinctFromDeclaredEmpty(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "silent"})
	require.NoError(t, err)
	_, err = api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
		Token:    "declared-empty",
		Location: &LocationDeclaration{},
	})
	require.NoError(t, err)

	_, err = api.PublishDeviceProfile(ctx, "silent", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "declared-empty", nil, nil, "tester")
	require.NoError(t, err)

	undeclared := snapshotOfVersion(t, api, ctx, "silent", 1)
	assert.Nil(t, undeclared.Location,
		"a profile that declares no location must snapshot nil — a non-nil zero value would say 'reports position', which is a different claim")

	declaredEmpty := snapshotOfVersion(t, api, ctx, "declared-empty", 1)
	require.NotNil(t, declaredEmpty.Location,
		"a declared-but-unconfigured location must survive as a present, empty declaration — collapsing it to nil erases the declaration itself")
	assert.Nil(t, declaredEmpty.Location.ExpectedAccuracyMeters)
	assert.Nil(t, declaredEmpty.Location.ExpectedUpdateIntervalSeconds)

	// Stated as the comparison a consumer actually makes: presence, not field values,
	// is what decides whether this kind of device reports a position.
	assert.NotEqual(t, undeclared.Location == nil, declaredEmpty.Location == nil,
		"undeclared and declared-but-empty must not answer the same question the same way")
}

// Clearing works, and what makes it distinguishable from never having declared is the
// append-only version history rather than a tombstone in the draft: the version
// published while the declaration existed still carries it, so an operator can see the
// profile stopped declaring position. The draft itself returns to the undeclared state,
// which is correct — a device that no longer reports position should display exactly
// like one that never did.
func TestClearingLocationDeclaration(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
		Token:    "tracker",
		Location: &LocationDeclaration{ExpectedAccuracyMeters: floatOf(7.25)},
	})
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "tracker", nil, nil, "tester")
	require.NoError(t, err)

	// An update carrying no declaration clears it — the same replace semantics every
	// other field on the request has.
	cleared, err := api.UpdateDeviceProfile(ctx, "tracker", &DeviceProfileCreateRequest{Token: "tracker"})
	require.NoError(t, err)
	draft, err := cleared.Location()
	require.NoError(t, err)
	assert.Nil(t, draft, "the clear did not take: the draft still declares a location")

	// Re-read from the database rather than trusting the returned struct — a clear that
	// only updated the in-memory value would pass the assertion above.
	reloaded, err := api.deviceProfileByToken(ctx, "tracker")
	require.NoError(t, err)
	persisted, err := reloaded.Location()
	require.NoError(t, err)
	assert.Nil(t, persisted, "the cleared declaration was not written as NULL")

	_, err = api.PublishDeviceProfile(ctx, "tracker", nil, nil, "tester")
	require.NoError(t, err)

	before := snapshotOfVersion(t, api, ctx, "tracker", 1)
	require.NotNil(t, before.Location, "clearing the draft must not reach back into an already-frozen version")
	require.NotNil(t, before.Location.ExpectedAccuracyMeters)
	assert.Equal(t, 7.25, *before.Location.ExpectedAccuracyMeters)

	after := snapshotOfVersion(t, api, ctx, "tracker", 2)
	assert.Nil(t, after.Location, "the version published after the clear must declare no location")
}

// A declaration whose stated expectations are not physically meaningful is refused at
// AUTHORING time. This validates the operator's form, never a device's data — nothing
// in the ingest path consults it.
func TestLocationDeclarationValidation(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for name, decl := range map[string]*LocationDeclaration{
		"negative accuracy": {ExpectedAccuracyMeters: floatOf(-1)},
		"zero accuracy":     {ExpectedAccuracyMeters: floatOf(0)},
		"negative interval": {ExpectedUpdateIntervalSeconds: int32Of(-5)},
		"zero interval":     {ExpectedUpdateIntervalSeconds: int32Of(0)},
	} {
		_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "p-" + name, Location: decl})
		assert.Error(t, err, "%s should be refused", name)
	}

	// The counterweight: a well-formed declaration still passes untouched.
	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
		Token:    "ok",
		Location: &LocationDeclaration{ExpectedAccuracyMeters: floatOf(1.5), ExpectedUpdateIntervalSeconds: int32Of(60)},
	})
	assert.NoError(t, err)
}

// A device type with no profile, and a profile that has never been published, both
// resolve to "undeclared" rather than to an error — the same limiting case an empty
// capability set has, and the reason the resolver's warning treats all three sources
// of nil identically.
func TestLocationDeclarationUnpublishedResolvesUndeclared(t *testing.T) {
	api := newLocationTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
		Token:    "tracker",
		Location: &LocationDeclaration{ExpectedAccuracyMeters: floatOf(2.5)},
	})
	require.NoError(t, err)

	// Adopted but never published: the draft declares a location, so this is exactly the
	// case where reading the draft instead of the active version would look correct.
	adopting := &DeviceType{}
	adopting.Token = "adopting"
	adopting.ProfileId = &created.ID
	require.NoError(t, api.RDB.DB(ctx).Create(adopting).Error)

	orphan := &DeviceType{}
	orphan.Token = "no-profile"
	require.NoError(t, api.RDB.DB(ctx).Create(orphan).Error)

	unpublished, err := api.LocationDeclarationByDeviceType(ctx, adopting.ID)
	require.NoError(t, err)
	assert.Nil(t, unpublished, "an unpublished draft declaration must not resolve — devices resolve the ACTIVE published version")

	none, err := api.LocationDeclarationByDeviceType(ctx, orphan.ID)
	require.NoError(t, err)
	assert.Nil(t, none)
}
