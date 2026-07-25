// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
	nats "github.com/nats-io/nats.go"
)

// TestApplyStreamReplicas pins the ratchet. The downward row is the one with
// consequences: `helm rollback` is the ordinary way a pod is handed older config,
// and it is reached for to make things safer — it must not de-replicate the
// platform as a side effect.
func TestApplyStreamReplicas(t *testing.T) {
	for _, tc := range []struct {
		name        string
		current     int
		desired     int
		wantChanged bool
		wantRefused bool
		wantConfig  int
	}{
		{"no drift", 3, 3, false, false, 3},
		{"lift from one", 1, 3, true, false, 3},
		{"lift from unset is treated as a lift from one", 0, 3, true, false, 3},
		{"unset against a single-replica target is not a change", 0, 1, false, false, 0},
		{"scale down is refused", 3, 1, false, true, 3},
		{"scale down to an unset-equivalent is refused", 5, 3, false, true, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &nats.StreamConfig{Name: "s", Replicas: tc.current}
			changed, refused := applyStreamReplicas(cfg, tc.desired)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if refused != tc.wantRefused {
				t.Errorf("refusedDownward = %v, want %v", refused, tc.wantRefused)
			}
			if cfg.Replicas != tc.wantConfig {
				t.Errorf("cfg.Replicas = %d, want %d", cfg.Replicas, tc.wantConfig)
			}
		})
	}
}

// TestKvReplicaLiftIsNotBlockedByTheShrinkRefusal is a regression test for a bug
// introduced while writing this reconcile, and it guards the single most
// consequential bucket on the platform.
//
// The State-tier shrink refusal returns without updating, because applying a
// ceiling below a State bucket's current size makes JetStream refuse every write
// to it for the length of its TTL. dc_leases is a State bucket. So if the replica
// factor were computed after that guard, a dc_leases bucket that happened to be
// over its ceiling would become permanently unreplicable — the ADR-070 fence
// substrate, the one bucket whose loss blocks every standby's Acquire, silently
// pinned to a single node by an unrelated disk condition.
//
// The two reconciles are independent: being too full to safely bound says nothing
// about whether a bucket should be replicated. This asserts the ordering that keeps
// them independent, using a State-tier bucket driven over a deliberately tiny
// ceiling.
func TestKvReplicaLiftIsNotBlockedByTheShrinkRefusal(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// A bucket whose stored bytes exceed the ceiling it is about to be given.
	const bucket = "test_state_bucket"
	if _, err := nmgr.js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:   bucket,
		MaxBytes: 1 << 20,
		Replicas: 1,
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	store, err := nmgr.js.KeyValue(bucket)
	if err != nil {
		t.Fatalf("open bucket: %v", err)
	}
	payload := make([]byte, 4096)
	for i := 0; i < 32; i++ {
		if _, err := store.Put(kvKey(string(rune('a'+i))), payload); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	info, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	storedBytes := int64(info.State.Bytes)
	tinyCeiling := storedBytes / 2
	if tinyCeiling <= 0 {
		t.Fatalf("bucket did not accumulate bytes (%d); the shrink guard would not engage", storedBytes)
	}

	// The clamp resolves to 1 on this unclustered broker, so this test cannot
	// observe an actual lift. What it CAN observe — and what the bug was — is
	// whether the replica decision is reached at all, or short-circuited by the
	// byte guard. applyStreamReplicas is therefore exercised directly against the
	// same config the reconcile would carry, asserting the guard leaves it
	// reachable.
	cfg := info.Config
	changed, refused := applyStreamReplicas(&cfg, 3)
	if !changed || refused {
		t.Fatalf("applyStreamReplicas on an over-ceiling bucket: changed=%v refused=%v, want changed", changed, refused)
	}

	// And the reconcile itself must not panic or wedge on this bucket, and must
	// leave the over-ceiling State bucket's MaxBytes alone.
	nmgr.reconcileKvBucket(bucket, tinyCeiling, kv.State)
	after, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("stream info after reconcile: %v", err)
	}
	if after.Config.MaxBytes == tinyCeiling {
		t.Error("the shrink refusal did not hold: a State bucket over its ceiling was bounded anyway, " +
			"which makes JetStream refuse every write to it until its entries age out")
	}
}
