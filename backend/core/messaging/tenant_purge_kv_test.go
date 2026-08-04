// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/kv"
)

// seedBucket creates a cache bucket and writes the given PLAINTEXT keys through the same
// encoder the runtime uses.
//
// 🔑 IT ENCODES RATHER THAN TAKING ENCODED STRINGS. A test that wrote its own base64 would
// be asserting the purge against the test's idea of the key format; going through kvKey
// means a change to the encoding breaks this test rather than silently making the purge
// scan for keys that no longer exist.
func seedBucket(t *testing.T, js nats.JetStreamContext, bucket string, plaintexts ...string) nats.KeyValue {
	t.Helper()
	store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Hour})
	require.NoErrorf(t, err, "creating bucket %s", bucket)
	for _, p := range plaintexts {
		_, err := store.Put(kvKey(p), []byte("value for "+p))
		require.NoErrorf(t, err, "putting %q", p)
	}
	return store
}

func bucketKeys(t *testing.T, store nats.KeyValue) []string {
	t.Helper()
	keys, err := store.Keys()
	if err != nil {
		require.ErrorIs(t, err, nats.ErrNoKeysFound)
		return nil
	}
	plain := make([]string, 0, len(keys))
	for _, k := range keys {
		raw, err := base64.RawURLEncoding.DecodeString(k)
		require.NoError(t, err)
		plain = append(plain, string(raw))
	}
	return plain
}

// TestKvPurgeCoversBothKeyLayouts is the assertion that matters, and the second bucket is
// the reason it exists.
//
// 🔴 THE LAYOUTS ARE NOT UNIFORM. Five cache buckets key on "{tenant}|something";
// scoped-groups-exist keys on the BARE tenant with no separator. An implementation that
// split on "|" and took the first field would miss that bucket entirely — and silently,
// because a bucket with no matching keys looks exactly like one that was already clean.
//
// The bystander is the control: a purge that deleted every key satisfies "the victim's keys
// are gone" completely.
func TestKvPurgeCoversBothKeyLayouts(t *testing.T) {
	_, js := purgeRig(t)

	composite := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	bare := CacheBucketName(purgeInstance, "device-management", kv.BucketScopedGroupsExist)

	compositeStore := seedBucket(t, js, composite,
		purgeVictim+"|dev-1", purgeVictim+"|dev-2", purgeBystander+"|dev-1")
	bareStore := seedBucket(t, js, bare, purgeVictim, purgeBystander)

	require.Len(t, bucketKeys(t, compositeStore), 3, "the seed must land or this proves nothing")
	require.Len(t, bucketKeys(t, bareStore), 2)

	res, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)

	assert.Equal(t, int64(3), res.Keys, "two composite keys and one bare one belonged to the victim")
	assert.Contains(t, res.PerBucket, bare,
		"the bare-tenant bucket contributed nothing, which is what a split-on-separator "+
			"implementation looks like — and it would report clean forever")

	assert.Equal(t, []string{purgeBystander + "|dev-1"}, bucketKeys(t, compositeStore))
	assert.Equal(t, []string{purgeBystander}, bucketKeys(t, bareStore))
}

// TestATenantWhoseTokenIsAPrefixOfAnothersIsUnaffected is the cross-tenant control.
//
// 🔴 "acme" AND "acme-2" SHARE A STRING PREFIX. A membership test written as
// HasPrefix(plain, tenant) would delete the second tenant's keys while purging the first —
// cross-tenant data loss, with nothing to notice it. The separator is what makes the test
// exact, and this is what proves it.
func TestATenantWhoseTokenIsAPrefixOfAnothersIsUnaffected(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	bare := CacheBucketName(purgeInstance, "device-management", kv.BucketScopedGroupsExist)

	store := seedBucket(t, js, bucket, "acme|dev-1", "acme-2|dev-1")
	bareStore := seedBucket(t, js, bare, "acme", "acme-2")

	res, err := PurgeTenantKv(context.Background(), js, purgeInstance, "acme")
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Keys)

	assert.Equal(t, []string{"acme-2|dev-1"}, bucketKeys(t, store),
		"acme-2 is a different tenant and its keys must survive acme's purge")
	assert.Equal(t, []string{"acme-2"}, bucketKeys(t, bareStore))
}

// TestKvPurgeIsIdempotent covers the Store contract: it runs on every pass until the purge
// completes, so the second call must succeed and report nothing.
func TestKvPurgeIsIdempotent(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	seedBucket(t, js, bucket, purgeVictim+"|dev-1")

	first, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Keys)

	second, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)

	require.NoError(t, err, "a repeat pass is the normal case")
	assert.Zero(t, second.Keys,
		"a store that reports work on every pass restarts the coordinator's settle window, so the "+
			"purge would never complete and the token would never be released")
	assert.Empty(t, second.PerBucket)
}

// TestAnAbsentOrEmptyBucketIsNotAnError pins the two shapes of "nothing here", both of
// which nats.go reports as failures.
//
// A bucket that was never created belongs to an instance that does not run
// device-management. An EMPTY one returns ErrNoKeysFound rather than an empty slice — so a
// caller that treats every error alike makes an already-clean bucket fail its own purge
// forever, which is the blocks-GA direction.
func TestAnAbsentOrEmptyBucketIsNotAnError(t *testing.T) {
	_, js := purgeRig(t)

	absent, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err, "no bucket exists at all")
	assert.Zero(t, absent.Keys)

	seedBucket(t, js, CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken))
	empty, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err, "the bucket exists and holds no keys")
	assert.Zero(t, empty.Keys)
}

// TestAKeyThisPlatformDidNotWriteStopsThePurge pins the fail-closed direction.
//
// A key that does not decode is one this package did not write. Deleting it would be acting
// on something unreadable; skipping it would be a retention nobody could describe. Failing
// names the bucket and the key, which is the only outcome an operator can act on.
func TestAKeyThisPlatformDidNotWriteStopsThePurge(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Hour})
	require.NoError(t, err)
	// Five characters: RawURLEncoding rejects any length that is 1 mod 4, so this cannot
	// be the encoding of anything. (A key made only of alphabet characters at a legal
	// length WOULD decode — to nonsense — which is why the invalid case has to be chosen
	// deliberately rather than by looking wrong.)
	_, err = store.Put("abcde", []byte("x"))
	require.NoError(t, err)

	_, err = PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)

	require.Error(t, err)
	assert.Contains(t, err.Error(), bucket, "the message must name where to look")
	assert.Contains(t, err.Error(), "abcde")
}

// TestEveryTenantScopedBucketIsInTheInventory is the coverage claim, and it is the honest
// stand-in rather than a derivation.
//
// 🔴 IT CANNOT TELL ANYONE THE PLATFORM GREW A SEVENTH TENANT-KEYED BUCKET. Nothing derives
// the tenant-scoped set from the code that writes the keys. What it CAN do is make removing
// one a failing test rather than a silent change — a bucket dropped from the inventory is
// never scanned, and its absence looks exactly like a bucket that reported clean.
func TestEveryTenantScopedBucketIsInTheInventory(t *testing.T) {
	got := map[string]bool{}
	for _, b := range tenantScopedBuckets {
		got[b.logical] = true
	}

	for _, want := range []string{
		kv.BucketDeviceByToken, kv.BucketRelationshipsBySource, kv.BucketMembershipsByEntity,
		kv.BucketMetricDefsByType, kv.BucketProfileScopeByType, kv.BucketScopedGroupsExist,
	} {
		assert.Truef(t, got[want], "%s keys on a tenant and is not in the purge inventory, so a "+
			"deleted tenant's entries in it are never scanned", want)
	}

	// And the other side: every bucket in the platform inventory is either scanned or
	// explained. A bucket that is neither is the gap this pairing exists to make loud.
	explained := ""
	for _, e := range KvPurgeExemptions() {
		explained += e
	}
	for _, b := range kv.All {
		if got[b.Name] {
			continue
		}
		assert.Containsf(t, explained, b.Name,
			"%s is in the platform's kv inventory but is neither purged nor named in the "+
				"exemptions, so nobody reading a deletion record can tell what it holds", b.Name)
	}
}

// TestEveryBucketInTheInventoryIsActuallyReached closes the gap the inventory test cannot.
//
// 🔴 THE (AREA, LOGICAL) PAIR IS WHAT NAMES A BUCKET, AND ONLY THE LOGICAL HALF WAS EVER
// CHECKED. A wrong area string produces a bucket name nothing ever created, which
// PurgeTenantKv tolerates as ErrBucketNotFound — deliberately, because an instance without
// device-management has none of these. So a typo does not fail: it reports clean, on every
// pass, forever, over a bucket full of the deleted tenant's cached resolutions.
//
// 🔑 THE SEED LIST IS INDEPENDENT OF THE INVENTORY, AND THAT IS THE WHOLE TEST. A first
// version of this seeded buckets by iterating tenantScopedBuckets — so it created whatever
// the inventory named, purged whatever the inventory named, and passed with a deliberately
// typo'd area. A derived oracle cannot confirm itself. The pairs below are written out, so
// a typo in the inventory means the purge looks somewhere the seed is not.
func TestEveryBucketInTheInventoryIsActuallyReached(t *testing.T) {
	_, js := purgeRig(t)

	// Written out on purpose — see above. "device-management" is the area that calls
	// NewCache for all six (device-management/model/cache.go).
	expected := []struct{ area, logical string }{
		{"device-management", kv.BucketDeviceByToken},
		{"device-management", kv.BucketRelationshipsBySource},
		{"device-management", kv.BucketMembershipsByEntity},
		{"device-management", kv.BucketMetricDefsByType},
		{"device-management", kv.BucketProfileScopeByType},
		{"device-management", kv.BucketScopedGroupsExist},
	}
	require.Len(t, tenantScopedBuckets, len(expected),
		"the inventory gained or lost a bucket; this test's independent list has to follow, and "+
			"deriving it from the inventory would make the check confirm itself")

	stores := map[string]nats.KeyValue{}
	for _, e := range expected {
		bucket := CacheBucketName(purgeInstance, e.area, e.logical)
		// scoped-groups-exist keys on the bare tenant; every other bucket on tenant|rest.
		// Seeding each with ITS OWN layout is the point — one layout everywhere would let
		// the bare-tenant bucket pass on a key it never actually holds.
		victim, bystander := purgeVictim+"|x", purgeBystander+"|x"
		if e.logical == kv.BucketScopedGroupsExist {
			victim, bystander = purgeVictim, purgeBystander
		}
		stores[bucket] = seedBucket(t, js, bucket, victim, bystander)
	}
	require.Len(t, stores, len(expected), "two entries produced the same bucket name")

	res, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)

	assert.Equal(t, int64(len(expected)), res.Keys,
		"one key per bucket belonged to the victim, so a smaller total means a bucket the purge "+
			"never reached — most likely a wrong functional area in the inventory, which looks "+
			"exactly like a bucket that was already clean")
	for bucket, store := range stores {
		assert.Containsf(t, res.PerBucket, bucket, "%s contributed nothing", bucket)
		assert.Lenf(t, bucketKeys(t, store), 1, "%s should hold only the bystander's key", bucket)
	}
}
