// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"
)

// TestEffectiveStreamReplicasClampsToBrokerCapability pins the decision table of
// the clamp itself. The interesting row is the last one: a configured 3 against an
// unclustered broker resolves to 1 rather than to 3, because issuing 3 there is not
// a degraded request, it is a hard server error on every stream and bucket the
// platform creates.
func TestEffectiveStreamReplicasClampsToBrokerCapability(t *testing.T) {
	tests := []struct {
		name       string
		configured uint32
		clustered  bool
		want       int
	}{
		{"unset on a single node", 0, false, 1},
		{"unset on a cluster", 0, true, 1},
		{"one on a single node", 1, false, 1},
		{"one on a cluster", 1, true, 1},
		{"three on a cluster is honoured", 3, true, 3},
		{"five on a cluster is honoured", 5, true, 5},
		{"three on a single node is clamped", 3, false, 1},
		{"five on a single node is clamped", 5, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nmgr, cleanup := newTestManager(t)
			defer cleanup()
			nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = tt.configured
			nmgr.clustered = tt.clustered

			if got := nmgr.effectiveStreamReplicas(); got != tt.want {
				t.Errorf("effectiveStreamReplicas() = %d, want %d (configured %d, clustered %v)",
					got, tt.want, tt.configured, tt.clustered)
			}
		})
	}
}

// TestReplicatedConfigAgainstUnclusteredBrokerStillCreatesBuckets is the test that
// matters, and it is deliberately an end-to-end one against a real broker rather
// than an assertion about the clamp function.
//
// The embedded server this harness starts is UNCLUSTERED, which is exactly the
// topology dcctl ships today. Asking it for 3 replicas makes nats-server refuse the
// bucket outright (JSStreamReplicasNotSupportedErr), and in a real service that
// refusal is retried and then crashloops the pod — including user-management, which
// is KV-only and therefore auth and JWKS for the whole platform.
//
// So this asserts the two halves of "degrade, do not die": the bucket is created at
// all, and it is created at 1 replica. Mutation-verify by making
// effectiveStreamReplicas return desiredStreamReplicas unconditionally: this test
// fails on the CREATE call, with the server error the clamp exists to prevent.
func TestReplicatedConfigAgainstUnclusteredBrokerStillCreatesBuckets(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	// clustered stays false — the embedded broker genuinely is not clustered, so
	// this matches what ExecuteInitialize would have read from it.

	if _, err := nmgr.NewCache("devices", time.Minute); err != nil {
		t.Fatalf("NewCache against an unclustered broker with streamReplicas=3: %v "+
			"(the replica factor must be clamped so the platform starts)", err)
	}

	info, err := nmgr.js.StreamInfo(kvStreamPrefix + "test_area_devices")
	if err != nil {
		t.Fatalf("StreamInfo for the created bucket: %v", err)
	}
	if info.Config.Replicas != 1 {
		t.Errorf("bucket created with Replicas = %d, want 1: an unclustered broker cannot replicate, "+
			"so the configured 3 must not reach it", info.Config.Replicas)
	}
}

// TestDesiredStreamReplicasIsUnclamped guards the other half of the pair. The
// desired value has to survive unclamped, because it is what the mismatch is
// reported and (in U3) exported against — a desired that silently became the
// effective value would make the two always agree and the "this instance is not
// HA" signal permanently unreachable.
func TestDesiredStreamReplicasIsUnclamped(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	nmgr.clustered = false

	if got := nmgr.desiredStreamReplicas(); got != 3 {
		t.Errorf("desiredStreamReplicas() = %d, want 3 — the configured value must survive the clamp "+
			"so the mismatch remains observable", got)
	}
	if nmgr.effectiveStreamReplicas() == nmgr.desiredStreamReplicas() {
		t.Error("desired and effective agree on an unclustered broker; the mismatch signal is unreachable")
	}
}
