// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/stretchr/testify/require"
)

// captureOwnership records every ownership fact, and what the context it was handed
// carried. Facts reach it only through the ownership drain, so every read of what it holds
// drains first (when api is set): the facts a write queued are then the facts it records,
// in the order the drain publishes them. fail, when set, refuses every publish with it.
type captureOwnership struct {
	mu     sync.Mutex
	facts  []DeviceOwnershipChangedEvent
	claims int // facts published under a context that carried the caller's claims
	tenant []string
	fail   error
	// attempts counts every publish asked of it, refused or not.
	attempts int

	api *Api
	t   *testing.T
}

func (c *captureOwnership) PublishDeviceOwnershipChanged(ctx context.Context, e *DeviceOwnershipChangedEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	if c.fail != nil {
		return c.fail
	}
	c.facts = append(c.facts, *e)
	if _, ok := auth.ClaimsFromContext(ctx); ok {
		c.claims++
	}
	t, _ := core.TenantFromContext(ctx)
	c.tenant = append(c.tenant, t)
	return nil
}

// drain publishes everything queued, as the service's drain pass would.
func (c *captureOwnership) drain() {
	if c.api == nil {
		return
	}
	c.t.Helper()
	_, err := c.api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(c.t, err, "draining the queued ownership facts")
}

func (c *captureOwnership) count() int {
	c.drain()
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.facts)
}

func (c *captureOwnership) last() DeviceOwnershipChangedEvent {
	c.drain()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.facts[len(c.facts)-1]
}

// ownershipFixture is the hierarchy test store with a device, two customers of one type,
// and the built-in assignment type, under an OPERATOR's request context (claims attached,
// as the transports attach them).
func ownershipFixture(t *testing.T) (*Api, context.Context, *captureOwnership) {
	t.Helper()
	api, tenantCtx := hierarchyTestApi(t)
	ctx := auth.WithClaims(tenantCtx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Authorities: []string{"*"}})
	// The customer-delete cascade's tables, which the hierarchy store does not carry.
	require.NoError(t, api.RDB.Database.AutoMigrate(&EntityAttribute{}, &Alarm{}, &EntityGroup{}, &EntityGroupMembership{}))
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")
	ct := &CustomerType{}
	ct.Token = "operator"
	require.NoError(t, api.RDB.DB(ctx).Create(ct).Error)
	for _, c := range []string{"acme-mining", "globex-mining"} {
		_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: c, CustomerTypeToken: "operator"})
		require.NoError(t, err)
	}
	_, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err)
	pub := &captureOwnership{api: api, t: t}
	api.DeviceOwnershipPublisher = pub
	return api, ctx, pub
}

// 🔑 EVERY WRITER OF A DEVICE'S EDGES PUBLISHES THE DEVICE'S OWNERSHIP, enumerated from
// the tree rather than from memory: single create and remove, bulk create and remove, a
// claim redeemed, a claimed device's claim reopened (a resale), a relationship type's
// tracked flag flipped either way, and the deletion of the customer a device was assigned
// to. Each step asserts exactly one new fact and the device's COMPLETE anchor set after
// the write. A rename of the type changes nobody's ownership and publishes nothing.
func TestEveryEdgeWriterPublishesTheDevicesOwnership(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	assigned := AssignmentRelationshipType

	step := func(what string, want []string, write func() error) {
		t.Helper()
		before := pub.count()
		require.NoError(t, write(), what)
		require.Equal(t, before+1, pub.count(), "%s: want exactly one ownership fact", what)
		got := pub.last()
		require.Equal(t, "dozer-01", got.DeviceToken, what)
		require.ElementsMatch(t, want, got.Anchors, "%s: the fact's anchors", what)
		require.NotNil(t, got.Anchors, "%s: an unowned device is an empty set, never nil", what)
	}

	step("a single edge created", []string{"customer:acme-mining"}, func() error {
		_, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "e-acme",
			SourceType: "device", Source: "dozer-01", TargetType: "customer", Target: "acme-mining",
			RelationshipType: assigned})
		return err
	})
	step("edges created in bulk", []string{"customer:acme-mining", "customer:globex-mining"}, func() error {
		_, err := api.CreateEntityRelationships(ctx, []*EntityRelationshipCreateRequest{{Token: "e-globex",
			SourceType: "device", Source: "dozer-01", TargetType: "customer", Target: "globex-mining",
			RelationshipType: assigned}})
		return err
	})
	step("a single edge removed", []string{"customer:globex-mining"}, func() error {
		_, err := api.RemoveEntityRelationship(ctx, "e-acme")
		return err
	})
	step("edges removed in bulk", []string{}, func() error {
		_, err := api.RemoveEntityRelationships(ctx, []string{"e-globex"})
		return err
	})

	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret"})
	require.NoError(t, err)
	step("a claim redeemed", []string{"customer:acme-mining"}, func() error {
		_, err := api.ClaimDevice(ctx, &DeviceClaimRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret",
			CustomerToken: "acme-mining"}, time.Now())
		return err
	})
	step("a claimed device's claim reopened", []string{}, func() error {
		_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "again"})
		return err
	})
	step("the reopened claim redeemed by another customer", []string{"customer:globex-mining"}, func() error {
		_, err := api.ClaimDevice(ctx, &DeviceClaimRequest{DeviceToken: "dozer-01", ClaimSecret: "again",
			CustomerToken: "globex-mining"}, time.Now())
		return err
	})

	untracked, tracked := false, true
	step("the assignment type un-tracked", []string{}, func() error {
		_, err := api.UpdateEntityRelationshipType(ctx, AssignmentRelationshipType,
			&EntityRelationshipTypeUpdateRequest{Tracked: optBool(untracked)})
		return err
	})
	step("the assignment type tracked again", []string{"customer:globex-mining"}, func() error {
		_, err := api.UpdateEntityRelationshipType(ctx, AssignmentRelationshipType,
			&EntityRelationshipTypeUpdateRequest{Tracked: optBool(tracked)})
		return err
	})
	before := pub.count()
	_, err = api.UpdateEntityRelationshipType(ctx, AssignmentRelationshipType,
		&EntityRelationshipTypeUpdateRequest{Name: optString("Owned by")})
	require.NoError(t, err)
	require.Equal(t, before, pub.count(), "a rename changed nobody's ownership but published a fact")

	step("the owning customer deleted", []string{}, func() error {
		_, err := api.DeleteCustomer(ctx, "globex-mining")
		return err
	})

	// 🔴 Confused deputy, both directions: every fact went out under the service's own
	// context — the write's tenant and none of the caller's claims.
	require.Zero(t, pub.claims, "an ownership fact was published under the caller's claims")
	for _, tn := range pub.tenant {
		require.Equal(t, "acme", tn)
	}
}

// TestClaimDefaultsToAssigned: a claim redeemed with no relationship type writes the
// built-in TRACKED assignment edge, so the device becomes visible to the claiming
// customer's members. Before, an empty type matched no relationship type and the
// redemption failed with a miss.
func TestClaimDefaultsToAssigned(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	_, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret"})
	require.NoError(t, err)
	edge, err := api.ClaimDevice(ctx, &DeviceClaimRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret",
		CustomerToken: "acme-mining"}, time.Now())
	require.NoError(t, err, "a claim with no relationship type was refused")
	require.Equal(t, AssignmentRelationshipType, edge.RelationshipType.Token)
	require.True(t, edge.RelationshipType.Tracked, "the default claim edge is not tracked, so it is not ownership")

	member := scopedMemberCtx("acme", "customer:acme-mining")
	found, err := api.DevicesByToken(member, []string{"dozer-01"})
	require.NoError(t, err)
	require.Len(t, found, 1, "the claiming customer's member cannot see the device it claimed")
	other, err := api.DevicesByToken(scopedMemberCtx("acme", "customer:globex-mining"), []string{"dozer-01"})
	require.NoError(t, err)
	require.Empty(t, other, "another customer's member sees the claimed device")
}

// scopedMemberCtx is a member of tenant limited to anchors, as the transports build it.
func scopedMemberCtx(tenant string, anchors ...string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: tenant, ScopeAnchors: anchors,
		Authorities: []string{string(auth.DeviceRead)}})
}

// 🔴 THE TENANT-KEYED CACHES NEVER ANSWER A SCOPED CALLER. Every cache in front of this
// API is keyed by tenant and nothing narrower; a hit filled by an unscoped read would hand
// a customer's member another customer's device past every predicate. The scoped read
// goes to the store instead, where the device rule filters it — and a model with no rule
// (the tracked relationships) is refused rather than served from memory.
func TestAScopedReadNeverHitsTheTenantKeyedCache(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	_, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "e-globex",
		SourceType: "device", Source: "dozer-01", TargetType: "customer", Target: "globex-mining",
		RelationshipType: AssignmentRelationshipType})
	require.NoError(t, err)
	kv := func() *messaging.Cache { return msgtest.NewMemoryKV().NewCache() }
	capi := NewCachedApi(api, &Caches{DeviceByToken: kv(), RelationshipsBySource: kv()})

	// The ingest path (tenant, no claims) fills both caches.
	tenantOnly := core.WithTenant(context.Background(), "acme")
	filled, err := capi.DevicesByToken(tenantOnly, []string{"dozer-01"})
	require.NoError(t, err)
	require.Len(t, filled, 1, "the fixture's device was not found to fill the cache")
	_, err = capi.TrackedRelationshipsForDevice(tenantOnly, filled[0].ID)
	require.NoError(t, err)

	acme := scopedMemberCtx("acme", "customer:acme-mining")
	got, err := capi.DevicesByToken(acme, []string{"dozer-01"})
	require.NoError(t, err)
	require.Empty(t, got, "acme's member was served globex's device from the tenant-keyed cache")

	_, err = capi.TrackedRelationshipsForDevice(acme, filled[0].ID)
	require.True(t, errors.Is(err, auth.ErrScopeUnsupported),
		"a scoped read of the relationship graph was answered from the cache: %v", err)

	// Control: globex's member reads its own device, through the store.
	got, err = capi.DevicesByToken(scopedMemberCtx("acme", "customer:globex-mining"), []string{"dozer-01"})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func optBool(v bool) dcgraphql.OptionalBool { return dcgraphql.OptionalBool{Set: true, Value: &v} }

func optString(v string) dcgraphql.OptionalString {
	return dcgraphql.OptionalString{Set: true, Value: &v}
}
