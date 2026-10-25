// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
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
// 🔴 THE LAYOUTS ARE NOT UNIFORM. Every other cache bucket keys on "{tenant}|something";
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

// A key the cache already DELETED still names the tenant: a KV delete leaves a marker on the key's
// subject, and a key listing skips it, so a purge that worked from the listing left it in the
// stream for good. The purge works from the stream's subject index, which still holds the marker,
// and removes it with the rest of the key's history, so the erasure covers a deleted tenant's
// evicted entries and not only its live ones.
func TestKvPurgeRemovesTheMarkerOfAnAlreadyDeletedKey(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store := seedBucket(t, js, bucket, purgeVictim+"|dev-1", purgeBystander+"|dev-1")
	require.NoError(t, store.Delete(kvKey(purgeVictim+"|dev-1")))
	victimSubject := "$KV." + bucket + "." + kvKey(purgeVictim+"|dev-1")

	subjects := func() map[string]uint64 {
		t.Helper()
		info, err := js.StreamInfo(KvStreamName(bucket), &nats.StreamInfoRequest{SubjectsFilter: "$KV." + bucket + ".>"})
		require.NoError(t, err)
		return info.State.Subjects
	}
	require.Contains(t, subjects(), victimSubject, "the delete must leave its marker or this proves nothing")

	res, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Keys)

	left := subjects()
	assert.NotContains(t, left, victimSubject, "the deleted key's marker still names the tenant")
	assert.Len(t, left, 1, "only the bystander's key remains")
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

// TestKvPurgeOfManyKeysIsExactUnderConcurrency drives far more keys than the purge has requests
// in flight, so every worker slot is reused many times, with the bystanders interleaved
// (including the prefix-collision tenant) and a deleted key's marker among them. The count
// must be exact (a lost increment or a double count is a wrong erasure figure in the deletion
// record), nothing of the victim may remain, and every bystander key must.
func TestKvPurgeOfManyKeysIsExactUnderConcurrency(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Hour})
	require.NoError(t, err)

	const victimKeys = kvPurgeConcurrency * 40
	var bystanders []string
	for i := 0; i < victimKeys; i++ {
		_, err := store.Put(kvKey(fmt.Sprintf("%s|dev-%d", purgeVictim, i)), []byte("v"))
		require.NoError(t, err)
		if i%10 == 0 {
			for _, other := range []string{purgeBystander, purgeVictim + "-2"} {
				k := fmt.Sprintf("%s|dev-%d", other, i)
				_, err := store.Put(kvKey(k), []byte("b"))
				require.NoError(t, err)
				bystanders = append(bystanders, k)
			}
		}
	}
	require.NoError(t, store.Delete(kvKey(purgeVictim+"|dev-0")))

	res, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)
	assert.Equal(t, int64(victimKeys), res.Keys)
	assert.Equal(t, int64(victimKeys), res.PerBucket[bucket])

	assert.ElementsMatch(t, bystanders, bucketKeys(t, store))
	info, err := js.StreamInfo(KvStreamName(bucket), &nats.StreamInfoRequest{SubjectsFilter: "$KV." + bucket + ".>"})
	require.NoError(t, err)
	assert.Len(t, info.State.Subjects, len(bystanders), "no marker of a victim key may remain")

	again, err := PurgeTenantKv(context.Background(), js, purgeInstance, purgeVictim)
	require.NoError(t, err)
	assert.Zero(t, again.Keys)
}

// A purge whose context is already cancelled must fail rather than report clean.
func TestKvPurgeWithACancelledContextFails(t *testing.T) {
	_, js := purgeRig(t)
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store := seedBucket(t, js, bucket, purgeVictim+"|dev-1", purgeVictim+"|dev-2")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := PurgeTenantKv(ctx, js, purgeInstance, purgeVictim)

	require.Error(t, err, "a cancelled pass that reported clean would let the coordinator ack an erasure that never ran")
	assert.Len(t, bucketKeys(t, store), 2)
}

// purgeSpy passes every JetStream call through to the real server and watches the purges: how
// many were issued, how many are in flight at once, and (when failAt or cancelAt is set) it
// fails or cancels at that call. Each purge holds briefly so that requests overlap and a
// missing bound or a missing wait shows up as a count rather than as luck.
type purgeSpy struct {
	nats.JetStreamContext
	failAt   int64
	cancelAt int64
	cancel   context.CancelFunc

	calls, inflight, maxInflight atomic.Int64
}

func (s *purgeSpy) PurgeStream(name string, opts ...nats.JSOpt) error {
	n := s.calls.Add(1)
	cur := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for {
		m := s.maxInflight.Load()
		if cur <= m || s.maxInflight.CompareAndSwap(m, cur) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	if n == s.cancelAt {
		s.cancel()
	}
	if n == s.failAt {
		return errors.New("injected purge failure")
	}
	return s.JetStreamContext.PurgeStream(name, opts...)
}

// seedManyVictimKeys writes count keys of the victim into a fresh device-by-token bucket.
func seedManyVictimKeys(t *testing.T, js nats.JetStreamContext, count int) {
	t.Helper()
	bucket := CacheBucketName(purgeInstance, "device-management", kv.BucketDeviceByToken)
	store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, TTL: time.Hour})
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		_, err := store.Put(kvKey(fmt.Sprintf("%s|dev-%d", purgeVictim, i)), []byte("v"))
		require.NoError(t, err)
	}
}

// The purges overlap, but never more than the bound: a purge is a request against one stream
// leader, and an unbounded fan-out over a large tenant is a burst that leader has to queue.
func TestKvPurgeOverlapsRequestsWithinTheBound(t *testing.T) {
	_, js := purgeRig(t)
	seedManyVictimKeys(t, js, kvPurgeConcurrency*8)

	spy := &purgeSpy{JetStreamContext: js}
	res, err := PurgeTenantKv(context.Background(), spy, purgeInstance, purgeVictim)
	require.NoError(t, err)
	assert.EqualValues(t, kvPurgeConcurrency*8, res.Keys)
	assert.LessOrEqual(t, spy.maxInflight.Load(), int64(kvPurgeConcurrency), "more purges in flight than the bound")
	assert.Greater(t, spy.maxInflight.Load(), int64(1), "the purges ran one at a time")
}

// A purge that fails mid-bucket fails the pass, and the call does not return while any purge
// it issued is still running: a request outliving the call would race the next pass.
func TestKvPurgeFailureFailsThePassAndAwaitsInFlightPurges(t *testing.T) {
	_, js := purgeRig(t)
	seedManyVictimKeys(t, js, kvPurgeConcurrency*8)

	spy := &purgeSpy{JetStreamContext: js, failAt: kvPurgeConcurrency * 2}
	_, err := PurgeTenantKv(context.Background(), spy, purgeInstance, purgeVictim)
	require.Error(t, err, "a pass with a failed purge must not report clean")
	assert.Contains(t, err.Error(), "injected purge failure")
	assert.Zero(t, spy.inflight.Load(), "a purge was still in flight when the call returned")
	assert.Less(t, spy.calls.Load(), int64(kvPurgeConcurrency*8), "dispatch did not stop at the failure")
}

// A context cancelled mid-scan stops dispatching: the pass fails, and at most the purges
// already in flight are issued after the cancellation.
func TestKvPurgeCancelledMidScanStopsDispatching(t *testing.T) {
	_, js := purgeRig(t)
	seedManyVictimKeys(t, js, kvPurgeConcurrency*8)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &purgeSpy{JetStreamContext: js, cancelAt: kvPurgeConcurrency, cancel: cancel}
	_, err := PurgeTenantKv(ctx, spy, purgeInstance, purgeVictim)
	require.Error(t, err)
	assert.LessOrEqual(t, spy.calls.Load(), int64(kvPurgeConcurrency*2), "dispatch went on after the context ended")
	assert.Zero(t, spy.inflight.Load())
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
// 🔴 IT CANNOT TELL ANYONE THE PLATFORM GREW ANOTHER TENANT-KEYED BUCKET. Nothing derives
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
		kv.BucketProfileResolutionByType, kv.BucketScopedGroupsExist,
		// Retired this release and still purged for one more — see tenantScopedBuckets.
		"metric-defs-by-type", "profile-scope-by-type",
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
	// NewCache for the live five (device-management/model/cache.go), and the area whose
	// previous release created the two retired ones, which are written as literals because
	// the kv constants that named them are gone.
	expected := []struct{ area, logical string }{
		{"device-management", kv.BucketDeviceByToken},
		{"device-management", kv.BucketRelationshipsBySource},
		{"device-management", kv.BucketMembershipsByEntity},
		{"device-management", kv.BucketProfileResolutionByType},
		{"device-management", kv.BucketScopedGroupsExist},
		{"device-management", "metric-defs-by-type"},
		{"device-management", "profile-scope-by-type"},
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
