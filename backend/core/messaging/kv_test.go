// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// newTestManager spins up an in-process NATS server with JetStream enabled and
// returns a minimal NatsManager wired to it, plus a cleanup func. It is a
// white-box harness for the KV-backed Cache and DistributedLock: only the js
// context and Microservice identity fields they read are populated.
func newTestManager(t *testing.T) (*NatsManager, func()) {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		t.Fatalf("connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		srv.Shutdown()
		t.Fatalf("jetstream: %v", err)
	}
	nmgr := &NatsManager{
		Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"},
		js:           js,
	}
	return nmgr, func() {
		nc.Close()
		srv.Shutdown()
	}
}

type cachedValue struct {
	Token string
	Count int
}

func TestCacheSetGetDelete(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	ctx := context.Background()

	c, err := nmgr.NewCache("devices", time.Minute)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Miss on an absent key returns (false, nil) — not an error.
	var got cachedValue
	found, err := c.Get(ctx, "tenant-a|dev-1", &got)
	if err != nil || found {
		t.Fatalf("expected clean miss, got found=%v err=%v", found, err)
	}

	// A key containing characters outside the NATS KV key charset ("|") must
	// round-trip thanks to base64url encoding.
	want := cachedValue{Token: "dev-1", Count: 7}
	if err := c.Set(ctx, "tenant-a|dev-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	found, err = c.Get(ctx, "tenant-a|dev-1", &got)
	if err != nil || !found {
		t.Fatalf("expected hit, got found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}

	// Delete evicts; a subsequent Get misses cleanly. A second Delete tolerates
	// the miss.
	if err := c.Delete(ctx, "tenant-a|dev-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	found, _ = c.Get(ctx, "tenant-a|dev-1", &got)
	if found {
		t.Fatal("expected miss after delete")
	}
	if err := c.Delete(ctx, "tenant-a|dev-1"); err != nil {
		t.Fatalf("Delete on missing key should tolerate miss: %v", err)
	}
}

func TestDistributedLockMutualExclusion(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// TTL well above the contention window so the holder's lock cannot auto-expire
	// mid-test; mutual exclusion is then purely a function of the held key.
	lock, err := nmgr.NewDistributedLock(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLock: %v", err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lock.WithLock(context.Background(), "area", func(context.Context) error {
			close(held)
			<-release // keep holding until the test says to let go
			return nil
		})
	}()
	<-held // the goroutine now holds the lock

	// A competing acquire while the lock is held must NOT run its logic. A short
	// deadline makes it give up fast (WithLock honors ctx.Done() during backoff).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	ran := false
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		ran = true
		return nil
	}); err == nil {
		t.Fatal("expected the competing acquire to fail while the lock was held")
	}
	if ran {
		t.Fatal("competing critical section must not have run while the lock was held")
	}

	// Release the holder, then the lock can be acquired again and the logic runs.
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
	reran := false
	if err := lock.WithLock(context.Background(), "area", func(context.Context) error {
		reran = true
		return nil
	}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if !reran {
		t.Fatal("expected logic to run after the lock was released")
	}
}

func TestDistributedLockReleasesOnError(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	ctx := context.Background()

	lock, err := nmgr.NewDistributedLock(5 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLock: %v", err)
	}

	// A critical section that returns an error must still release the lock, so a
	// subsequent acquire succeeds.
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		return context.Canceled
	}); err == nil {
		t.Fatal("expected the logic error to propagate")
	}

	ran := false
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("acquire after error release: %v", err)
	}
	if !ran {
		t.Fatal("lock was not released after the guarded logic errored")
	}
}

// The reconcile path is the whole reason KeyValueStore does not just pass
// MaxBytes to CreateKeyValue: the create path runs only when a bucket is absent,
// so without this a ceiling would reach fresh installs and NOTHING else. Every
// cluster that had already run would keep its unbounded bucket and look healthy.
func TestKeyValueStoreBoundsAnAlreadyUnboundedBucket(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	const bucket = "preexisting_bucket"
	// Create it the way a previous release did: no MaxBytes at all, which JetStream
	// stores as unlimited.
	if _, err := nmgr.js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Minute}); err != nil {
		t.Fatalf("seed unbounded bucket: %v", err)
	}
	info, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("seed StreamInfo: %v", err)
	}
	if info.Config.MaxBytes > 0 {
		t.Fatalf("precondition failed: seeded bucket already bounded at %d B; this test "+
			"can no longer distinguish a working reconcile from a lucky default", info.Config.MaxBytes)
	}

	// dc_locks is a State bucket, so it takes the state ceiling.
	if _, err := nmgr.KeyValueStore(kv.BucketLocks, bucket, time.Minute); err != nil {
		t.Fatalf("KeyValueStore: %v", err)
	}

	info, err = nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("StreamInfo after reconcile: %v", err)
	}
	if want := config.DefaultKvStateMaxBytes; info.Config.MaxBytes != want {
		t.Errorf("existing bucket left at MaxBytes %d, want %d: an already-created "+
			"bucket never receives the ceiling, so only fresh installs are bounded",
			info.Config.MaxBytes, want)
	}
}

// A newly created bucket must carry its ceiling too, and a cache bucket must get
// the SMALLER one — the tier split is the thing that keeps the fleet-scaling
// buckets from eating the headroom the budget leaves.
func TestKeyValueStoreBoundsNewBucketsByTier(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	for _, tc := range []struct {
		name    string
		logical string
		bucket  string
		want    int64
	}{
		{"cache tier", kv.BucketDeviceByToken, "new_cache_bucket", config.DefaultKvCacheMaxBytes},
		{"state tier", kv.BucketRefreshTokens, "new_state_bucket", config.DefaultKvStateMaxBytes},
		// An unregistered bucket takes the STATE ceiling, not the cache one: the
		// tiers rank by what a refused write costs, so the unknown case has to
		// assume the expensive one.
		{"unregistered", "not-in-the-inventory", "new_unknown_bucket", config.DefaultKvStateMaxBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := nmgr.KeyValueStore(tc.logical, tc.bucket, time.Minute); err != nil {
				t.Fatalf("KeyValueStore: %v", err)
			}
			info, err := nmgr.js.StreamInfo(kvStreamPrefix + tc.bucket)
			if err != nil {
				t.Fatalf("StreamInfo: %v", err)
			}
			if info.Config.MaxBytes != tc.want {
				t.Errorf("MaxBytes = %d, want %d", info.Config.MaxBytes, tc.want)
			}
		})
	}
}

// Bounding a KV bucket is only safe because JetStream refuses the write instead
// of evicting an older entry. That is a property of nats.go's bucket creation,
// not of anything this repo controls, and the tier classification in kv.All is
// built entirely on it — so it is pinned here rather than assumed. If a future
// nats.go created buckets with DiscardOld, every cache bucket would start
// silently dropping live entries under its new ceiling.
func TestKeyValueBucketsDiscardNewSoACeilingRefusesRatherThanEvicts(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	if _, err := nmgr.KeyValueStore(kv.BucketDeviceByToken, "discard_policy_bucket", time.Minute); err != nil {
		t.Fatalf("KeyValueStore: %v", err)
	}
	info, err := nmgr.js.StreamInfo(kvStreamPrefix + "discard_policy_bucket")
	if err != nil {
		t.Fatalf("StreamInfo: %v", err)
	}
	if info.Config.Discard != nats.DiscardNew {
		t.Errorf("KV bucket discard policy = %v, want DiscardNew: a full bucket would "+
			"EVICT live entries rather than refuse the write, which invalidates the "+
			"tier reasoning in core/kv", info.Config.Discard)
	}
}
