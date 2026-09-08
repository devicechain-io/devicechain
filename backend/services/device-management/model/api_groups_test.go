// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func newGroupTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&EntityGroup{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// ValidGroupMemberType accepts every non-group entity family and rejects the
// group type itself plus anything unknown.
func TestValidGroupMemberType(t *testing.T) {
	for _, ok := range []entity.Type{entity.TypeDevice, entity.TypeAsset, entity.TypeArea, entity.TypeCustomer} {
		if !ValidGroupMemberType(ok) {
			t.Errorf("expected %q to be a valid group member type", ok)
		}
	}
	for _, bad := range []entity.Type{entity.TypeGroup, "", "widget", "GROUP"} {
		if ValidGroupMemberType(bad) {
			t.Errorf("expected %q to be an invalid group member type", bad)
		}
	}
}

// CreateEntityGroup defaults a nil mode to static, admits a valid static group,
// and fails closed on an unknown member family, an explicit dynamic mode, a
// selector on a static group, and a garbage mode.
func TestCreateEntityGroup_Validation(t *testing.T) {
	api := newGroupTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// A valid static group (nil mode → static).
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "g1", MemberType: "device"})
	if err != nil {
		t.Fatalf("valid static group rejected: %v", err)
	}
	if g.MembershipMode != string(MembershipStatic) {
		t.Fatalf("expected default mode static, got %q", g.MembershipMode)
	}
	if g.Selector.Valid {
		t.Fatalf("a static group must carry no selector")
	}

	dynamic := string(MembershipDynamic)
	sel := "attr[\"climate\"] == \"arid\""
	badSel := "attr[\"a\"] == attr[\"b\"]" // not lowerable (two indexes)
	garbage := "sideways"
	cases := []struct {
		name string
		req  *EntityGroupCreateRequest
	}{
		{"unknown member family", &EntityGroupCreateRequest{Token: "b1", MemberType: "widget"}},
		{"group member family", &EntityGroupCreateRequest{Token: "b2", MemberType: "group"}},
		{"dynamic mode without selector", &EntityGroupCreateRequest{Token: "b3", MemberType: "device", MembershipMode: &dynamic}},
		{"selector on static group", &EntityGroupCreateRequest{Token: "b4", MemberType: "device", Selector: &sel}},
		{"garbage mode", &EntityGroupCreateRequest{Token: "b5", MemberType: "device", MembershipMode: &garbage}},
		{"dynamic with non-lowerable selector", &EntityGroupCreateRequest{Token: "b6", MemberType: "device", MembershipMode: &dynamic, Selector: &badSel}},
	}
	for _, tc := range cases {
		if _, err := api.CreateEntityGroup(ctx, tc.req); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

// A STATIC group has no selector, and the update must say so rather than store one.
//
// 🔴 THIS TEST USED TO ASSERT THREE REFUSALS AND NOW ASSERTS ONE, WHICH IS NOT A LOSS OF
// COVERAGE. The other two — a member-family change and a membership-mode change — were
// refusals of requests EntityGroupUpdateRequest can no longer express: both fields are
// identity, so they are not in the input at all. The claim moved from "the model refuses
// it" to "the schema has nowhere to write it", which is checked where it is now true, in
// graphql/partial_update_wire_test.go. A guard's test can pass while the guard is
// unreachable; an absent field cannot.
func TestUpdateEntityGroup_AStaticGroupRefusesASelector(t *testing.T) {
	api := newGroupTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "g1", MemberType: "device"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The counterweight, first: an ordinary presentation edit still works, so the refusal
	// below is not bought by refusing everything.
	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Fleet North"),
	}); err != nil {
		t.Fatalf("presentation update rejected: %v", err)
	}

	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Selector: dcgraphql.OptionalStringOf(`attr["x"] == 1`),
	}); err == nil {
		t.Error("a static group accepted a selector, which nothing would ever evaluate")
	}
}

// A DYNAMIC group's selector cannot be CLEARED. It is the only thing that decides who is
// in the group, so a group without one matches nothing and there is no request that gets
// it back to a valid state — which is why null is refused rather than honoured.
func TestUpdateEntityGroup_ADynamicSelectorCannotBeCleared(t *testing.T) {
	api := newGroupTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	mode := string(MembershipDynamic)
	seeded := `attr["climate"] == "arid"`
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "g1", MemberType: "device", MembershipMode: &mode, Selector: &seeded,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Selector: dcgraphql.ClearedString(),
	}); err == nil {
		t.Fatal("a dynamic group's selector was cleared, leaving a group that matches nothing")
	}
	rows, err := api.EntityGroupsByToken(ctx, []string{"g1"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("reload: %v (%d rows)", err, len(rows))
	}
	if rows[0].Selector.String != seeded {
		t.Fatalf("the refused clear still moved the selector to %q", rows[0].Selector.String)
	}

	// The counterweight: a real selector edit still takes.
	replaced := `attr["climate"] == "humid"`
	if _, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Selector: dcgraphql.OptionalStringOf(replaced),
	}); err != nil {
		t.Fatalf("a selector edit was refused: %v", err)
	}
	rows, err = api.EntityGroupsByToken(ctx, []string{"g1"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("reload: %v (%d rows)", err, len(rows))
	}
	if rows[0].Selector.String != replaced {
		t.Fatalf("selector = %q, want %q", rows[0].Selector.String, replaced)
	}
}

// EntityGroups filters by member family and membership mode.
func TestEntityGroups_Filters(t *testing.T) {
	api := newGroupTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, s := range []struct{ token, mt string }{
		{"d1", "device"}, {"d2", "device"}, {"a1", "area"},
	} {
		if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: s.token, MemberType: s.mt}); err != nil {
			t.Fatalf("seed %s: %v", s.token, err)
		}
	}
	device := "device"
	res, err := api.EntityGroups(ctx, EntityGroupSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		MemberType: &device,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	assert.Len(t, res.Results, 2, "expected two device groups")
	for _, g := range res.Results {
		assert.Equal(t, "device", g.MemberType)
	}
}
