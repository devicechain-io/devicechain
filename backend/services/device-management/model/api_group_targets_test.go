// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
)

// seedPublishedDeviceGroup creates a dynamic device group over the "climate" facet and
// publishes it, returning the group and its first frozen version.
func seedPublishedDeviceGroup(t *testing.T, api *Api, ctx context.Context,
	token, selector string) (*EntityGroup, int32) {
	t.Helper()
	dynamic := string(MembershipDynamic)
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: token, MemberType: "device", MembershipMode: &dynamic, Selector: &selector,
	})
	if err != nil {
		t.Fatalf("create dynamic group: %v", err)
	}
	version, err := api.PublishEntityGroup(ctx, token, nil, nil, "test")
	if err != nil {
		t.Fatalf("publish group: %v", err)
	}
	return g, version.Version
}

// leaveTheSelector rewrites a device's climate facet so it no longer satisfies the
// "arid" selector — the ordinary way a device leaves a dynamic group mid-walk (an
// attribute write), as opposed to being decommissioned.
func leaveTheSelector(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	humid := "humid"
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: token,
		Scope: string(AttributeScopeShared), AttrKey: "climate",
		ValueType: string(AttributeValueString), Value: &humid,
	}); err != nil {
		t.Fatalf("move %s out of the selector: %v", token, err)
	}
}

// walkAll pages the whole group through the keyset walk and returns every token seen.
func walkAll(t *testing.T, api *Api, ctx context.Context, token string, limit int) []string {
	t.Helper()
	seen := make([]string, 0)
	cursor := uint(0)
	for i := 0; i < 100; i++ { // a loop bound, so a cursor bug fails instead of hanging
		page, err := api.ResolveDeviceGroupTargets(ctx, token, nil, cursor, limit)
		if err != nil {
			t.Fatalf("resolve page: %v", err)
		}
		if page.Rejected {
			t.Fatalf("unexpected rejection: %s — %s", page.Code, page.Reason)
		}
		seen = append(seen, page.DeviceTokens...)
		if page.NextCursor == 0 {
			return seen
		}
		cursor = page.NextCursor
	}
	t.Fatal("the keyset walk did not terminate within 100 pages")
	return nil
}

// 🔴 TestTheKeysetWalkDoesNotSkipAMemberWhenAnEarlierOneLeaves is the reason this whole
// resolution path exists rather than reusing ResolveGroupMembers.
//
// The scenario is ordinary, not adversarial: a batch pages through a 10,000-device group
// while the fleet keeps living — a device is decommissioned, or an attribute write moves
// one out of the selector. Under OFFSET paging the departure shifts every later row down a
// slot, so a device that satisfied the selector the entire time is never visited: it
// receives no command, appears in no refusal list, and nothing anywhere records that it was
// skipped. That is an unauditable hole in a physical fleet actuation.
//
// The test walks page 1, removes a device from it, then walks page 2 and requires the
// device that would have been shifted over to still be visited.
func TestTheKeysetWalkDoesNotSkipAMemberWhenAnEarlierOneLeaves(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	for i := 1; i <= 4; i++ {
		seedDeviceWithClimate(t, api, ctx, fmt.Sprintf("d%d", i), "arid")
	}
	seedPublishedDeviceGroup(t, api, ctx, "arid", `attr["climate"] == "arid"`)

	first, err := api.ResolveDeviceGroupTargets(ctx, "arid", nil, 0, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assert.Equal(t, []string{"d1", "d2"}, first.DeviceTokens, "first page")
	assert.NotZero(t, first.NextCursor, "a full page must carry a cursor")

	// d1 leaves the set between the two pages.
	leaveTheSelector(t, api, ctx, "d1")

	second, err := api.ResolveDeviceGroupTargets(ctx, "arid", nil, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	assert.Equal(t, []string{"d3", "d4"}, second.DeviceTokens,
		"the keyset walk must still visit d3 — under offset paging d1's departure shifts "+
			"d3 into the slot the first page already consumed, and it is never seen")
}

// TestOffsetPagingSkipsThatMember is the NEGATIVE CONTROL for the test above, and without
// it that test proves nothing: "the walk visited every device" is also what a walk with no
// bug at all would produce, on a mechanism that never had the bug.
//
// This drives the SAME scenario through ResolveGroupMembers' offset paging and asserts the
// skip actually happens — so the keyset test is measured against a mechanism demonstrated
// to fail, not against an assumption that it would.
//
// ⚠️ If this ever fails, offset paging was fixed upstream and the premise above should be
// revisited — it is not a test of desired behaviour, it is a record of why the keyset walk
// was written.
func TestOffsetPagingSkipsThatMember(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	for i := 1; i <= 4; i++ {
		seedDeviceWithClimate(t, api, ctx, fmt.Sprintf("d%d", i), "arid")
	}
	g, _ := seedPublishedDeviceGroup(t, api, ctx, "arid", `attr["climate"] == "arid"`)

	first, err := api.ResolveGroupMembers(ctx, g, rdb.Pagination{PageNumber: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assert.Len(t, first.Results, 2, "first offset page")

	leaveTheSelector(t, api, ctx, "d1")

	second, err := api.ResolveGroupMembers(ctx, g, rdb.Pagination{PageNumber: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	seen := []string{}
	for _, m := range first.Results {
		seen = append(seen, m.Token)
	}
	for _, m := range second.Results {
		seen = append(seen, m.Token)
	}
	assert.NotContains(t, seen, "d3",
		"this test exists to demonstrate the offset-paging skip the keyset walk avoids; "+
			"if d3 IS seen, offset paging no longer has the defect and the keyset test's "+
			"premise needs revisiting")
}

// 🔴 TestACommandCannotTargetANonDeviceGroup. Groups collect four entity families and
// tokens are unique per TABLE, so an asset and a device may both be called "pump-1" in one
// tenant. Without the family check an asset group's tokens flow into device validation:
// mostly refused as DEVICE_NOT_FOUND — a category error reported as a per-device condition
// — but wherever a token collides with a real device, ACCEPTED, commanding hardware nobody
// targeted.
func TestACommandCannotTargetANonDeviceGroup(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "north-plant", MemberType: entity.TypeArea.String(),
	}); err != nil {
		t.Fatalf("create area group: %v", err)
	}

	page, err := api.ResolveDeviceGroupTargets(ctx, "north-plant", nil, 0, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !page.Rejected {
		t.Fatal("an AREA group was accepted as a command target — its member tokens would " +
			"be treated as device tokens, and any that collide with a real device token " +
			"would command hardware the operator never named")
	}
	assert.Equal(t, RejectGroupNotADeviceGroup, page.Code)
}

// TestAnUnpublishedDynamicGroupIsRefused. Such a group has only its mutable draft selector,
// so a batch record could never answer "what did this group mean when it fired?" — the
// ambiguity version-pinning exists to remove. Refusing it keeps the frozen-selector
// guarantee unconditional.
func TestAnUnpublishedDynamicGroupIsRefused(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d1", "arid")
	dynamic := string(MembershipDynamic)
	sel := `attr["climate"] == "arid"`
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "draft-only", MemberType: "device", MembershipMode: &dynamic, Selector: &sel,
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	page, err := api.ResolveDeviceGroupTargets(ctx, "draft-only", nil, 0, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !page.Rejected {
		t.Fatal("an unpublished dynamic group resolved — the batch would have fired at a " +
			"draft selector that may never have been reviewed")
	}
	assert.Equal(t, RejectGroupNotPublished, page.Code)
}

// 🔑 TestEditingTheDraftDoesNotChangeWhatABatchTargets is the version-pinning doctrine
// test: a rule scopes to {group}@{version} precisely so its target set cannot drift under a
// later selector edit, and a physical fleet actuation deserves at least that.
//
// It edits the group's draft to a selector matching a DIFFERENT device and requires the
// resolution to keep matching the published one.
func TestEditingTheDraftDoesNotChangeWhatABatchTargets(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d-arid", "arid")
	seedDeviceWithClimate(t, api, ctx, "d-humid", "humid")
	seedPublishedDeviceGroup(t, api, ctx, "target", `attr["climate"] == "arid"`)

	before := walkAll(t, api, ctx, "target", 100)
	assert.Equal(t, []string{"d-arid"}, before, "the published selector matches the arid device")

	// Someone edits the draft to mean something else entirely, and does not publish.
	dynamic := string(MembershipDynamic)
	edited := `attr["climate"] == "humid"`
	if _, err := api.UpdateEntityGroup(ctx, "target", &EntityGroupCreateRequest{
		Token: "target", MemberType: "device", MembershipMode: &dynamic, Selector: &edited,
	}); err != nil {
		t.Fatalf("edit draft: %v", err)
	}

	after := walkAll(t, api, ctx, "target", 100)
	assert.Equal(t, []string{"d-arid"}, after,
		"the batch target followed the mutable DRAFT selector; a fleet actuation must fire "+
			"at the frozen published version, not at whatever someone last typed")
}

// TestAStaticGroupPagesItsEdgesAndRefusesAVersion. Static groups are never versioned, so a
// named version cannot be honoured — and ignoring it would silently answer a different
// question than the one asked.
func TestAStaticGroupPagesItsEdgesAndRefusesAVersion(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d1", "")
	seedDeviceWithClimate(t, api, ctx, "d2", "")
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "fleet", MemberType: "device",
	}); err != nil {
		t.Fatalf("create static group: %v", err)
	}
	if _, err := api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{{
		Token: "e1", RelationshipType: MembershipRelationshipType,
		SourceType: string(entity.TypeGroup), Source: "fleet",
		TargetType: entity.TypeDevice.String(), Target: "d1",
	}}); err != nil {
		t.Fatalf("add member edge: %v", err)
	}

	assert.Equal(t, []string{"d1"}, walkAll(t, api, ctx, "fleet", 100), "static members")

	version := int32(1)
	page, err := api.ResolveDeviceGroupTargets(ctx, "fleet", &version, 0, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !page.Rejected {
		t.Fatal("a version was accepted for a static group, which has none — the caller " +
			"asked for a pinned target set and silently got an unpinned one")
	}
	assert.Equal(t, RejectGroupNotVersioned, page.Code)
}

// TestAMissingGroupOrVersionIsADecidedRefusal, not an error: both are the caller naming
// something that does not exist, which they can fix, and neither says anything about the
// platform's health.
func TestAMissingGroupOrVersionIsADecidedRefusal(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, ctx, "d1", "arid")
	seedPublishedDeviceGroup(t, api, ctx, "arid", `attr["climate"] == "arid"`)

	missing, err := api.ResolveDeviceGroupTargets(ctx, "no-such-group", nil, 0, 100)
	if err != nil {
		t.Fatalf("a missing group must not error: %v", err)
	}
	assert.True(t, missing.Rejected)
	assert.Equal(t, RejectGroupNotFound, missing.Code)

	future := int32(99)
	stale, err := api.ResolveDeviceGroupTargets(ctx, "arid", &future, 0, 100)
	if err != nil {
		t.Fatalf("a missing version must not error: %v", err)
	}
	assert.True(t, stale.Rejected)
	assert.Equal(t, RejectGroupVersionNotFound, stale.Code)
}

// TestTheCursorStopsAtTheEnd. A short page means exhausted, so the cursor zeroes: carrying
// one would cost every batch a guaranteed-empty extra query. The walk must also terminate
// when the group divides exactly by the page size, which is the case a naive
// "stop when the page is short" reader gets wrong by one query, not by a hang.
func TestTheCursorStopsAtTheEnd(t *testing.T) {
	api, ctx := newGroupMemberTestApi(t)
	for i := 1; i <= 4; i++ {
		seedDeviceWithClimate(t, api, ctx, fmt.Sprintf("d%d", i), "arid")
	}
	seedPublishedDeviceGroup(t, api, ctx, "arid", `attr["climate"] == "arid"`)

	assert.Equal(t, []string{"d1", "d2", "d3", "d4"}, walkAll(t, api, ctx, "arid", 2),
		"an exactly-divisible group walks completely")
	assert.Equal(t, []string{"d1", "d2", "d3", "d4"}, walkAll(t, api, ctx, "arid", 3),
		"an unevenly-divided group walks completely")

	short, err := api.ResolveDeviceGroupTargets(ctx, "arid", nil, 0, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assert.Zero(t, short.NextCursor, "a short page is the last page")
}

// 🔴 TestABatchCannotTargetAnotherTenantsGroup. Group tokens are unique per tenant, so two
// tenants can each own a "fleet". A lost scope here would not error — it would resolve
// another tenant's devices into this tenant's fleet write.
func TestABatchCannotTargetAnotherTenantsGroup(t *testing.T) {
	api, acme := newGroupMemberTestApi(t)
	seedDeviceWithClimate(t, api, acme, "d1", "arid")
	seedPublishedDeviceGroup(t, api, acme, "fleet", `attr["climate"] == "arid"`)

	intruder := core.WithTenant(context.Background(), "intruder")
	page, err := api.ResolveDeviceGroupTargets(intruder, "fleet", nil, 0, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !page.Rejected {
		t.Fatalf("another tenant's group resolved to %v — the lookup is not tenant-scoped",
			page.DeviceTokens)
	}
	assert.Equal(t, RejectGroupNotFound, page.Code)
}
