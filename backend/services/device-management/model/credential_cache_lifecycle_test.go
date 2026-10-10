// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// warmBoth authenticates the fixture's credential and leaves it cached.
func warmBoth(t *testing.T, f ccFixture) {
	t.Helper()
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "warm")
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "held")
}

// A tenant's erasure drops every credential of the tenant, by the broadcast's tenant-wide
// message, and the next check reads the database.
func TestATenantWideEvictionDropsEveryCredentialOfTheTenant(t *testing.T) {
	f := newCredentialCacheFixture(t)
	warmBoth(t, f)

	f.cache.ApplyEviction(messaging.CacheEviction{Tenant: "another", All: true})
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 0, "another tenant's erasure must not touch acme")

	f.cache.ApplyEviction(messaging.CacheEviction{Tenant: "acme", All: true})
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "after acme's erasure")
}

// A replica that reconnects to the broker drops every credential it holds, since it may
// have missed evictions while it was away.
func TestClearingTheCredentialCacheDropsEverythingAndRejectsInFlightFills(t *testing.T) {
	f := newCredentialCacheFixture(t)
	warmBoth(t, f)
	entries, _ := f.cache.len()
	require.Equal(t, 1, entries)

	gen, readAt := f.cache.readStarted()
	f.cache.Clear()
	entries, bytes := f.cache.len()
	require.Zero(t, entries)
	require.Zero(t, bytes)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "after clear")

	// A read begun before the clear must not put what it read back.
	cred := &DeviceCredential{}
	f.cache.fill(credentialCacheKey("acme", "x", "y"), "x", cred, gen, readAt)
	entries, _ = f.cache.len()
	require.Equal(t, 1, entries, "only the entry the post-clear check stored; the stale fill was accepted")
}

// The time an entry is kept is configurable, and a value of zero or less is refused.
func TestTheCredentialCacheTimeToLiveIsConfigurable(t *testing.T) {
	require.Equal(t, 5*time.Minute, CredentialCacheTTL, "the default is the documented five minutes")
	require.Equal(t, time.Minute, NewCredentialCache(1, 1<<20, WithCredentialCacheTTL(time.Minute)).ttl)
	require.Equal(t, CredentialCacheTTL, NewCredentialCache(1, 1<<20).ttl)
	require.Panics(t, func() { WithCredentialCacheTTL(0) })
	require.Panics(t, func() { WithCredentialCacheTTL(-time.Second) })
}
