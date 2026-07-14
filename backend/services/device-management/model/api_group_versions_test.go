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
	"gorm.io/gorm"
)

// newGroupVersionTestApi builds an Api with the tables entity-group versioning + a
// group delete (deleteEdgeEntity cascade) touch.
func newGroupVersionTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&EntityGroup{}, &EntityGroupVersion{},
		&EntityGroupMembership{}, &EntityGroupFacetRef{},
		&EntityRelationship{}, &EntityAttribute{}, &Alarm{},
		&DetectionRule{}, &DeviceProfile{}, &DeviceProfileVersion{}, &DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}), core.WithTenant(context.Background(), "acme")
}

// createDynamicGroup is a test helper that creates a dynamic group with a valid,
// lowerable selector.
func createDynamicGroup(t *testing.T, api *Api, ctx context.Context, token, selector string) *EntityGroup {
	t.Helper()
	dynamic := string(MembershipDynamic)
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token:          token,
		MemberType:     "device",
		MembershipMode: &dynamic,
		Selector:       &selector,
	})
	if err != nil {
		t.Fatalf("create dynamic group %q: %v", token, err)
	}
	return g
}

// PublishEntityGroup freezes the draft selector into a new monotonic version and
// advances the active pointer; the frozen selector is captured verbatim and later
// versions do not disturb earlier ones.
func TestPublishEntityGroup_MonotonicAndActivePointer(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)

	v1, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "alice")
	assert.NoError(t, err)
	assert.EqualValues(t, 1, v1.Version)
	assert.Equal(t, `attr["climate"] == "arid"`, v1.Selector, "the draft selector is frozen verbatim")
	assert.Equal(t, "device", v1.MemberType)
	assert.Equal(t, "alice", v1.PublishedBy)

	// The active pointer now names v1.
	g, err := api.entityGroupByToken(ctx, "g1")
	assert.NoError(t, err)
	assert.True(t, g.ActiveVersion.Valid)
	assert.EqualValues(t, 1, g.ActiveVersion.Int32)

	// Edit the draft, then publish v2 — monotonic, and the active pointer advances.
	newSel := `attr["climate"] == "humid"`
	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupCreateRequest{Token: "g1", Selector: &newSel}); err != nil {
		t.Fatalf("edit draft: %v", err)
	}
	v2, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "bob")
	assert.NoError(t, err)
	assert.EqualValues(t, 2, v2.Version)
	assert.Equal(t, newSel, v2.Selector)

	g, _ = api.entityGroupByToken(ctx, "g1")
	assert.EqualValues(t, 2, g.ActiveVersion.Int32)

	// v1's frozen selector is untouched by the v2 publish.
	frozenV1, err := api.EntityGroupVersionByNumber(ctx, g.ID, 1)
	assert.NoError(t, err)
	assert.Equal(t, `attr["climate"] == "arid"`, frozenV1.Selector, "an earlier version is immutable")

	// Versions list is newest-first.
	versions, err := api.EntityGroupVersions(ctx, "g1")
	assert.NoError(t, err)
	if assert.Len(t, versions, 2) {
		assert.EqualValues(t, 2, versions[0].Version)
		assert.EqualValues(t, 1, versions[1].Version)
	}
}

// A static group has no selector to freeze, so publishing it fails closed.
func TestPublishEntityGroup_RejectsStatic(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "s1", MemberType: "device"}); err != nil {
		t.Fatalf("create static group: %v", err)
	}
	_, err := api.PublishEntityGroup(ctx, "s1", nil, nil, "alice")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only a dynamic entity group can be published")
}

// Publishing a group that does not exist fails closed with NotFound.
func TestPublishEntityGroup_NotFound(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	_, err := api.PublishEntityGroup(ctx, "ghost", nil, nil, "alice")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Rollback re-points the active version at an earlier version without destroying any
// version, and rejects a version that does not exist.
func TestRollbackEntityGroup(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	g := createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)

	if _, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "alice"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	newSel := `attr["climate"] == "humid"`
	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupCreateRequest{Token: "g1", Selector: &newSel}); err != nil {
		t.Fatalf("edit draft: %v", err)
	}
	if _, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "bob"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	// Active is v2; roll back to v1.
	rolled, err := api.RollbackEntityGroup(ctx, "g1", 1)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, rolled.ActiveVersion.Int32)

	// Both versions still exist (non-destructive).
	versions, err := api.EntityGroupVersions(ctx, "g1")
	assert.NoError(t, err)
	assert.Len(t, versions, 2)

	// The draft is untouched by rollback — the live selector is still v2's edit.
	g, _ = api.entityGroupByToken(ctx, "g1")
	assert.Equal(t, newSel, g.Selector.String, "rollback does not disturb the draft")

	// Rolling back to a nonexistent version fails closed.
	_, err = api.RollbackEntityGroup(ctx, "g1", 99)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Rolling back a group that does not exist fails closed.
func TestRollbackEntityGroup_NotFound(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	_, err := api.RollbackEntityGroup(ctx, "ghost", 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Deleting a group cascades its frozen versions (append-only history is meaningless
// once the group is gone) and — with no rule referencing it (S4) — is not refused.
func TestDeleteEntityGroup_CascadesVersions(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	g := createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)
	if _, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "alice"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "alice"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	var before int64
	api.RDB.DB(ctx).Model(&EntityGroupVersion{}).Where("entity_group_id = ?", g.ID).Count(&before)
	assert.EqualValues(t, 2, before)

	deleted, err := api.DeleteEntityGroup(ctx, "g1")
	assert.NoError(t, err)
	assert.True(t, deleted)

	// The group and all its versions are gone.
	_, err = api.entityGroupByToken(ctx, "g1")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var after int64
	api.RDB.DB(ctx).Unscoped().Model(&EntityGroupVersion{}).Where("entity_group_id = ?", g.ID).Count(&after)
	assert.EqualValues(t, 0, after, "the group's frozen versions are cascade-deleted")
}

// The (entity_group_id, version) unique index rejects a duplicate version number —
// the guard that makes concurrent publishes safe (the loser's insert fails).
func TestEntityGroupVersion_DuplicateVersionRejected(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	g := createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)

	first := &EntityGroupVersion{EntityGroupId: g.ID, Version: 1, Selector: "x", MemberType: "device", SelectorSchema: 1}
	assert.NoError(t, api.RDB.DB(ctx).Create(first).Error)

	dup := &EntityGroupVersion{EntityGroupId: g.ID, Version: 1, Selector: "y", MemberType: "device", SelectorSchema: 1}
	assert.Error(t, api.RDB.DB(ctx).Create(dup).Error, "a duplicate (entity_group_id, version) must be rejected")
}

// A draft edit must NOT revert the active-version pointer (the MED race Fable found):
// UpdateEntityGroup omits active_version, so an edit after a publish leaves the
// pointer intact.
func TestUpdateEntityGroup_DoesNotRevertActiveVersion(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	createDynamicGroup(t, api, ctx, "g1", `attr["climate"] == "arid"`)
	if _, err := api.PublishEntityGroup(ctx, "g1", nil, nil, "alice"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Edit the draft selector after publishing.
	newSel := `attr["climate"] == "humid"`
	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupCreateRequest{Token: "g1", Selector: &newSel}); err != nil {
		t.Fatalf("edit draft: %v", err)
	}

	g, err := api.entityGroupByToken(ctx, "g1")
	assert.NoError(t, err)
	assert.True(t, g.ActiveVersion.Valid, "the edit must not clear the active pointer")
	assert.EqualValues(t, 1, g.ActiveVersion.Int32, "the edit must not revert the active pointer")
	assert.Equal(t, newSel, g.Selector.String, "the draft selector is still updated")
}

// Versions are tenant-scoped: two tenants may own a group under the same token, and
// each sees only its own versions.
func TestEntityGroupVersions_TenantIsolation(t *testing.T) {
	api, _ := newGroupVersionTestApi(t)
	ctxA := core.WithTenant(context.Background(), "acme")
	ctxB := core.WithTenant(context.Background(), "globex")

	createDynamicGroup(t, api, ctxA, "shared", `attr["climate"] == "arid"`)
	if _, err := api.PublishEntityGroup(ctxA, "shared", nil, nil, "a"); err != nil {
		t.Fatalf("A publish v1: %v", err)
	}
	if _, err := api.PublishEntityGroup(ctxA, "shared", nil, nil, "a"); err != nil {
		t.Fatalf("A publish v2: %v", err)
	}

	// Tenant B owns a same-token group with its own single version.
	createDynamicGroup(t, api, ctxB, "shared", `attr["climate"] == "humid"`)
	if _, err := api.PublishEntityGroup(ctxB, "shared", nil, nil, "b"); err != nil {
		t.Fatalf("B publish v1: %v", err)
	}

	aVersions, err := api.EntityGroupVersions(ctxA, "shared")
	assert.NoError(t, err)
	assert.Len(t, aVersions, 2, "tenant A sees only its two versions")

	bVersions, err := api.EntityGroupVersions(ctxB, "shared")
	assert.NoError(t, err)
	assert.Len(t, bVersions, 1, "tenant B sees only its one version")
	assert.Equal(t, `attr["climate"] == "humid"`, bVersions[0].Selector, "B reads its own frozen selector, not A's")
}

// Deleting a nonexistent group token is a no-op (mirrors deleteEdgeEntity), not an
// error — and the ErrEntityInUse guard is reachable machinery even while structurally
// false in S1.
func TestDeleteEntityGroup_UnknownTokenIsNoOp(t *testing.T) {
	api, ctx := newGroupVersionTestApi(t)
	deleted, err := api.DeleteEntityGroup(ctx, "ghost")
	assert.NoError(t, err)
	assert.False(t, deleted)
	// The guard returns "not referenced" for a group no rule scopes to.
	inUse, err := api.entityGroupReferencedByRule(ctx, api.RDB.DB(ctx), 1)
	assert.NoError(t, err)
	assert.False(t, inUse)
}
