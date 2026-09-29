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
	capi, _, ctx, deviceId := newCachedApiForTest(t)
	stores := map[string]*msgtest.MemoryKV{}
	build := func(name string) *messaging.Cache {
		kv := msgtest.NewMemoryKV()
		stores[name] = kv
		return kv.NewCache(messaging.WithoutLocalCache())
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
