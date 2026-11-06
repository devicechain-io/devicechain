// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
)

// A bucket that declares its own small ceiling reserves that, not its tier's; and never more than
// the tier, so a compact preset's lower state ceiling still wins.
func TestABucketMayDeclareASmallerCeilingButNeverALargerOne(t *testing.T) {
	b, ok := kv.Lookup(kv.BucketPartitionPlacement)
	if !ok || b.MaxBytes != 4<<20 {
		t.Fatalf("the placement bucket must declare a 4 MiB ceiling, got %+v", b)
	}
	var n NatsConfiguration
	if got := n.KvMaxBytesFor(kv.BucketPartitionPlacement); got != 4<<20 {
		t.Errorf("default: placement ceiling %d, want 4 MiB", got)
	}
	if got := n.KvMaxBytesFor(kv.BucketLocks); got != DefaultKvStateMaxBytes {
		t.Errorf("an ordinary state bucket must keep the tier ceiling, got %d", got)
	}
	n.KvStateMaxBytes = 1 << 20
	if got := n.KvMaxBytesFor(kv.BucketPartitionPlacement); got != 1<<20 {
		t.Errorf("a tier ceiling below the bucket's own must win, got %d", got)
	}
}
