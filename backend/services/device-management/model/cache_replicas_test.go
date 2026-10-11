// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// Two device-management replicas share one key-value store per cache and one database;
// each has its own process memory in front of the store. These tests pin what the
// in-process tier costs a replica that did NOT make a change WHEN NO EVICTION MESSAGE
// REACHES IT: the bound that holds if a message is lost, which is the whole time to live.
//
// They build their caches over an in-memory store (NewCacheOver), which has no broker to
// broadcast on. In the service every cache is built WithCrossReplicaEviction, and a change
// reaches the other replicas at once; cache_eviction_wiring_test.go in the service package
// proves that, over a real broker, with these same changes.

// 🔑 A device deleted through replica A is still resolved by replica B's cached lookup
// until B's copy expires. That bound is documented. It is also why the raise-alarm consumer
// resolves the device through the plain Api, which reads the database and finds it gone:
// its drop of an edge for a deleted device must hold at once, on every replica.
func TestAnotherReplicaResolvesADeletedDeviceUntilItsCopyExpires(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	// One store per cache, shared by both replicas; each replica builds its own Caches
	// over them, so each has its own process memory, as two pods do.
	stores := make([]*msgtest.MemoryKV, 5)
	for i := range stores {
		stores[i] = msgtest.NewMemoryKV()
	}
	replica := func() *CachedApi {
		return NewCachedApi(api, &Caches{
			DeviceByToken:           stores[0].NewCache(),
			RelationshipsBySource:   stores[1].NewCache(),
			ProfileResolutionByType: stores[2].NewCache(),
			MembershipsByEntity:     stores[3].NewCache(),
			ScopedGroupsExist:       stores[4].NewCache(),
		})
	}
	a, b := replica(), replica()
	api.CacheEvictor = a

	dev := &Device{}
	dev.Token = "dev-1"
	if err := api.RDB.DB(ctx).Create(dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if got, err := b.DevicesByToken(ctx, []string{"dev-1"}); err != nil || len(got) != 1 {
		t.Fatalf("replica B's warm read = (%d devices, %v), want the device", len(got), err)
	}

	if deleted, err := api.DeleteDevice(ctx, "dev-1"); err != nil || !deleted {
		t.Fatalf("delete through replica A = (%v, %v)", deleted, err)
	}

	if got, err := a.DevicesByToken(ctx, []string{"dev-1"}); err != nil || len(got) != 0 {
		t.Errorf("replica A, which deleted it, still resolves the device: (%d, %v)", len(got), err)
	}
	if got, err := b.DevicesByToken(ctx, []string{"dev-1"}); err != nil || len(got) != 1 || got[0].ID != dev.ID {
		t.Errorf("replica B within its in-process TTL = (%d devices, %v), want the device it held: the "+
			"documented bound", len(got), err)
	}
	if got, err := api.DevicesByToken(ctx, []string{"dev-1"}); err != nil || len(got) != 0 {
		t.Errorf("the plain Api still resolves the deleted device: (%d, %v)", len(got), err)
	}
}

// 🔑 A publish through replica A reaches events replica B resolves once B's copy of the
// type's resolution expires, not at once. The version, the rule scope and the scope
// memberships sit in three caches whose copies expire independently, which is why the docs
// say a publish that changes a rule's group scope can, for those few seconds on another
// replica, be evaluated against the previous scope.
func TestAnotherReplicaSeesAPublishOnceItsCopyExpires(t *testing.T) {
	r := newProfileResolutionRig(t)
	const ttl = 2 * time.Second
	b := NewCachedApi(r.api, &Caches{ProfileResolutionByType: r.kv.NewCache(messaging.WithLocalTTL(ttl))})
	readB := func() *ProfileResolution {
		t.Helper()
		res, err := b.ProfileResolutionByDeviceType(r.ctx, r.typeId)
		if err != nil {
			t.Fatalf("replica B read: %v", err)
		}
		return res
	}
	start := time.Now()
	if got := readB().Scope.ProfileVersionToken; got != "p@1" {
		t.Fatalf("replica B's warm read = %q, want p@1", got)
	}

	r.editUnitAndPublish(t, "K") // through replica A, the Api's evictor
	if got := r.read(t); got.Scope.ProfileVersionToken != "p@2" {
		t.Errorf("replica A, which published, reads %q, want p@2", got.Scope.ProfileVersionToken)
	}
	if got := readB(); got.Scope.ProfileVersionToken != "p@1" {
		if time.Since(start) < ttl {
			t.Errorf("replica B inside its in-process TTL reads %q, want p@1 (the documented bound)",
				got.Scope.ProfileVersionToken)
		}
	}

	time.Sleep(ttl - time.Since(start) + 50*time.Millisecond)
	if got := readB(); got.Scope.ProfileVersionToken != "p@2" || unitOf(t, got) != "K" {
		t.Errorf("replica B after its copy expired reads %q with unit %q, want p@2 with K",
			got.Scope.ProfileVersionToken, unitOf(t, got))
	}
}
