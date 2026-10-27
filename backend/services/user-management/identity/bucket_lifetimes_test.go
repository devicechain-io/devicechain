// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
)

// The sign-in buckets' lifetimes are declared twice: here, where user-management creates them in
// the identity account, and in core/kv, which the broker administrator creates them with when the
// broker has no identity account (they then live in the shared account and user-management only
// verifies them). The two must agree, or the same code means a different lifetime depending on
// which account the broker has.
func TestTheCatalogDeclaresTheSignInLifetimesThisPackageUses(t *testing.T) {
	for name, want := range map[string]time.Duration{
		AuthCodeBucket:         AuthCodeTTL,
		kv.BucketSsoState:      SsoStateTTL,
		kv.BucketSsoCodes:      SsoCodeTTL,
		kv.BucketMfaChallenges: MfaChallengeTTL,
	} {
		b, ok := kv.Lookup(name)
		if !ok {
			t.Fatalf("bucket %s is not in the catalog", name)
		}
		if b.TTL != want {
			t.Errorf("bucket %s: the catalog declares a lifetime of %s, user-management creates it with %s", name, b.TTL, want)
		}
	}
}
