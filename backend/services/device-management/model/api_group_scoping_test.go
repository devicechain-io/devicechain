// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// newScopingTestApi builds an Api with every table the ADR-062 S2 membership subsystem
// touches: the member families (devices + types, areas), the attribute store, groups +
// their versions, the read-model + reverse index, and the delete-cascade tables.
func newScopingTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	// Shared-cache named in-memory DB: an attribute write now opens a transaction while
	// ResolveEntityToken already ran on the pool connection, so all connections must see
	// the same tables (a bare ":memory:" gives one DB per connection).
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceCredential{}, &Area{}, &AreaType{},
		&EntityAttribute{}, &EntityGroup{}, &EntityGroupVersion{}, &EntityGroupMembership{},
		&EntityGroupFacetRef{}, &EntityRelationship{}, &Alarm{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "region"}); err != nil {
		t.Fatalf("seed area type: %v", err)
	}
	return api, ctx
}

// setSharedClimate sets (or with delete=true removes) a device's SHARED climate facet.
func setSharedClimate(t *testing.T, api *Api, ctx context.Context, token, climate string) {
	t.Helper()
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: token,
		Scope: string(AttributeScopeShared), AttrKey: "climate",
		ValueType: string(AttributeValueString), Value: &climate,
	}); err != nil {
		t.Fatalf("set climate on %s: %v", token, err)
	}
}

func seedDevice(t *testing.T, api *Api, ctx context.Context, token string) uint {
	t.Helper()
	d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatalf("seed device %s: %v", token, err)
	}
	return d.ID
}

// publishAridGroup creates a dynamic device group scoped to climate == "arid", publishes
// it, and returns the group + its version.
func publishAridGroup(t *testing.T, api *Api, ctx context.Context, token string) (*EntityGroup, int32) {
	t.Helper()
	g := createDynamicGroup(t, api, ctx, token, `attr["climate"] == "arid"`)
	v, err := api.PublishEntityGroup(ctx, token, nil, nil, "tester")
	if err != nil {
		t.Fatalf("publish %s: %v", token, err)
	}
	return g, v.Version
}

// isMemberOf reports whether the entity has a membership row for group@version.
func isMemberOf(t *testing.T, api *Api, ctx context.Context, entityType string, entityId, groupId uint, version int32) bool {
	t.Helper()
	memberships, err := api.MembershipsForEntity(ctx, entityType, entityId)
	if err != nil {
		t.Fatalf("memberships for %s/%d: %v", entityType, entityId, err)
	}
	for _, m := range memberships {
		if m.GroupId == groupId && m.SelectorVersion == version {
			return true
		}
	}
	return false
}

// Register backfills the read-model with the frozen selector's current matches and
// populates the reverse index with its facet keys.
func TestRegister_BackfillsMembershipAndReverseIndex(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	arid1 := seedDevice(t, api, ctx, "d1")
	arid2 := seedDevice(t, api, ctx, "d2")
	humid := seedDevice(t, api, ctx, "d3")
	setSharedClimate(t, api, ctx, "d1", "arid")
	setSharedClimate(t, api, ctx, "d2", "arid")
	setSharedClimate(t, api, ctx, "d3", "humid")

	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}

	assert.True(t, isMemberOf(t, api, ctx, "device", arid1, g.ID, v), "d1 backfilled as member")
	assert.True(t, isMemberOf(t, api, ctx, "device", arid2, g.ID, v), "d2 backfilled as member")
	assert.False(t, isMemberOf(t, api, ctx, "device", humid, g.ID, v), "d3 (humid) is not a member")

	// The reverse index carries the selector's one facet key for this group@v.
	var refs []EntityGroupFacetRef
	assert.NoError(t, api.RDB.DB(ctx).Where("group_id = ? AND selector_version = ?", g.ID, v).Find(&refs).Error)
	if assert.Len(t, refs, 1) {
		assert.Equal(t, "climate", refs[0].FacetKey)
		assert.Equal(t, "device", refs[0].MemberType)
		assert.Equal(t, "arid-areas", refs[0].GroupToken)
	}
}

// A SHARED facet write recomputes membership incrementally: a device that starts humid
// joins when set arid, and an arid member leaves when set humid.
func TestRecompute_JoinAndLeaveOnSet(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "humid")

	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	assert.False(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "humid device starts a non-member")

	// Join on set to arid.
	setSharedClimate(t, api, ctx, "d1", "arid")
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "device joins when set arid")

	// Leave on set back to humid.
	setSharedClimate(t, api, ctx, "d1", "humid")
	assert.False(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "device leaves when set humid")
}

// Removing the facet that made a device a member drops its membership (same-txn recompute
// on delete).
func TestRecompute_LeaveOnDelete(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")

	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "arid device is a member")

	removed, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeShared), "climate")
	assert.NoError(t, err)
	assert.True(t, removed)
	assert.False(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "device leaves when its facet is removed")
}

// A CLIENT-scope facet write does not affect membership: the selector reads SHARED facets
// only, and the recompute hook is skipped for CLIENT (the ADR-062 device-can't-self-scope
// invariant).
func TestRecompute_ClientScopeIgnored(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDevice(t, api, ctx, "d1")

	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}

	// A device reports climate=arid at CLIENT scope — it must NOT become a member.
	arid := "arid"
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d1",
		Scope: string(AttributeScopeClient), AttrKey: "climate",
		ValueType: string(AttributeValueString), Value: &arid,
	}); err != nil {
		t.Fatalf("set client climate: %v", err)
	}
	assert.False(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "a CLIENT facet cannot create membership")
}

// Deregister GCs a group@version's read-model + reverse-index rows.
func TestDeregister_GCsRows(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v))

	if err := api.DeregisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	assert.False(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "membership GC'd on deregister")
	var refs int64
	api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).Where("group_id = ?", g.ID).Count(&refs)
	assert.EqualValues(t, 0, refs, "reverse-index rows GC'd on deregister")
}

// Deleting a member entity purges its membership rows (delete-cascade teardown).
func TestEntityDelete_PurgesMembership(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v))

	deleted, err := api.DeleteDevice(ctx, "d1")
	assert.NoError(t, err)
	assert.True(t, deleted)

	var rows int64
	api.RDB.DB(ctx).Unscoped().Model(&EntityGroupMembership{}).
		Where("entity_type = ? AND entity_id = ?", "device", d).Count(&rows)
	assert.EqualValues(t, 0, rows, "the deleted device's membership rows are purged")
}

// Deleting a group purges all its scoping rows (both directions) with its versions.
func TestGroupDelete_PurgesScopingRows(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-areas", v); err != nil {
		t.Fatalf("register: %v", err)
	}

	deleted, err := api.DeleteEntityGroup(ctx, "arid-areas")
	assert.NoError(t, err)
	assert.True(t, deleted)

	var mem, ref int64
	api.RDB.DB(ctx).Unscoped().Model(&EntityGroupMembership{}).Where("group_id = ?", g.ID).Count(&mem)
	api.RDB.DB(ctx).Unscoped().Model(&EntityGroupFacetRef{}).Where("group_id = ?", g.ID).Count(&ref)
	assert.EqualValues(t, 0, mem, "membership rows purged on group delete")
	assert.EqualValues(t, 0, ref, "reverse-index rows purged on group delete")
}

// A recompute is family-filtered: a device group registered on "climate" is not disturbed
// by an AREA's climate write (row ids are per-table; the reverse index carries member_type).
func TestRecompute_FamilyIsolation(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	dev := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-devices")
	if err := api.RegisterGroupVersionForScoping(ctx, "arid-devices", v); err != nil {
		t.Fatalf("register: %v", err)
	}
	assert.True(t, isMemberOf(t, api, ctx, "device", dev, g.ID, v))

	// An area reports a SHARED climate=arid facet. The device group's reverse-index rows
	// are member_type=device, so the area's write must not create a device-group membership
	// for the area (even if the area's row id collides with the device's).
	area, err := api.CreateArea(ctx, &AreaCreateRequest{Token: "a1", AreaTypeToken: "region"})
	if err != nil {
		t.Fatalf("seed area: %v", err)
	}
	arid := "arid"
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeArea.String(), Entity: "a1",
		Scope: string(AttributeScopeShared), AttrKey: "climate",
		ValueType: string(AttributeValueString), Value: &arid,
	}); err != nil {
		t.Fatalf("set area climate: %v", err)
	}
	assert.False(t, isMemberOf(t, api, ctx, "area", area.ID, g.ID, v), "an area write does not join a device group")
}
