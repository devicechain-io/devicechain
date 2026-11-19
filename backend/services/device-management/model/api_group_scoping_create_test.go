// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func registerCreateGroup(t *testing.T, api *Api, ctx context.Context, family, source string, access bool) (*EntityGroup, int32) {
	t.Helper()
	mode := string(MembershipDynamic)
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "new-members", MemberType: family, MembershipMode: &mode, Selector: &source})
	require.NoError(t, err)
	v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
	require.NoError(t, err)
	require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
	if access {
		_, err = api.SetEntityGroupAccessScope(ctx, g.Token, true)
		require.NoError(t, err)
	}
	return g, v.Version
}

func assertCreateMembership(t *testing.T, api *Api, ctx context.Context, family string, id uint, g *EntityGroup, version int32) {
	t.Helper()
	var rows []EntityGroupMembership
	require.NoError(t, api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ?", family, id).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, g.ID, rows[0].GroupId)
	require.Equal(t, version, rows[0].SelectorVersion)
	require.True(t, isMemberOf(t, api, ctx, family, id, g.ID, version))
	if family == "device" {
		members, err := api.MembershipsForEntity(ctx, family, id)
		require.NoError(t, err)
		require.True(t, members[0].AccessScope)
		var d Device
		require.NoError(t, api.RDB.DB(ctx).First(&d, id).Error)
		found, err := api.DevicesByToken(groupMember("group:"+g.Token), []string{d.Token})
		require.NoError(t, err)
		require.Len(t, found, 1)
		pub, ok := api.DeviceOwnershipPublisher.(*captureOwnership)
		if !ok {
			pub = &captureOwnership{api: api, t: t}
			api.DeviceOwnershipPublisher = pub
		}
		fact, ok := lastFactFor(pub, 0, d.Token)
		require.True(t, ok)
		require.Contains(t, fact.Anchors, "group:"+g.Token)
	}
}

func TestCreateEnrollDevice(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, true)
	d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "fresh", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	assertCreateMembership(t, api, ctx, "device", d.ID, g, v)
}

func TestCreateEnrollBulk(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, true)
	ds, err := api.CreateDevices(ctx, []*DeviceCreateRequest{{Token: "fresh-a", DeviceTypeToken: "sensor"}, {Token: "fresh-b", DeviceTypeToken: "sensor"}})
	require.NoError(t, err)
	require.Len(t, ds, 2)
	for _, d := range ds {
		assertCreateMembership(t, api, ctx, "device", d.ID, g, v)
	}
}

func TestCreateEnrollTemplate(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, true)
	ds, err := api.CreateDevicesFromTemplate(ctx, &DeviceBulkCreateRequest{DeviceTypeToken: "sensor", Count: 2, TokenTemplate: "fresh-{n}"})
	require.NoError(t, err)
	require.Len(t, ds, 2)
	for _, d := range ds {
		assertCreateMembership(t, api, ctx, "device", d.ID, g, v)
	}
}

func TestCreateEnrollImport(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, true)
	result, err := api.ImportDevices(ctx, []*DeviceImportRow{{Token: "fresh", DeviceTypeToken: "sensor"}}, DeviceImportModeNone, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Created)
	ds, err := api.DevicesByToken(ctx, []string{"fresh"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assertCreateMembership(t, api, ctx, "device", ds[0].ID, g, v)
}

func TestCreateEnrollProvisioning(t *testing.T) {
	f := newProvFixture(t)
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(f.ctx)).AutoMigrate(&EntityAttribute{}, &EntityGroup{}, &IssuedGroupToken{}, &EntityGroupVersion{}, &EntityGroupFacetRef{}, &EntityGroupMembership{}, &PendingOwnershipFact{}, &EntityRelationship{}, &EntityRelationshipType{}, &AlarmSuppressionWindow{}))
	g, v := registerCreateGroup(t, f.api, f.ctx, "device", `!("retired" in attr)`, true)
	f.profile("pp", ProvisionAllowNew)
	result, err := f.register("pp", "SN-fresh", material(1))
	require.NoError(t, err)
	require.Equal(t, ProvisionCreated, result.Outcome)
	d := f.deviceByExternal("SN-fresh")
	assertCreateMembership(t, f.api, f.ctx, "device", d.ID, g, v)
}

func TestCreateEnrollAsset(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	require.NoError(t, api.RDB.Database.AutoMigrate(&Asset{}, &AssetType{}, &AssetTypeVersion{}))
	_, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "equipment"})
	require.NoError(t, err)
	g, v := registerCreateGroup(t, api, ctx, "asset", `!("retired" in attr)`, false)
	a, err := api.CreateAsset(ctx, &AssetCreateRequest{Token: "fresh", AssetTypeToken: "equipment"})
	require.NoError(t, err)
	assertCreateMembership(t, api, ctx, "asset", a.ID, g, v)
}

func TestCreateEnrollArea(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "area", `!("retired" in attr)`, false)
	a, err := api.CreateArea(ctx, &AreaCreateRequest{Token: "fresh", AreaTypeToken: "region"})
	require.NoError(t, err)
	assertCreateMembership(t, api, ctx, "area", a.ID, g, v)
}

func TestCreateEnrollCustomer(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	require.NoError(t, api.RDB.Database.AutoMigrate(&Customer{}, &CustomerType{}, &IssuedCustomerToken{}))
	_, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: "business"})
	require.NoError(t, err)
	g, v := registerCreateGroup(t, api, ctx, "customer", `!("retired" in attr)`, false)
	c, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: "fresh", CustomerTypeToken: "business"})
	require.NoError(t, err)
	assertCreateMembership(t, api, ctx, "customer", c.ID, g, v)
}

func TestCreateEnrollNegativeControls(t *testing.T) {
	for _, scenario := range []string{"positive-selector", "other-family", "deregistered", "no-groups"} {
		t.Run(scenario, func(t *testing.T) {
			api, ctx := newScopingTestApi(t)
			if scenario != "no-groups" {
				family, source := "device", `!("retired" in attr)`
				if scenario == "other-family" {
					family = "area"
				}
				if scenario == "positive-selector" {
					source = `"retired" in attr`
				}
				g, v := registerCreateGroup(t, api, ctx, family, source, false)
				if scenario == "deregistered" {
					require.NoError(t, api.DeregisterGroupVersionForScoping(ctx, g.Token, v))
				}
			}
			d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "fresh", DeviceTypeToken: "sensor"})
			require.NoError(t, err)
			ms, err := api.MembershipsForEntity(ctx, "device", d.ID)
			require.NoError(t, err)
			require.Empty(t, ms)
		})
	}
}

func TestCreateEnrollFailureRollsBack(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, true)
	injected := errors.New("enrollment unavailable")
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register("fail-enrollment", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Name == "EntityGroupMembership" {
			db.AddError(injected)
		}
	}))
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "rolled-back", DeviceTypeToken: "sensor"})
	require.ErrorIs(t, err, injected)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", "rolled-back").Count(&n).Error)
	require.Zero(t, n)
	require.Zero(t, countOf(t, api, ctx, &PendingOwnershipFact{}))
}

// A selector that names the reserved key itself writes a ref with the marker's key (through
// Keys()), though it does not match an entity with no attributes. Creation evaluates the frozen
// selector rather than trusting the ref, so the new device is not enrolled.
func TestCreateEnrollIgnoresAReservedKeyCollision(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `"\x01empty" in attr`, false)
	var refs []EntityGroupFacetRef
	require.NoError(t, api.RDB.DB(ctx).Where("facet_key = ? AND group_id = ? AND selector_version = ?",
		createEnrollFacetKey, g.ID, v).Find(&refs).Error)
	require.Len(t, refs, 1, "precondition: the collision writes a ref under the reserved key")
	d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "fresh", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	ms, err := api.MembershipsForEntity(ctx, "device", d.ID)
	require.NoError(t, err)
	require.Empty(t, ms)
}
