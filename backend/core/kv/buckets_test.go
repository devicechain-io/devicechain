// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kv

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

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

// Every bucket is opened by somebody, every area named is a real one, and a cache bucket -
// which is created per instance AND per area - names exactly one.
func TestEveryBucketNamesItsAreas(t *testing.T) {
	for _, b := range All {
		if len(b.Writers) == 0 || len(b.Readers) == 0 {
			t.Errorf("bucket %q declares no writer or no reader: nothing could open it", b.Name)
		}
		for _, a := range append(append([]string{}, b.Writers...), b.Readers...) {
			if !streams.IsPlatformArea(a) {
				t.Errorf("bucket %q names %q, which is not a platform area", b.Name, a)
			}
		}
		if b.Tier == Cache && len(b.Admins()) != 1 {
			t.Errorf("cache bucket %q is per-area, but names %v", b.Name, b.Admins())
		}
	}
}

func TestKnownBucketRoles(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  bool
	}{
		{"device-management writes device-by-token", Writes(BucketDeviceByToken, "device-management")},
		{"user-management writes refresh tokens", Writes(BucketRefreshTokens, "user-management")},
		{"event-processing holds leases", Administers(BucketLeases, "event-processing")},
		{"sparkplug-ingest holds leases", Administers(BucketLeases, "sparkplug-ingest")},
		{"event-sources takes locks", Administers(BucketLocks, "event-sources")},
	} {
		if !tc.got {
			t.Errorf("%s: not declared", tc.name)
		}
	}
	for _, tc := range []struct {
		name string
		got  bool
	}{
		// Forging a device through the cache event resolution trusts, and reading a
		// refresh token, are the two things this declaration exists to fence.
		{"event-sources must not write device-by-token", Writes(BucketDeviceByToken, "event-sources")},
		{"lwm2m-ingest must not write device-by-token", Writes(BucketDeviceByToken, "lwm2m-ingest")},
		{"device-management must not read refresh tokens", Reads(BucketRefreshTokens, "device-management")},
		{"an undeclared bucket has no writer", Writes("a-bucket-nobody-declared", "user-management")},
	} {
		if tc.got {
			t.Errorf("%s", tc.name)
		}
	}
}
