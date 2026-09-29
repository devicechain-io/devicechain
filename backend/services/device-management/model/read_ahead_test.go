// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/stretchr/testify/require"
)

// readAheadStores gives the CachedApi from newCachedApiForTest all five caches, each over a
// counting store with the in-process tier off, so every cache read reaches its store.
func readAheadStores(t *testing.T) (*CachedApi, map[string]*msgtest.MemoryKV, context.Context, uint) {
	t.Helper()
	return readAheadStoresWith(t, messaging.WithoutLocalCache())
}

// readAheadStoresWith is readAheadStores with each cache built with opts: with none, the
// in-process tier is on, as in production.
func readAheadStoresWith(t *testing.T, opts ...messaging.CacheOption) (*CachedApi, map[string]*msgtest.MemoryKV, context.Context, uint) {
	t.Helper()
	capi, _, ctx, deviceId := newCachedApiForTest(t)
	stores := map[string]*msgtest.MemoryKV{}
	build := func(name string) *messaging.Cache {
		kv := msgtest.NewMemoryKV()
		stores[name] = kv
		return kv.NewCache(opts...)
	}
	capi.caches = &Caches{
		DeviceByToken:           build("device"),
		RelationshipsBySource:   build("relationships"),
		ProfileResolutionByType: build("profile"),
		MembershipsByEntity:     build("memberships"),
		ScopedGroupsExist:       build("scoped"),
	}
	return capi, stores, ctx, deviceId
}

func totalGets(stores map[string]*msgtest.MemoryKV) int {
	n := 0
	for _, kv := range stores {
		n += kv.Gets
	}
	return n
}

// With no tenant in the context the CachedApi reads no cache at all, so there is nothing
// to read ahead: the api comes back unchanged and no store is asked anything.
func TestNothingIsReadAheadWithoutATenant(t *testing.T) {
	capi, stores, _, deviceId := readAheadStores(t)
	got := ReadAheadForEvent(context.Background(), capi, &Device{})
	if got != DeviceManagementApi(capi) {
		t.Errorf("ReadAheadForEvent without a tenant returned %T, want the CachedApi itself", got)
	}
	ReadMembershipsAhead(context.Background(), got, []MembershipTarget{{"device", deviceId}, {"area", 1}})
	if n := totalGets(stores); n != 0 {
		t.Errorf("reading ahead without a tenant made %d cache reads, want 0", n)
	}
}

// Any api but the CachedApi itself comes back unchanged: there are no caches to read.
func TestNothingIsReadAheadForAnotherApi(t *testing.T) {
	capi, stores, ctx, _ := readAheadStores(t)
	plain := DeviceManagementApi(capi.Api)
	if got := ReadAheadForEvent(ctx, plain, &Device{}); got != plain {
		t.Errorf("ReadAheadForEvent over the plain Api returned %T, want it unchanged", got)
	}
	if ReadsAheadConcurrently(plain) {
		t.Error("ReadsAheadConcurrently(plain Api) = true, want false")
	}
	if !ReadsAheadConcurrently(capi) {
		t.Error("ReadsAheadConcurrently(CachedApi) = false, want true")
	}
	if n := totalGets(stores); n != 0 {
		t.Errorf("reading ahead over another api made %d cache reads, want 0", n)
	}
}

// A read made ahead answers one lookup. The second lookup of the same key in the same event
// asks the cache again, exactly as it did before anything was read ahead.
func TestAReadMadeAheadAnswersOneLookup(t *testing.T) {
	capi, stores, ctx, deviceId := readAheadStores(t)
	// Warm the relationships bucket so the read ahead is a hit.
	_, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)
	stores["relationships"].Gets = 0

	device := &Device{}
	device.ID = deviceId
	api := ReadAheadForEvent(ctx, capi, device)
	first, err := api.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)
	require.Len(t, first.Results, 1)
	if got := stores["relationships"].Gets; got != 1 {
		t.Fatalf("relationship cache reads after the read-ahead and one lookup = %d, want 1", got)
	}
	second, err := api.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)
	require.Len(t, second.Results, 1)
	if got := stores["relationships"].Gets; got != 2 {
		t.Errorf("relationship cache reads after a second lookup = %d, want 2", got)
	}
}

// What was read ahead under one tenant is never served to a lookup under another: the key
// carries the tenant, so the other tenant's lookup goes through the CachedApi, and reads
// its own (empty) set.
func TestAReadMadeAheadIsNotServedToAnotherTenant(t *testing.T) {
	capi, _, ctx, deviceId := readAheadStores(t)
	_, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)

	device := &Device{}
	device.ID = deviceId
	api := ReadAheadForEvent(ctx, capi, device)
	other := core.WithTenant(context.Background(), "other")
	got, err := api.TrackedRelationshipsForDevice(other, deviceId)
	require.NoError(t, err)
	if len(got.Results) != 0 {
		t.Errorf("tenant other read %d of acme's relationships through the read-ahead, want 0", len(got.Results))
	}
	// And acme's own lookup is still answered from what was read.
	own, err := api.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)
	require.Len(t, own.Results, 1)
}

// 🔑 A READ PROCESS MEMORY CAN ANSWER NEVER GOES PAST IT. readAll asks memory for every key
// on the caller's goroutine first, and hands only the rest to the bucket, at the same time.
// Skipping that step still gets every answer right — a Get with the in-process tier on is
// answered from memory too, so no count of bucket Gets moves — but it starts goroutines
// for an event whose reads memory holds, which made such an event cost several times what
// it did (BenchmarkWarmResolve). So the assertion is on the eventReads' own count of reads
// made past memory, which only that step keeps at 0.
func TestReadsMemoryHoldsNeverLeaveIt(t *testing.T) {
	capi, stores, ctx, deviceId := readAheadStoresWith(t) // the in-process tier on
	device := &Device{DeviceTypeId: 7}
	device.ID = deviceId
	targets := []MembershipTarget{{"device", deviceId}, {"area", 1}}

	// The counterweight first: with memory empty, all five reads go past it. Without this, a
	// counter stuck at 0 would read as the verdict below.
	cold, ok := ReadAheadForEvent(ctx, capi, device).(*eventReads)
	require.True(t, ok, "ReadAheadForEvent over the CachedApi did not read ahead")
	ReadMembershipsAhead(ctx, cold, targets)
	if cold.pastMemory != 5 {
		t.Fatalf("with memory empty, %d reads went past it, want 5 (profile, relationships, "+
			"scoped groups, two memberships)", cold.pastMemory)
	}

	// Every key through its cache, which keeps it in this process's memory as well as the
	// bucket.
	tenant := "acme"
	require.NoError(t, capi.caches.ProfileResolutionByType.Set(ctx, profileResolutionByTypeKey(tenant, 7),
		&ProfileResolution{Metrics: []ResolvedMetric{{MetricKey: "temp"}}}))
	require.NoError(t, capi.caches.RelationshipsBySource.Set(ctx, relationshipsBySourceKey(tenant, deviceId),
		&EntityRelationshipSearchResults{}))
	require.NoError(t, capi.caches.ScopedGroupsExist.Set(ctx, tenant, true))
	for _, tg := range targets {
		require.NoError(t, capi.caches.MembershipsByEntity.Set(ctx,
			membershipsByEntityKey(tenant, tg.Type, tg.Id), []GroupMembership{}))
	}
	before := totalGets(stores)

	warm := ReadAheadForEvent(ctx, capi, device).(*eventReads)
	ReadMembershipsAhead(ctx, warm, targets)
	if warm.pastMemory != 0 {
		t.Errorf("with every key in memory, %d reads went past it, want 0: readAll did not ask "+
			"memory first", warm.pastMemory)
	}
	if got := totalGets(stores) - before; got != 0 {
		t.Errorf("with every key in memory, the bucket was asked %d times, want 0", got)
	}
	// And each lookup is answered from what memory held. This rig has no profile or group
	// tables, so an answer from the database would be an error.
	res, err := warm.ProfileResolutionByDeviceType(ctx, 7)
	require.NoError(t, err)
	require.Len(t, res.Metrics, 1)
	scoped, err := warm.AnyScopedGroups(ctx)
	require.NoError(t, err)
	require.True(t, scoped)
	for _, tg := range targets {
		_, err := warm.MembershipsForEntity(ctx, tg.Type, tg.Id)
		require.NoError(t, err)
	}
}
