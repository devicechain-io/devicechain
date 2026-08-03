// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deletedTenants builds a lifecycle gate refusing exactly the named tenants, and records
// what it was asked about.
func deletedTenants(asked *[]string, deleted ...string) func(string) bool {
	set := map[string]bool{}
	for _, d := range deleted {
		set[d] = true
	}
	return func(tenant string) bool {
		*asked = append(*asked, tenant)
		return set[tenant]
	}
}

// 🔴 ADR-077: a deleted tenant must not AUTO-REGISTER a device.
//
// This is the worst of the three things the gate stops. A cache miss under an
// auto-register policy issues createDevice under a SERVICE token naming the purged
// tenant — re-seeding the entity root every other area's rows cascade from, after those
// areas may already have swept. It is the platform resurrecting a tenant it is erasing,
// on behalf of a device nobody has revoked.
func TestResolveRefusesToAutoRegisterIntoADeletedTenant(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupMiss(), nil }}
	var asked []string
	r := NewRegistrar(gql, "url", "sp-", deletedTenants(&asked, "acme"))

	tok, outcome, err := r.Resolve(context.Background(), "acme", "plant-a/node-3",
		IngestPolicy{AutoRegister: true, DeviceTypeToken: "sp-node"})

	require.NoError(t, err, "a purge does not un-happen; refusing must not be retryable")
	assert.Equal(t, "", tok)
	assert.Equal(t, ResolveTenantGone, outcome)
	assert.Equal(t, 0, gql.creates, "a deleted tenant must never have a device created in it")
	assert.Equal(t, 0, gql.lookups, "the gate should refuse before device-management is touched")
	assert.Equal(t, []string{"acme"}, asked)
}

// 🔴 The gate runs BEFORE the cache, which is the part device deletion alone cannot do.
//
// The cache never expires and never re-asks, so a device already resolved keeps
// resolving from memory forever — deleting its row in device-management does not stop
// it. A gate placed after the cache read would leave exactly the hot, high-rate devices
// ingesting, which are the ones that matter.
func TestResolveRefusesEvenAnAlreadyCachedDevice(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	deleted := false
	r := NewRegistrar(gql, "url", "sp-", func(string) bool { return deleted })

	// Warm the cache while the tenant is live.
	tok, outcome, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	require.Equal(t, "dev-1", tok)
	require.Equal(t, ResolveFound, outcome)

	deleted = true
	tok, outcome, err = r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "", tok)
	assert.Equal(t, ResolveTenantGone, outcome, "a cached device must not survive its tenant's deletion")
}

// And the entry is EVICTED, not merely bypassed. Once ADR-077 releases a purged token, a
// surviving entry stops being useless and becomes a cross-tenant hazard: a successor
// tenant reusing the token would resolve its own external ids to the predecessor's
// device tokens. Bypassing the cache while leaving it populated would leave that live.
func TestResolveEvictsADeletedTenantsCachedEntries(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	deleted := false
	r := NewRegistrar(gql, "url", "sp-", func(string) bool { return deleted })

	_, _, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	_, ok := r.cache.get("acme", "g/n")
	require.True(t, ok, "the cache should be warm before the delete, or this test proves nothing")

	deleted = true
	_, _, err = r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)

	_, ok = r.cache.get("acme", "g/n")
	assert.False(t, ok, "the purged tenant's cached tokens must be dropped, not just skipped")
}

// One tenant's deletion must not evict another's. The nested-map layout makes this
// nearly free, but "nearly free" is not "true" — an invalidate written against the old
// flat key layout with a sloppy prefix match would take "acme-corp" out with "acme".
func TestInvalidateTenantLeavesOtherTenantsAlone(t *testing.T) {
	c := newTokenCache()
	c.put("acme", "g/n", "dev-1")
	c.put("acme-corp", "g/n", "dev-2")
	c.put("other", "g/n", "dev-3")

	c.invalidateTenant("acme")

	_, ok := c.get("acme", "g/n")
	assert.False(t, ok)
	tok, ok := c.get("acme-corp", "g/n")
	assert.True(t, ok, "a tenant whose token merely starts with the purged one must survive")
	assert.Equal(t, "dev-2", tok)
	_, ok = c.get("other", "g/n")
	assert.True(t, ok)
}

// Two properties, and worth separating in your head because they fail to different
// mutations:
//
//   - the NEGATIVE CONTROL (the token and outcome assertions): with the gate wired and
//     answering "live", resolution is untouched. A gate that refused everything would
//     satisfy every refusal test above and take all ingest down; this is what catches it.
//   - the WIRING assertion (asked): the gate is consulted, and about the INGESTING
//     tenant rather than some other string in scope. This one does fail alongside the
//     tests above when the gate is removed entirely, which is fine — it is pinning that
//     the call happens, not that its answer is respected.
func TestResolveIsUnchangedForALiveTenantWithTheGateWired(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	var asked []string
	r := NewRegistrar(gql, "url", "sp-", deletedTenants(&asked, "someone-else"))

	tok, outcome, err := r.Resolve(context.Background(), "acme", "g/n", IngestPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "dev-1", tok)
	assert.Equal(t, ResolveFound, outcome)
	assert.Equal(t, []string{"acme"}, asked, "the gate must be asked about the ingesting tenant")
}

// The ingester must not EMIT a refused outcome. This is the seam the Ingestible()
// predicate exists to protect: the drop is decided in Resolve, but the decision only
// matters if the caller acts on it — and the caller's old shape (`== ResolveDropped`)
// would have fallen through to Emit for an outcome added later.
func TestIngestDoesNotEmitForADeletedTenant(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	w := &fakeWriter{}
	ing := NewIngester(
		NewRegistrar(gql, "url", "sp-", func(string) bool { return true }),
		NewEmitter(w, fixedNow, "sp", false), IngestMetrics{})

	err := ing.Ingest(context.Background(), "acme", IngestPolicy{AutoRegister: true},
		"g/n", []Sample{{Name: "temp", Value: 21.5}})

	require.NoError(t, err, "a refused message is fully handled, not a retryable failure")
	assert.Empty(t, w.msgs, "no event may be written for a tenant that has been deleted")
}

// The drop is counted and explained as what it IS.
//
// Both refusals stop ingest, so a metric that lumps them together still "works" — which
// is exactly why this needs pinning rather than trusting: the two mean opposite things to
// whoever is looking. UnknownDropped is a source misconfiguration to go fix;
// TenantGoneDropped is the platform correctly refusing to write into a tenant being
// erased. Counting a purge under the unknown-device counter (and logging it as
// "auto-registration is off") sends an operator debugging a silent fleet to change a
// setting that is not the cause.
func TestADeletedTenantsDropIsCountedApartFromAnUnknownDevice(t *testing.T) {
	deleted := false
	gqlMiss := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupMiss(), nil }}
	m, read := newIngestMetrics()
	ing := NewIngester(
		NewRegistrar(gqlMiss, "url", "sp-", func(string) bool { return deleted }),
		NewEmitter(&fakeWriter{}, fixedNow, "sp", false), m)

	// An unknown device with auto-register off: the pre-existing meaning.
	require.NoError(t, ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"},
		"g/n", []Sample{{Name: "t", Value: 1, Time: 1}}))
	assert.Equal(t, float64(1), read("dropped"))
	assert.Equal(t, float64(0), read("tenantGone"))

	// The same call for a deleted tenant lands on the other counter, and does NOT
	// increment the first — a shared counter would show 2 here and be indistinguishable.
	deleted = true
	require.NoError(t, ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "s"},
		"g/n", []Sample{{Name: "t", Value: 1, Time: 1}}))
	assert.Equal(t, float64(1), read("tenantGone"))
	assert.Equal(t, float64(1), read("dropped"), "a purge must not be counted as an unknown device")
}
