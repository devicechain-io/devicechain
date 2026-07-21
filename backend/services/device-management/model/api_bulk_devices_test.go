// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newBulkDeviceTestApi builds a SQLite-backed Api that, unlike a bare AutoMigrate,
// ALSO installs the per-tenant partial unique index on device token that production
// gets from a migration (rdb.CreateTenantTokenIndex). Without it a "the colliding
// batch rolls back" assertion would pass whether or not the code is transactional —
// SQLite would happily insert two identical tokens — which is precisely the
// check-that-cannot-fail this test exists to avoid. Installing the real constraint
// is what makes the rollback assertion mean something.
func newBulkDeviceTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}), "migrate")
	// Install BOTH per-tenant unique indexes production gets from migrations — token
	// AND external_id — so neither a token-collision nor an external-id-collision
	// rollback assertion can pass vacuously under the harness.
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &Device{}), "install device tenant-token unique index")
	require.NoError(t, rdb.CreateTenantExternalIdIndex(db, &Device{}), "install device tenant-external-id unique index")
	return NewApi(&rdb.RdbManager{Database: db})
}

func countDevices(t *testing.T, api *Api, ctx context.Context) int64 {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Count(&n).Error)
	return n
}

// --- pure expansion (no DB) ---------------------------------------------------

func TestExpandBulkDeviceRequest_RendersTokensNamesAndDefaults(t *testing.T) {
	reqs, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           3,
		TokenTemplate:   "car-{n:03d}",
		NameTemplate:    strp("Car {n}"),
	})
	require.NoError(t, err)
	require.Len(t, reqs, 3)
	assert.Equal(t, "car-001", reqs[0].Token)
	assert.Equal(t, "car-003", reqs[2].Token)
	assert.Equal(t, "Car 1", *reqs[0].Name)
	assert.Equal(t, "Car 3", *reqs[2].Name)
	assert.Equal(t, "tracker", reqs[0].DeviceTypeToken)
	assert.Nil(t, reqs[0].ExternalId, "no external-id template ⇒ nil external id")
}

func TestExpandBulkDeviceRequest_StartIndexOffsetsTheRange(t *testing.T) {
	reqs, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           2,
		StartIndex:      i32p(101),
		TokenTemplate:   "car-{n:04d}",
	})
	require.NoError(t, err)
	require.Len(t, reqs, 2)
	assert.Equal(t, "car-0101", reqs[0].Token)
	assert.Equal(t, "car-0102", reqs[1].Token)
}

func TestExpandBulkDeviceRequest_RejectsCountOutOfRange(t *testing.T) {
	for _, count := range []int32{0, -5, MaxBulkDeviceCount + 1} {
		_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
			DeviceTypeToken: "tracker", Count: count, TokenTemplate: "car-{n}",
		})
		assert.Error(t, err, "count %d must be rejected", count)
	}
}

func TestExpandBulkDeviceRequest_RequiresIndexPlaceholderInToken(t *testing.T) {
	_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           2,
		TokenTemplate:   "car", // no {n} ⇒ every device renders the same token
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index placeholder")
}

func TestExpandBulkDeviceRequest_RejectsUngrammaticalRenderedToken(t *testing.T) {
	// A space is not in the token grammar; it must be caught at render time, not
	// deep in a GraphQL/DB round-trip.
	_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           1,
		TokenTemplate:   "car {n}",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rendered token")
}

func TestExpandBulkDeviceRequest_RejectsFixedExternalIdForManyDevices(t *testing.T) {
	// A fixed external id across >1 device would collide on the per-tenant unique
	// index; it must be rejected up front, not fail opaquely mid-transaction.
	_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken:    "tracker",
		Count:              2,
		TokenTemplate:      "car-{n}",
		ExternalIdTemplate: strp("FIXED-VIN"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vary per device")

	// The same fixed external id is fine for a single device.
	reqs, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken:    "tracker",
		Count:              1,
		TokenTemplate:      "car-{n}",
		ExternalIdTemplate: strp("FIXED-VIN"),
	})
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Equal(t, "FIXED-VIN", *reqs[0].ExternalId)
}

func TestExpandBulkDeviceRequest_RandomExternalIdsVaryButTokensAreStable(t *testing.T) {
	reqs, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken:    "tracker",
		Count:              50,
		TokenTemplate:      "car-{n:04d}",
		ExternalIdTemplate: strp("VIN-{random}"),
	})
	require.NoError(t, err)
	require.Len(t, reqs, 50)

	seenExt := make(map[string]bool)
	for i, r := range reqs {
		// Token is a deterministic function of the index.
		assert.Equal(t, core.RenderTemplate("car-{n:04d}", i+1, nil), r.Token)
		// External id is present, prefixed, and unique across the batch.
		require.NotNil(t, r.ExternalId)
		assert.Contains(t, *r.ExternalId, "VIN-")
		assert.False(t, seenExt[*r.ExternalId], "external id %q repeated in batch", *r.ExternalId)
		seenExt[*r.ExternalId] = true
	}
}

func TestExpandBulkDeviceRequest_RejectsMemoryAmplifyingPadWidth(t *testing.T) {
	// {n:01000000d} would render a 1 MB string per device — a DoS across a batch.
	// It must be rejected before any rendering, whichever template carries it.
	for _, tc := range []struct {
		name string
		req  *DeviceBulkCreateRequest
	}{
		{"token", &DeviceBulkCreateRequest{DeviceTypeToken: "t", Count: 10, TokenTemplate: "d-{n:01000000d}"}},
		{"name", &DeviceBulkCreateRequest{DeviceTypeToken: "t", Count: 10, TokenTemplate: "d-{n}", NameTemplate: strp("N {n:01000000d}")}},
		{"externalId", &DeviceBulkCreateRequest{DeviceTypeToken: "t", Count: 10, TokenTemplate: "d-{n}", ExternalIdTemplate: strp("V-{n:01000000d}")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := expandBulkDeviceRequest(tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "pad width")
		})
	}
}

func TestExpandBulkDeviceRequest_RejectsRandomInTokenOrName(t *testing.T) {
	_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "t", Count: 2, TokenTemplate: "d-{n}-{random}",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokenTemplate must not use {random}")

	_, err = expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "t", Count: 2, TokenTemplate: "d-{n}", NameTemplate: strp("Device {random}"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nameTemplate must not use {random}")
}

func TestExpandBulkDeviceRequest_RejectsStartIndexBelowOne(t *testing.T) {
	for _, start := range []int32{0, -1, -100} {
		_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
			DeviceTypeToken: "t", Count: 2, TokenTemplate: "d-{n}", StartIndex: i32p(start),
		})
		require.Error(t, err, "startIndex %d must be rejected", start)
		assert.Contains(t, err.Error(), "startIndex")
	}
}

func TestExpandBulkDeviceRequest_RejectsOverLongRenderedName(t *testing.T) {
	// A 200-char literal name template renders a 200+ char name, over the 128 column.
	long := strings.Repeat("x", 200) + " {n}"
	_, err := expandBulkDeviceRequest(&DeviceBulkCreateRequest{
		DeviceTypeToken: "t", Count: 1, TokenTemplate: "d-{n}", NameTemplate: &long,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeding the maximum")
}

// --- transactional creation (DB) ----------------------------------------------

func TestCreateDevicesFromTemplate_CreatesFleetAndRostersEach(t *testing.T) {
	api := newBulkDeviceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap
	seedType(t, api, ctx, "tracker", "tracker-profile")

	created, err := api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           5,
		TokenTemplate:   "fleet-{n:04d}",
		NameTemplate:    strp("Fleet #{n}"),
	})
	require.NoError(t, err)
	assert.Len(t, created, 5)
	assert.Equal(t, int64(5), countDevices(t, api, ctx))

	// Every device is rostered under the type's profile (post-commit, once each).
	require.Len(t, cap.events, 5, "each created device emits exactly one roster fact")
	got := make(map[string]string)
	for _, ev := range cap.events {
		got[ev.DeviceToken] = ev.ProfileToken
	}
	assert.Equal(t, "tracker-profile", got["fleet-0001"])
	assert.Equal(t, "tracker-profile", got["fleet-0005"])
}

func TestCreateDevicesFromTemplate_UnknownTypeCreatesNothing(t *testing.T) {
	api := newBulkDeviceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "ghost",
		Count:           4,
		TokenTemplate:   "fleet-{n}",
	})
	require.Error(t, err)
	assert.Equal(t, int64(0), countDevices(t, api, ctx), "an unknown type must create no devices")
}

// The whole point of the transaction: if any rendered token collides with an
// existing device, NONE of the batch is created — not the ones before the
// collision, not the ones after. The tenant-token unique index installed in the
// test harness is what makes this a real assertion rather than a vacuous one.
func TestCreateDevicesFromTemplate_CollisionRollsBackWholeBatch(t *testing.T) {
	api := newBulkDeviceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	// A publisher is wired so the test also pins the "roster emit is POST-COMMIT"
	// property: a rolled-back batch must emit NO roster facts. Without this, moving
	// the emit inside the transaction would leave every assertion green.
	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap
	seedType(t, api, ctx, "tracker", "")

	// Pre-existing device occupying the token the batch's 2nd render will hit.
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "fleet-0002", DeviceTypeToken: "tracker"})
	require.NoError(t, err)
	require.Equal(t, int64(1), countDevices(t, api, ctx))
	cap.events = nil // discard the single-create's roster fact; we only care about the bulk batch

	_, err = api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{
		DeviceTypeToken: "tracker",
		Count:           3, // renders fleet-0001, fleet-0002 (collides), fleet-0003
		TokenTemplate:   "fleet-{n:04d}",
	})
	require.Error(t, err, "a token collision must fail the batch")

	// Only the original device survives: fleet-0001 (created before the collision)
	// was rolled back, and fleet-0003 (after) was never reached.
	assert.Equal(t, int64(1), countDevices(t, api, ctx), "the whole batch rolled back")
	var remaining []string
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Pluck("token", &remaining).Error)
	assert.Equal(t, []string{"fleet-0002"}, remaining)
	// No roster fact for any device in the rolled-back batch (emit is post-commit).
	assert.Empty(t, cap.events, "a rolled-back batch must emit no roster facts")
}

func i32p(v int32) *int32 { return &v }
