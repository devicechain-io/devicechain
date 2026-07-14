// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// newGroupMemberTestApi builds an Api with the tables group-member resolution touches:
// the member family (devices + types), the facet store, groups, and the membership edge.
func newGroupMemberTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	// A shared-cache, per-test named in-memory DB: CreateEntityRelationships resolves its
	// source/target on a non-transaction connection WHILE a write transaction is open (the
	// production Postgres pattern), so the pool's connections must all see the same tables —
	// which a bare ":memory:" (one DB per connection) does not provide.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &EntityAttribute{},
		&EntityGroup{}, &EntityGroupMembership{}, &EntityGroupFacetRef{},
		&EntityRelationship{}, &EntityRelationshipType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	return api, ctx
}

// seedDeviceWithClimate creates a device and (optionally) sets its SHARED climate facet.
func seedDeviceWithClimate(t *testing.T, api *Api, ctx context.Context, token, climate string) uint {
	t.Helper()
	d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatalf("seed device %s: %v", token, err)
	}
	if climate != "" {
		if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
			EntityType: entity.TypeDevice.String(), Entity: token,
			Scope: string(AttributeScopeShared), AttrKey: "climate",
			ValueType: string(AttributeValueString), Value: &climate,
		}); err != nil {
			t.Fatalf("set climate on %s: %v", token, err)
		}
	}
	return d.ID
}

// A dynamic group resolves the members whose facets satisfy its selector, and IsGroupMember
// answers the single-entity test off the same predicate.
func TestResolveGroupMembers_Dynamic(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	aridID := seedDeviceWithClimate(t, api, ctx, "d-arid", "arid")
	humidID := seedDeviceWithClimate(t, api, ctx, "d-humid", "humid")
	bareID := seedDeviceWithClimate(t, api, ctx, "d-bare", "") // no climate facet

	dynamic := string(MembershipDynamic)
	sel := `attr["climate"] == "arid"`
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "arid-areas", MemberType: "device", MembershipMode: &dynamic, Selector: &sel,
	})
	if err != nil {
		t.Fatalf("create dynamic group: %v", err)
	}
	assert.True(t, g.Selector.Valid, "dynamic group carries its selector")
	assert.Equal(t, 1, g.SelectorSchema, "selector schema stamped")

	res, err := api.ResolveGroupMembers(ctx, g, rdb.Pagination{PageNumber: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	tokens := make([]string, 0, len(res.Results))
	for _, m := range res.Results {
		tokens = append(tokens, m.Token)
	}
	assert.ElementsMatch(t, []string{"d-arid"}, tokens, "only the arid device is a member")

	// IsGroupMember off the same lowered predicate.
	isArid, err := api.IsGroupMember(ctx, g, aridID)
	assert.NoError(t, err)
	assert.True(t, isArid, "the arid device is a member")

	for _, nonMember := range []uint{humidID, bareID} {
		is, err := api.IsGroupMember(ctx, g, nonMember)
		assert.NoError(t, err)
		assert.False(t, is, "device %d is not a member", nonMember)
	}
}

// A static group resolves its "member" edges, and IsGroupMember degrades to an edge check.
func TestResolveGroupMembers_Static(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	memberID := seedDeviceWithClimate(t, api, ctx, "d1", "")
	seedDeviceWithClimate(t, api, ctx, "d2", "") // not a member

	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "fleet", MemberType: "device"})
	if err != nil {
		t.Fatalf("create static group: %v", err)
	}
	if _, err := api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{{
		Token:            "e1",
		RelationshipType: MembershipRelationshipType,
		SourceType:       string(entity.TypeGroup), Source: "fleet",
		TargetType: entity.TypeDevice.String(), Target: "d1",
	}}); err != nil {
		t.Fatalf("add member edge: %v", err)
	}

	res, err := api.ResolveGroupMembers(ctx, g, rdb.Pagination{PageNumber: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("resolve static: %v", err)
	}
	tokens := make([]string, 0, len(res.Results))
	for _, m := range res.Results {
		tokens = append(tokens, m.Token)
	}
	assert.ElementsMatch(t, []string{"d1"}, tokens, "the one member edge resolves")

	is, err := api.IsGroupMember(ctx, g, memberID)
	assert.NoError(t, err)
	assert.True(t, is, "the edge target is a member")
}

// PreviewSelector evaluates a candidate selector without saving a group — the console's
// live "matches N" authoring preview. It returns the same matches a saved dynamic group
// would resolve, and surfaces a non-lowerable candidate as the selector engine's typed error.
func TestPreviewSelector(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d-arid", "arid")
	seedDeviceWithClimate(t, api, ctx, "d-humid", "humid")
	seedDeviceWithClimate(t, api, ctx, "d-bare", "") // no climate facet

	// A valid candidate matches exactly the entities a saved group would.
	res, err := api.PreviewSelector(ctx, "device", `attr["climate"] == "arid"`,
		rdb.Pagination{PageNumber: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("preview valid selector: %v", err)
	}
	tokens := make([]string, 0, len(res.Results))
	for _, m := range res.Results {
		tokens = append(tokens, m.Token)
	}
	assert.ElementsMatch(t, []string{"d-arid"}, tokens, "preview matches only the arid device")
	assert.Equal(t, int32(1), res.Pagination.TotalRecords)

	// A non-lowerable candidate (two indexes) surfaces the selector engine's typed error so
	// the query resolver can present it inline rather than as a server fault.
	_, err = api.PreviewSelector(ctx, "device", `attr["a"] == attr["b"]`,
		rdb.Pagination{PageNumber: 1, PageSize: 100})
	assert.Error(t, err, "a non-lowerable candidate is rejected")
	var notLowerable *selector.NotLowerableError
	assert.True(t, errors.As(err, &notLowerable), "rejection is a typed NotLowerableError, got %T", err)

	// An unknown member family fails closed before any compilation.
	_, err = api.PreviewSelector(ctx, "widget", `attr["climate"] == "arid"`,
		rdb.Pagination{PageNumber: 1, PageSize: 100})
	assert.Error(t, err, "an unknown member family is rejected")
}

// A dynamic resolve never runs an unbounded scan even if a caller requests one.
func TestResolveGroupMembers_ForcesBounded(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d-arid", "arid")

	dynamic := string(MembershipDynamic)
	sel := `"climate" in attr`
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "any-climate", MemberType: "device", MembershipMode: &dynamic, Selector: &sel,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := api.ResolveGroupMembers(ctx, g, rdb.Pagination{PageNumber: 1, PageSize: 100, Unbounded: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Unbounded was forced off, so the page reports a real bounded span, not the all-rows form.
	assert.Equal(t, int32(1), res.Pagination.TotalRecords)
}
