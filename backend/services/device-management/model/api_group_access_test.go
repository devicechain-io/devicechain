// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// accessFixture is the scoping store with the relationship-type table the ownership reads
// need, a capturing ownership publisher, and two devices: d-arid (climate arid) and d-humid
// (climate humid). The group "arid" selects climate == "arid" and is published at v1, but is
// NOT an access scope yet.
func accessFixture(t *testing.T) (*Api, context.Context, *captureOwnership) {
	t.Helper()
	api, ctx := newScopingTestApi(t)
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(ctx)).AutoMigrate(&EntityRelationshipType{}))
	pub := &captureOwnership{}
	api.DeviceOwnershipPublisher = pub
	seedDevice(t, api, ctx, "d-arid")
	seedDevice(t, api, ctx, "d-humid")
	setSharedClimate(t, api, ctx, "d-arid", "arid")
	setSharedClimate(t, api, ctx, "d-humid", "humid")
	publishAridGroup(t, api, ctx, "arid")
	return api, ctx, pub
}

// groupMember is a member of tenant acme whose access is limited to the given anchors.
func groupMember(anchors ...string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), "acme"), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme", ScopeAnchors: anchors,
		Authorities: []string{string(auth.DeviceRead)}})
}

// visibleDevices lists the device tokens ctx can read.
func visibleDevices(t *testing.T, api *Api, ctx context.Context) []string {
	t.Helper()
	found, err := api.Devices(ctx, DeviceSearchCriteria{})
	require.NoError(t, err)
	out := []string{}
	for _, d := range found.Results {
		out = append(out, d.Token)
	}
	sort.Strings(out)
	return out
}

// lastFactFor returns the newest ownership fact for device, and whether there was one at or
// after index from.
func lastFactFor(pub *captureOwnership, from int, device string) (DeviceOwnershipChangedEvent, bool) {
	pub.mu.Lock()
	defer pub.mu.Unlock()
	for i := len(pub.facts) - 1; i >= from; i-- {
		if pub.facts[i].DeviceToken == device {
			return pub.facts[i], true
		}
	}
	return DeviceOwnershipChangedEvent{}, false
}

// 🔑 THE SLICE'S PROMISE: a member limited to a group sees the devices in it, and a device
// whose attributes move it into the group becomes visible WITHOUT ANYONE EDITING THE MEMBER —
// and stops being visible when they move it out. Each move is announced with the device's
// new anchors, so the live view follows at once.
func TestDeviceEnteringGroupBecomesVisibleWithoutEditingTheMember(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	member := groupMember("group:arid")

	// Before the group is an access scope, naming it reaches nothing: a member may be
	// limited to any group token, and only an access scope confers anything.
	require.Empty(t, visibleDevices(t, api, member), "a group that is not an access scope reached a device")

	from := pub.count()
	g, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	require.True(t, g.IsAccessScope())
	require.Equal(t, []string{"d-arid"}, visibleDevices(t, api, member))
	fact, ok := lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "enabling announced no member")
	require.Equal(t, []string{"group:arid"}, fact.Anchors)
	_, ok = lastFactFor(pub, from, "d-humid")
	require.False(t, ok, "a device outside the group was announced")

	// The device moves INTO the group by its attributes alone.
	from = pub.count()
	setSharedClimate(t, api, ctx, "d-humid", "arid")
	require.Equal(t, []string{"d-arid", "d-humid"}, visibleDevices(t, api, member))
	fact, ok = lastFactFor(pub, from, "d-humid")
	require.True(t, ok, "a device entering the group was not announced")
	require.Equal(t, []string{"group:arid"}, fact.Anchors)

	// A write that leaves membership as it was announces nothing.
	from = pub.count()
	setSharedClimate(t, api, ctx, "d-humid", "arid")
	require.Equal(t, from, pub.count(), "a write that changed no membership was announced")

	// And OUT of it.
	from = pub.count()
	setSharedClimate(t, api, ctx, "d-arid", "humid")
	require.Equal(t, []string{"d-humid"}, visibleDevices(t, api, member))
	fact, ok = lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "a device leaving the group was not announced")
	require.Empty(t, fact.Anchors)
	require.NotNil(t, fact.Anchors)

	// Negative controls: the operator sees both; a member limited to another group, or to a
	// customer, sees neither.
	require.Equal(t, []string{"d-arid", "d-humid"}, visibleDevices(t, api, ctx))
	require.Empty(t, visibleDevices(t, api, groupMember("group:other")))
	require.Empty(t, visibleDevices(t, api, groupMember("customer:arid")))

	// Stopping the group being an access scope takes the devices away, announced.
	from = pub.count()
	_, err = api.SetEntityGroupAccessScope(ctx, "arid", false)
	require.NoError(t, err)
	require.Empty(t, visibleDevices(t, api, member))
	fact, ok = lastFactFor(pub, from, "d-humid")
	require.True(t, ok, "disabling announced no member")
	require.Empty(t, fact.Anchors)
}

// The device's group anchors are part of its anchors wherever another record reads them: the
// alarm stamp at raise, the batch read other services stamp commands and update assignments
// from, and the membership read the event resolver stamps events from (whose AccessScope
// marker is the published version only).
func TestAccessScopeAnchorsAreTheDevicesAnchorsEverywhere(t *testing.T) {
	api, ctx, _ := accessFixture(t)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	arid, humid := deviceId(t, api, ctx, "d-arid"), deviceId(t, api, ctx, "d-humid")

	stamp, err := api.deviceScopeAnchors(ctx, arid)
	require.NoError(t, err)
	require.Equal(t, []string{"group:arid"}, []string(stamp))
	stamp, err = api.deviceScopeAnchors(ctx, humid)
	require.NoError(t, err)
	require.Empty(t, stamp)

	batch, err := api.DeviceScopeAnchors(ctx, []string{"d-arid", "d-humid"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"d-arid": {"group:arid"}, "d-humid": {}}, batch)

	ms, err := api.MembershipsForEntity(ctx, "device", arid)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.True(t, ms[0].AccessScope)

	// A published v2 moves the access scope to v2; v1's rows are torn down, since no rule
	// pins them. v2 selects humid.
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroup{}).Where("token = ?", "arid").
		Update("selector", `attr["climate"] == "humid"`).Error)
	_, err = api.PublishEntityGroup(ctx, "arid", nil, nil, "tester")
	require.NoError(t, err)
	require.Equal(t, []string{"d-humid"}, visibleDevices(t, api, groupMember("group:arid")))
	batch, err = api.DeviceScopeAnchors(ctx, []string{"d-arid", "d-humid"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"d-arid": {}, "d-humid": {"group:arid"}}, batch)
	var v1 int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).Where("selector_version = 1").Count(&v1).Error)
	require.Zero(t, v1, "the old version stayed materialized with nothing referencing it")

	// Rolling back moves it back.
	_, err = api.RollbackEntityGroup(ctx, "arid", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"d-arid"}, visibleDevices(t, api, groupMember("group:arid")))
}

// The read-model rows of a group@version are shared by a rule pinning it and an access scope
// on it, so each holds them: stopping the access scope keeps the rows a live rule reads, and
// the access scope's rows stay while only it references them.
func TestARulePinAndAnAccessScopeShareTheReadModel(t *testing.T) {
	api, ctx, _ := accessFixture(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&DetectionRuleScopeRef{DeviceProfileId: 1, RuleToken: "r",
		GroupToken: "arid", GroupVersion: 1}).Error)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	_, err = api.SetEntityGroupAccessScope(ctx, "arid", false)
	require.NoError(t, err)
	rows := func() int64 {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).Count(&n).Error)
		return n
	}
	require.Equal(t, int64(1), rows(), "disabling the access scope tore down rows a rule pins")
	ms, err := api.MembershipsForEntity(ctx, "device", deviceId(t, api, ctx, "d-arid"))
	require.NoError(t, err)
	require.False(t, ms[0].AccessScope, "a rule-only row still read as an access scope")
}

// Only a published dynamic group of devices can be an access scope, and a tenant may have
// at most MaxAccessScopeGroups — the bound on how many group anchors one event can carry.
func TestAnAccessScopeIsABoundedSetOfPublishedDynamicDeviceGroups(t *testing.T) {
	api, ctx, _ := accessFixture(t)
	invalid := func(err error) bool {
		cls, ok := integrity.Refused(err)
		return ok && cls == integrity.ClassInvalid && errors.Is(err, ErrAccessScopeIneligible)
	}

	static := "static"
	_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "listed", MemberType: "device", MembershipMode: &static})
	require.NoError(t, err)
	_, err = api.SetEntityGroupAccessScope(ctx, "listed", true)
	require.True(t, invalid(err), "a static group: %v", err)

	createDynamicGroup(t, api, ctx, "draft-only", `attr["climate"] == "arid"`)
	_, err = api.SetEntityGroupAccessScope(ctx, "draft-only", true)
	require.True(t, invalid(err), "an unpublished group: %v", err)

	dynamic := "dynamic"
	sel := `attr["climate"] == "arid"`
	_, err = api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "areas", MemberType: "area", MembershipMode: &dynamic, Selector: &sel})
	require.NoError(t, err)
	_, err = api.PublishEntityGroup(ctx, "areas", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.SetEntityGroupAccessScope(ctx, "areas", true)
	require.True(t, invalid(err), "a group of areas: %v", err)

	for i := 0; i < MaxAccessScopeGroups; i++ {
		token := fmt.Sprintf("region-%d", i)
		publishAridGroup(t, api, ctx, token)
		_, err := api.SetEntityGroupAccessScope(ctx, token, true)
		require.NoError(t, err)
	}
	_, err = api.SetEntityGroupAccessScope(ctx, "arid", true)
	var lim *limit.Error
	require.True(t, errors.As(err, &lim), "one past the ceiling: %v", err)
	ok, err := api.RDB.DB(ctx).Model(&EntityGroup{}).Where("token = ? AND access_scope_at IS NOT NULL", "arid").Rows()
	require.NoError(t, err)
	require.False(t, ok.Next(), "the refused group was marked anyway")
	require.NoError(t, ok.Close())

	// Setting what is already set is a no-op, not a second place against the ceiling.
	_, err = api.SetEntityGroupAccessScope(ctx, "region-0", true)
	require.NoError(t, err)
}

// A deleted group's token is retired, as a deleted customer's is: "group:<token>" names it on
// member scopes and on records that outlive it. Deleting an access scope announces its
// members with the anchor gone.
func TestADeletedGroupsTokenIsRetired(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)

	// A live duplicate is still an already-exists.
	_, err = api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "arid", MemberType: "device"})
	require.True(t, conflict.Is(err), "a live duplicate: %v", err)

	from := pub.count()
	deleted, err := api.DeleteEntityGroup(ctx, "arid")
	require.NoError(t, err)
	require.True(t, deleted)
	fact, ok := lastFactFor(pub, from, "d-arid")
	require.True(t, ok, "deleting an access scope announced no member")
	require.Empty(t, fact.Anchors)

	_, err = api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "arid", MemberType: "device"})
	require.True(t, errors.Is(err, ErrGroupTokenRetired), "a create at a deleted group's token: %v", err)
	require.False(t, conflict.Is(err), "a retired token must never read as an already-exists")
	cls, ok := integrity.Refused(err)
	require.True(t, ok && cls == integrity.ClassInvalid)

	// Another tenant's ledger is its own.
	other := core.WithTenant(context.Background(), "globex")
	_, err = api.CreateEntityGroup(other, &EntityGroupCreateRequest{Token: "arid", MemberType: "device"})
	require.NoError(t, err)
}

func deviceId(t *testing.T, api *Api, ctx context.Context, token string) uint {
	t.Helper()
	found, err := api.DevicesByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return found[0].ID
}

// A rule pinning an OLD version beside the access scope on the published one: the old
// version's rows stay (the rule reads them) but confer nothing — a device in the group only
// at the pinned version is not visible to the group's member, carries no group anchor on
// its records, and its membership row does not read as an access scope. Only the published
// version is the access scope.
func TestARulePinOnAnOldVersionConfersNoAccess(t *testing.T) {
	api, ctx, _ := accessFixture(t)
	require.NoError(t, api.RDB.DB(ctx).Create(&DetectionRuleScopeRef{DeviceProfileId: 1, RuleToken: "r",
		GroupToken: "arid", GroupVersion: 1}).Error)
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	// v2 selects humid; v1 (pinned by the rule) keeps selecting arid.
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroup{}).Where("token = ?", "arid").
		Update("selector", `attr["climate"] == "humid"`).Error)
	_, err = api.PublishEntityGroup(ctx, "arid", nil, nil, "tester")
	require.NoError(t, err)
	arid, humid := deviceId(t, api, ctx, "d-arid"), deviceId(t, api, ctx, "d-humid")

	var v1 int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).
		Where("selector_version = 1 AND entity_id = ?", arid).Count(&v1).Error)
	require.Equal(t, int64(1), v1, "the rule's pinned rows were torn down")

	require.Equal(t, []string{"d-humid"}, visibleDevices(t, api, groupMember("group:arid")))
	batch, err := api.DeviceScopeAnchors(ctx, []string{"d-arid", "d-humid"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"d-arid": {}, "d-humid": {"group:arid"}}, batch)
	stamp, err := api.deviceScopeAnchors(ctx, arid)
	require.NoError(t, err)
	require.Empty(t, stamp)
	ms, err := api.MembershipsForEntity(ctx, "device", arid)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.False(t, ms[0].AccessScope, "a row at the rule's pinned version read as an access scope")
	ms, err = api.MembershipsForEntity(ctx, "device", humid)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.True(t, ms[0].AccessScope)
}

// The search criterion a scope picker uses: accessScope true lists the access scopes,
// false the rest, and unset both.
func TestEntityGroupsCanBeListedByAccessScope(t *testing.T) {
	api, ctx, _ := accessFixture(t)
	publishAridGroup(t, api, ctx, "other")
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	tokens := func(flag *bool) []string {
		found, err := api.EntityGroups(ctx, EntityGroupSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, AccessScope: flag})
		require.NoError(t, err)
		out := []string{}
		for _, g := range found.Results {
			out = append(out, g.Token)
		}
		sort.Strings(out)
		return out
	}
	yes, no := true, false
	require.Equal(t, []string{"arid"}, tokens(&yes))
	require.Equal(t, []string{"other"}, tokens(&no))
	require.Equal(t, []string{"arid", "other"}, tokens(nil))
}
