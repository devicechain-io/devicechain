// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kv

import "testing"

// A duplicate name would make TierFor return the first entry's tier while the
// disk budget counted the bucket twice — an inconsistency that is invisible at
// the call site and quietly overstates the reservation.
func TestBucketNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range All {
		if seen[b.Name] {
			t.Errorf("bucket %q declared more than once", b.Name)
		}
		seen[b.Name] = true
	}
}

// Every declared bucket must carry the reasoning behind its tier. The tier is a
// judgment about what a refused write costs, and a tier with no stated reason is
// one nobody can safely re-evaluate later.
func TestEveryBucketExplainsItsTier(t *testing.T) {
	for _, b := range All {
		if b.Why == "" {
			t.Errorf("bucket %q has no Why: its tier cannot be reviewed", b.Name)
		}
		if b.Name == "" {
			t.Error("a bucket was declared with an empty name")
		}
	}
}

// An unregistered bucket must take State — the LARGER ceiling. This is the
// opposite of the default direction for message streams, and inverting it would
// bound an undeclared bucket holding real state at the cache ceiling, turning a
// full bucket into a failed login rather than a cache miss.
func TestUnregisteredBucketFallsToStateTier(t *testing.T) {
	if got := TierFor("a-bucket-nobody-declared"); got != State {
		t.Errorf("unregistered bucket got tier %v, want State (the larger ceiling)", got)
	}
}

// The declared buckets must resolve to the tier they were declared with — the
// lookup the whole budget keys on.
func TestTierForReturnsDeclaredTier(t *testing.T) {
	for _, b := range All {
		if got := TierFor(b.Name); got != b.Tier {
			t.Errorf("TierFor(%q) = %v, want %v", b.Name, got, b.Tier)
		}
	}
}

// Count must agree with All, since the disk budget multiplies it by the tier
// ceiling.
func TestCountMatchesAll(t *testing.T) {
	if got := Count(Cache) + Count(State); got != len(All) {
		t.Errorf("tier counts sum to %d, want %d: a bucket is in neither tier", got, len(All))
	}
}
