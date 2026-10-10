// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"testing"
)

// TestCacheKeysMatchTheirFormattedShape holds each concatenated cache key to the
// fmt.Sprintf shape it replaced. A key that changed shape would read nothing written under
// the old one, and the key-value tier is shared by replicas running different builds
// during a rollout.
func TestCacheKeysMatchTheirFormattedShape(t *testing.T) {
	ids := []uint{0, 1, 7, 10, 4294967295, math.MaxUint}
	for _, tenant := range []string{"", "acme", "tenant-0001"} {
		for _, id := range ids {
			if got, want := relationshipsBySourceKey(tenant, id), fmt.Sprintf("%s|%d", tenant, id); got != want {
				t.Errorf("relationshipsBySourceKey(%q, %d) = %q, want %q", tenant, id, got, want)
			}
			if got, want := profileResolutionByTypeKey(tenant, id), fmt.Sprintf("%s|%d", tenant, id); got != want {
				t.Errorf("profileResolutionByTypeKey(%q, %d) = %q, want %q", tenant, id, got, want)
			}
			for _, etype := range []string{"", "Device", "Area"} {
				if got, want := membershipsByEntityKey(tenant, etype, id), fmt.Sprintf("%s|%s|%d", tenant, etype, id); got != want {
					t.Errorf("membershipsByEntityKey(%q, %q, %d) = %q, want %q", tenant, etype, id, got, want)
				}
			}
		}
	}
}

// BenchmarkPerEventCacheKeys is the two keys every resolved event builds.
func BenchmarkPerEventCacheKeys(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = relationshipsBySourceKey("tenant-0001", uint(i))
		_ = profileResolutionByTypeKey("tenant-0001", 7)
	}
}
