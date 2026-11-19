// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go/jetstream"
)

// unreachableKV is a cache bucket behind a broker this service cannot reach: every call
// waits until its context gives up, as a JetStream request does when nothing answers.
type unreachableKV struct{ calls atomic.Int64 }

func (u *unreachableKV) wait(ctx context.Context) error {
	u.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func (u *unreachableKV) Get(ctx context.Context, _ string) (jetstream.KeyValueEntry, error) {
	return nil, u.wait(ctx)
}

func (u *unreachableKV) Put(ctx context.Context, _ string, _ []byte) (uint64, error) {
	return 0, u.wait(ctx)
}

func (u *unreachableKV) Delete(ctx context.Context, _ string, _ ...jetstream.KVDeleteOpt) error {
	return u.wait(ctx)
}

// 🔑 A DEVICE DELETE MADE WHILE THE SERVICE IS CUT OFF FROM THE BROKER ANSWERS ONCE IT HAS
// COMMITTED. Every cache bucket is unreachable, so each eviction the delete owes waits out
// the cache's own delete timeout (5 s per entry, several entries). The delete must still
// answer true promptly, and its deletion fact must be in the outbox, which is what delivers
// it once the broker is back. Before the bound, the caller was held for the sum of the
// evictions and, behind an ingress, saw a gateway timeout for a delete that had happened.
func TestDeleteDeviceAnswersPromptlyWhenTheCacheBucketsAreUnreachable(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	kv := &unreachableKV{}
	cached := NewCachedApi(api, &Caches{
		DeviceByToken:           messaging.NewCacheOver(kv),
		RelationshipsBySource:   messaging.NewCacheOver(kv),
		ProfileResolutionByType: messaging.NewCacheOver(kv),
		MembershipsByEntity:     messaging.NewCacheOver(kv),
		ScopedGroupsExist:       messaging.NewCacheOver(kv),
	})
	cached.evictionWait = 100 * time.Millisecond
	api.CacheEvictor = cached

	dev := &Device{}
	dev.Token = "dev-1"
	if err := api.RDB.DB(ctx).Create(dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	start := time.Now()
	deleted, err := api.DeleteDevice(ctx, "dev-1")
	took := time.Since(start)
	if err != nil || !deleted {
		t.Fatalf("DeleteDevice = (%v, %v), want (true, nil): the delete committed", deleted, err)
	}
	// Two waits of 100 ms (the entity-delete and the membership evictions) and the
	// transaction; a single bucket delete alone would take 5 s.
	if took > 2*time.Second {
		t.Fatalf("DeleteDevice took %s with the broker unreachable, want it to answer within the eviction "+
			"wait: it waited on the cache evictions", took)
	}
	if kv.calls.Load() == 0 {
		t.Fatal("no eviction reached the cache bucket: the test did not exercise the unreachable broker")
	}

	var gone int64
	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", "dev-1").Count(&gone).Error; err != nil || gone != 0 {
		t.Fatalf("device rows after the delete = (%d, %v), want 0", gone, err)
	}
	var facts []PendingEntityDeletion
	if err := api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ?", "device", dev.ID).Find(&facts).Error; err != nil {
		t.Fatalf("read the deletion outbox: %v", err)
	}
	if len(facts) != 1 || facts[0].TenantId != "acme" {
		t.Fatalf("deletion outbox rows = %+v, want one for acme: the fact must wait in the outbox for the broker", facts)
	}
}

// The bound does not abandon an eviction that finishes in time: on a reachable bucket the
// replica that made the change drops its own copy before the change is answered.
func TestEvictAfterCommitWaitsForAnEvictionThatFinishes(t *testing.T) {
	capi := &CachedApi{evictionWait: 5 * time.Second}
	var ran atomic.Bool
	capi.evictAfterCommit(context.Background(), "test", func(context.Context) {
		time.Sleep(20 * time.Millisecond)
		ran.Store(true)
	})
	if !ran.Load() {
		t.Fatal("evictAfterCommit returned before an eviction that finished inside the wait")
	}
}

// An eviction carries on after its caller has gone: it runs on a context the caller's
// cancellation does not reach.
func TestEvictAfterCommitOutlivesItsCaller(t *testing.T) {
	capi := &CachedApi{evictionWait: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	finished := make(chan error, 1)
	capi.evictAfterCommit(ctx, "test", func(ectx context.Context) {
		<-release
		finished <- ectx.Err()
	})
	cancel()
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("the eviction's context was cancelled with its caller's: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the eviction never finished after its caller returned")
	}
}
