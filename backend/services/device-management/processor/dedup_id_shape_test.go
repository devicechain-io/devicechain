// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// TestSourceDedupIDMatchesItsFormattedShape holds the concatenated dedup id to the
// fmt.Sprintf shape it replaced. The id is the broker's duplicate-window key: a publish
// retried across a rollout carries the id the other build gave it, and a changed shape
// would let both copies through.
func TestSourceDedupIDMatchesItsFormattedShape(t *testing.T) {
	for _, kind := range []string{"resolved", "failed"} {
		for _, seq := range []uint64{1, 9, 10, 1 << 32, math.MaxUint64} {
			for _, index := range []int{0, 1, 255, math.MaxInt} {
				src := messaging.Message{StreamSeq: seq}
				want := fmt.Sprintf("%s:%s:%d:%d", kind, "tenant-0001", seq, index)
				if got := sourceDedupID(kind, "tenant-0001", src, index); got != want {
					t.Errorf("sourceDedupID(%q, seq %d, index %d) = %q, want %q", kind, seq, index, got, want)
				}
			}
		}
	}
	if got := sourceDedupID("resolved", "t", messaging.Message{}, 0); got != "" {
		t.Errorf("a source with no stream sequence got id %q, want none", got)
	}
}
