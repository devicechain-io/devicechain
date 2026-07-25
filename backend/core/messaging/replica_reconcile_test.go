// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

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
