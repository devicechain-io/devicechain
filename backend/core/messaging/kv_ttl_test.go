// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	nats "github.com/nats-io/nats.go"
)

type ttlCountingContext struct {
	nats.JetStreamContext
	updates int
}

func (js *ttlCountingContext) UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.updates++
	return js.JetStreamContext.UpdateStream(cfg, opts...)
}

func TestKeyValueStoreReconcilesTTL(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		initial, requested, want time.Duration
		changeBytes              bool
		updates                  int
	}{
		{"raise", time.Minute, 2 * time.Minute, 2 * time.Minute, false, 1},
		{"lower", 2 * time.Minute, time.Minute, time.Minute, false, 1},
		{"unchanged", time.Minute, time.Minute, time.Minute, false, 0},
		{"zero cannot unbound", time.Minute, 0, time.Minute, false, 0},
		{"bytes and TTL", time.Minute, 2 * time.Minute, 2 * time.Minute, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nmgr, cleanup := newTestManager(t)
			defer cleanup()
			const bucket = "ttl_reconcile"
			if _, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, bucket, tc.initial); err != nil {
				t.Fatal(err)
			}
			if tc.changeBytes {
				nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.KvStateMaxBytes = 4 << 20
			}
			js := &ttlCountingContext{JetStreamContext: nmgr.js}
			nmgr.js = js
			if _, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, bucket, tc.requested); err != nil {
				t.Fatal(err)
			}
			info, err := js.StreamInfo(kvStreamPrefix + bucket)
			if err != nil {
				t.Fatal(err)
			}
			if info.Config.MaxAge != tc.want {
				t.Errorf("MaxAge = %s, want %s", info.Config.MaxAge, tc.want)
			}
			if want := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.KvMaxBytesFor(kv.BucketRefreshTokens); info.Config.MaxBytes != want {
				t.Errorf("MaxBytes = %d, want %d", info.Config.MaxBytes, want)
			}
			if js.updates != tc.updates {
				t.Errorf("UpdateStream calls = %d, want %d", js.updates, tc.updates)
			}
		})
	}
}

func TestKeyValueStoreTTLDoesNotRevertReplicaLift(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()
	const bucket = "ttl_replica_lift"
	if _, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, bucket, time.Minute); err != nil {
		t.Fatal(err)
	}
	waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 1)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.KvStateMaxBytes = 4 << 20
	if _, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, bucket, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	info := waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 3)
	if info.Config.MaxAge != 2*time.Minute || info.Config.MaxBytes != 4<<20 {
		t.Fatalf("reconcile lost TTL or ceiling: MaxAge=%s MaxBytes=%d", info.Config.MaxAge, info.Config.MaxBytes)
	}
}

// TestVerifyOnlyOpenIgnoresTTL pins that a deployed area, which only verifies, neither changes
// nor refuses a bucket whose lifetime differs from the one it asked for: openers legitimately
// ask for different lifetimes (the lock bucket's 5s and 30s, a cache's configured TTL), and a
// refusal would stop the pod.
func TestVerifyOnlyOpenIgnoresTTL(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	const bucket = "ttl_verify"
	if _, err := nmgr.KeyValueStore(kv.BucketLocks, bucket, time.Minute); err != nil {
		t.Fatal(err)
	}
	nmgr.Microservice.Deployed = true
	nmgr.Microservice.FunctionalArea = "event-sources"
	js := &ttlCountingContext{JetStreamContext: nmgr.js}
	nmgr.js = js
	if _, err := nmgr.KeyValueStore(kv.BucketLocks, bucket, 2*time.Minute); err != nil {
		t.Fatalf("verify-only open refused a bucket for its lifetime: %v", err)
	}
	if js.updates != 0 {
		t.Fatal("verify-only path updated the stream")
	}
}

// TestTTLLeftAloneWhereItIsNotTheBucketsOwn pins the buckets whose lifetime an open must not
// change: the lease bucket (the fencing window), the lock bucket outside the administrator
// (per-call lifetimes would flap it and a lowered MaxAge expires held locks), and an identity
// bucket under the administrator (user-management's configured lifetime owns it).
func TestTTLLeftAloneWhereItIsNotTheBucketsOwn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logical string
		admin   bool
	}{
		{"lease bucket", kv.BucketLeases, false},
		{"lock bucket, service", kv.BucketLocks, false},
		{"refresh bucket, administrator", kv.BucketRefreshTokens, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nmgr, cleanup := newTestManager(t)
			defer cleanup()
			bucket := "ttl_alone_" + map[bool]string{false: "svc", true: "adm"}[tc.admin]
			if _, err := nmgr.KeyValueStore(tc.logical, bucket, 2*time.Minute); err != nil {
				t.Fatal(err)
			}
			nmgr.admin = tc.admin
			if _, err := nmgr.KeyValueStore(tc.logical, bucket, time.Minute); err != nil {
				t.Fatal(err)
			}
			info, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
			if err != nil {
				t.Fatal(err)
			}
			if info.Config.MaxAge != 2*time.Minute {
				t.Fatalf("MaxAge = %s, want the bucket's own 2m kept", info.Config.MaxAge)
			}
		})
	}
	// The administrator does apply the declared lifetime of the lock bucket.
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	const bucket = "ttl_lock_admin"
	if _, err := nmgr.KeyValueStore(kv.BucketLocks, bucket, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	nmgr.admin = true
	if _, err := nmgr.KeyValueStore(kv.BucketLocks, bucket, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	info, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.MaxAge != 30*time.Second {
		t.Fatalf("administrator left the lock bucket at %s, want 30s", info.Config.MaxAge)
	}
}
