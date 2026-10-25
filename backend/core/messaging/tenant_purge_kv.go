// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	nats "github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
)

// Erasing one tenant from the key-value store (ADR-077).
//
// # 🔴 There is no server-side filter, and the reason is structural
//
// A KV key becomes ONE SUBJECT TOKEN under `$KV.{bucket}.`, and kvKey base64url-encodes
// the plaintext — so an encoded key never contains a dot. `*` therefore matches the whole
// key and `>` matches everything: there is no prefix a NATS filter can express, whatever
// the plaintext looks like. Every tenant-scoped erasure here is a full-bucket scan,
// decoding each key and comparing.
//
// A prefix scan on the ENCODED form is not a shortcut either, and it is worth stating
// because it looks like one: base64 packs three bytes into four characters, so the encoding
// of "{tenant}|" is a stable string prefix only when len(tenant)+1 is a multiple of 3.
// Otherwise the boundary byte is smeared across a shared sextet and the prefix silently
// stops matching. Decode, then compare.

// tenantScopedBuckets are the cache buckets whose keys carry a tenant, with the functional
// area that creates them — the concrete bucket name folds the area in, so it cannot be
// derived from the logical name alone.
//
// 🔴 THE LAYOUTS DIFFER AND ONE OF THEM HAS NO SEPARATOR. All but one of these key on
// "{tenant}|something"; scoped-groups-exist keys on the BARE tenant. An implementation that
// split on "|" and took the first field would miss that bucket entirely — silently, because
// a bucket with no matching keys is indistinguishable from one that was already clean. That
// is why the membership test below is equality OR prefix, and why it is one function rather
// than a rule per bucket.
var tenantScopedBuckets = []struct{ area, logical string }{
	{"device-management", kv.BucketDeviceByToken},
	{"device-management", kv.BucketRelationshipsBySource},
	{"device-management", kv.BucketMembershipsByEntity},
	{"device-management", kv.BucketProfileResolutionByType},
	{"device-management", kv.BucketScopedGroupsExist},
	{"device-management", retiredBucketMetricDefsByType},
	{"device-management", retiredBucketProfileScopeByType},
}

// The two per-device-type caches that profile-resolution-by-type replaced. Nothing creates
// or writes them any more, and they are not in the kv inventory, but they stay in the purge
// for ONE release, then go.
//
// 🔴 THEY ARE HERE FOR THE ROLLOUT, NOT FOR THE ORPHANS AFTER IT. The purge runs in
// user-management, and during an upgrade it can run while device-management pods of the
// previous release are still filling these buckets with the tenant's resolutions. Dropping
// them from this list the same release they were retired would let a tenant deletion in
// that window report clean over entries it never scanned, which then outlive it for the TTL
// the bucket was created with — and that TTL is fixed at creation and has no upper bound.
// Scanning a bucket that no longer exists costs nothing: purgeBucket treats it as empty.
//
// They are literals rather than kv constants on purpose: the inventory must not list a
// bucket nothing creates, and the constants were deleted so that no live code can reach
// them by name.
const (
	retiredBucketMetricDefsByType   = "metric-defs-by-type"
	retiredBucketProfileScopeByType = "profile-scope-by-type"
)

// KvPurgeResult is what one key-value purge removed.
type KvPurgeResult struct {
	// Keys is the total deleted across every bucket.
	Keys int64
	// PerBucket is what each bucket contributed. Buckets contributing nothing are omitted.
	PerBucket map[string]int64
}

// PurgeTenantKv deletes every key belonging to tenant from the tenant-scoped buckets.
//
// It is idempotent: a second call finds nothing and reports zero. A bucket that does not
// exist is not an error — the caches are created by device-management on startup, so an
// instance that does not run that area has none of them.
func PurgeTenantKv(ctx context.Context, js nats.JetStreamContext, instanceId, tenant string) (KvPurgeResult, error) {
	res := KvPurgeResult{PerBucket: map[string]int64{}}
	// The same guard the broker purge carries, for a weaker reason: a KV delete is per-key
	// so an empty tenant cannot widen anything the way a subject filter can. It is here
	// because an empty token would match the encoding of every key whose plaintext starts
	// with "|", and because a purge asked to erase nobody should say so rather than scan.
	if err := core.ValidateToken(tenant); err != nil {
		return res, fmt.Errorf("refusing to purge the key-value store for tenant %q: %w", tenant, err)
	}
	if strings.TrimSpace(instanceId) == "" {
		return res, fmt.Errorf("refusing to purge the key-value store with no instance id: every " +
			"bucket name is rooted at it, so nothing would be found and the store would report clean")
	}

	for _, b := range tenantScopedBuckets {
		bucket := CacheBucketName(instanceId, b.area, b.logical)
		n, err := purgeBucket(ctx, js, bucket, tenant)
		if err != nil {
			return res, err
		}
		if n > 0 {
			res.PerBucket[bucket] += n
			res.Keys += n
		}
	}
	return res, nil
}

// purgeBucket scans one bucket and removes the keys that decode to this tenant's.
//
// 🔴 IT NEVER OPENS THE BUCKET AS A KEY-VALUE STORE, AND THAT IS DELIBERATE. A KV Keys() call is an
// ordered consumer on the bucket's stream and a KV Purge is a write into its subjects, and both
// would hand the tenant-purge login rights this erasure does not need and must not have: a
// consumer on device-by-token is a read of every tenant's device mapping, and a write there is
// the forgery of one. The keys are listed from the stream's subject index (the same listing the
// MQTT sweeps use) and each owned key's subject is purged from the stream, which is also a
// stronger erasure: a KV purge leaves a marker whose subject still names the key, and this
// removes the marker with the history.
func purgeBucket(ctx context.Context, js nats.JetStreamContext, bucket, tenant string) (int64, error) {
	stream := KvStreamName(bucket)
	prefix := kvSubjectPrefix + bucket + "."
	info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: prefix + ">"}, nats.Context(ctx))
	if err != nil {
		// A bucket that was never created holds nothing. device-management creates these
		// at startup, so an instance without that area has none of them.
		if errors.Is(err, nats.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("listing the keys of kv bucket %s: %w", bucket, err)
	}

	// The purges are independent single-subject requests, so they run concurrently. One at a
	// time each paid a full round trip (on a replicated stream, a consensus write), which made
	// a 189k-key tenant a ~100 s erasure on loopback and many minutes on a cluster. The scan
	// and the ownership test stay on this goroutine, so a key this package did not write is
	// still found before it is acted on, and dispatching stops at the first failure.
	var deleted atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(kvPurgeConcurrency)
	var scanErr error
dispatch:
	for subject := range info.State.Subjects {
		// A full-bucket scan is the one unbounded thing this store does — device-by-token
		// is documented as the largest KV consumer at fleet scale — and it runs on the
		// coordinator's single goroutine, which stops by cancelling a context. Without this
		// a shutdown mid-scan waits for the whole bucket. gctx also ends when a purge fails.
		if err := gctx.Err(); err != nil {
			scanErr = fmt.Errorf("scanning kv bucket %s was interrupted: %w", bucket, err)
			break dispatch
		}
		encoded := strings.TrimPrefix(subject, prefix)
		owned, err := keyBelongsTo(encoded, tenant)
		if err != nil {
			// A key this package did not write. Deleting it would be acting on something
			// we cannot read, and leaving it is a retention we cannot describe — so it
			// stops the purge with the bucket and the key named.
			scanErr = fmt.Errorf("kv bucket %s holds key %q, which is not a key this "+
				"platform writes and cannot be attributed to a tenant: %w", bucket, encoded, err)
			break dispatch
		}
		if !owned {
			continue
		}
		g.Go(func() error {
			if err := js.PurgeStream(stream, &nats.StreamPurgeRequest{Subject: subject}, nats.Context(gctx)); err != nil {
				return fmt.Errorf("purging %s from kv bucket %s: %w", encoded, bucket, err)
			}
			deleted.Add(1)
			return nil
		})
	}
	// Wait for every purge already issued before reporting, so the count is exact and no
	// request outlives the call. A failed purge wins over a scan stop: it is the cause.
	if err := g.Wait(); err != nil {
		return deleted.Load(), err
	}
	return deleted.Load(), scanErr
}

// kvPurgeConcurrency bounds the purge requests in flight per bucket. Each is a small
// request-and-ack against one stream, so the gain is overlapped round trips (and, on a
// replicated stream, overlapped consensus rounds), not parallel disk work: a few dozen in
// flight saturates the stream leader and more only queues behind it. 32 recovers nearly
// all of the latency the sequential loop paid without a burst large enough to press the
// server's per-connection pending limits. Re-keying tenant-scoped buckets as
// "<tenant>.<key>" would replace all of these requests with one subject-filtered purge
// per bucket; that is a data-format change and is deliberately not made here.
const kvPurgeConcurrency = 32

// kvSubjectPrefix is what JetStream puts in front of a bucket's name to form the subjects its
// keys are stored under ("$KV.<bucket>.<key>"). nats.go keeps its copy unexported.
const kvSubjectPrefix = "$KV."

// TenantScopedBucketStreams are the streams backing every cache bucket a tenant purge scans, for
// an instance, so the grant of the area that runs the purge names exactly those and no other.
func TenantScopedBucketStreams(instanceId string) []string {
	out := make([]string, 0, len(tenantScopedBuckets))
	for _, b := range tenantScopedBuckets {
		out = append(out, KvStreamName(CacheBucketName(instanceId, b.area, b.logical)))
	}
	return out
}

// tenantCacheArea is the area whose in-process caches a tenant purge tells to let go.
const tenantCacheArea = "device-management"

// TenantCacheEvictionSubjects is the wildcard over the eviction subjects a tenant purge publishes
// on, for an instance (BroadcastTenantCacheEviction), so the purging area's grant names it.
func TenantCacheEvictionSubjects(instanceId string) string {
	return CacheEvictSubjects(instanceId, tenantCacheArea)
}

// keyBelongsTo reports whether an encoded KV key is this tenant's.
//
// The two real layouts are "{tenant}" and "{tenant}|...", so the test is equality OR the
// tenant followed by the separator. Both halves are load-bearing: without the first,
// scoped-groups-exist is missed entirely; without the second, every other bucket is.
//
// DeviceClientIDMatches applies the same equality-or-prefix-plus-separator test to MQTT
// client ids, guarding cross-device session takeover rather than cross-tenant data loss.
// A third instance of the shape is worth extracting; two are worth cross-referencing.
//
// 🔑 THE SEPARATOR IS WHAT MAKES THIS SAFE AGAINST A PREFIX COLLISION. Tenants "acme" and
// "acme-2" share a string prefix, and a naive HasPrefix(plain, tenant) would delete the
// second's keys while purging the first — cross-tenant data loss, silently. "acme-2|x"
// does not start with "acme|" and does not equal "acme", so the test is exact.
func keyBelongsTo(encoded, tenant string) (bool, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false, err
	}
	plain := string(raw)
	return plain == tenant || strings.HasPrefix(plain, tenant+kvTenantSeparator), nil
}

// kvTenantSeparator is the character the cache key builders put between the tenant and the
// rest of a composite key.
const kvTenantSeparator = "|"

// KvPurgeExemptions explains the buckets a tenant purge deliberately does NOT touch.
//
// They are returned as sentences rather than skipped in silence because each one is a
// judgement, and a reader deciding whether an erasure claim holds needs the judgement, not
// the omission. None of them blocks completion — that is what makes writing them down the
// only thing standing between a reader and an unexamined assumption.
//
// 🔑 IT IS ALSO THE HALF OF A COVERAGE CLAIM. Paired with tenantScopedBuckets it partitions
// the platform's kv inventory: every bucket is either scanned or explained here, and
// TestEveryTenantScopedBucketIsInTheInventory fails on one that is neither. That is what
// makes a bucket added tomorrow a failing test rather than a silent retention — the
// inventory cannot be derived from the code that writes the keys, so this is the honest
// stand-in for a derivation.
func KvPurgeExemptions() []string {
	return []string{
		kv.BucketRefreshTokens + ": holds one entry per live refresh token, keyed by an opaque " +
			"jti with the owner's email as its value, and each sign-in's record and index entry " +
			"(keyed by session id and by a hash of the email). None of them names a tenant, because a refresh " +
			"token belongs to the PERSON — the same identity the relational sweep deliberately " +
			"keeps. It is also inert for a deleted tenant: the refresh flow re-resolves membership " +
			"at exchange time, and that tenant's membership rows are gone, so the token buys " +
			"nothing; and it refuses a token issued before the tenant row it names was created, so " +
			"a token outliving the purge buys nothing in a later tenant created with the same token " +
			"either. Deleting by the tenant's user emails would log those humans out of their " +
			"OTHER tenants, which is the same mistake as deleting the identity",
		kv.BucketOAuthCodes + ": single-use authorization codes with a 60-second lifetime. A code " +
			"issued before the cut expires long before a purge can complete, because the settle " +
			"window is measured in minutes and is floored well above it",
		kv.BucketSsoState + " and " + kv.BucketSsoCodes + ": single sign-on attempts (a ten-minute " +
			"state record) and completed sign-ins awaiting completion (a single-use record with " +
			"a 60-second lifetime). A record names an external account and the connection it came " +
			"through, never a tenant, and both expire inside the settle window",
		kv.BucketCredentialAttempts + ": sign-in backoff state, one entry per recently failing " +
			"principal of a throttled kind, keyed by a hash of the presented identifier (today an " +
			"email). It names no tenant — a sign-in attempt belongs to the PERSON, like the refresh-token store — and " +
			"every entry expires ten minutes after its last write, well inside the settle window",
		kv.BucketDeviceCredentialAttempts + ": MQTT password backoff state, one entry per " +
			"recently tried MQTT username. The username does name a tenant, but the key is a HASH " +
			"of it, so no sweep can find a tenant's entries by name; the entries carry only a " +
			"count and a time. Every entry expires ten minutes after its last write, well inside " +
			"the settle window, and once device-management sees the delete, that tenant's " +
			"connects are refused before they are counted",
		kv.BucketLocks + " and " + kv.BucketLeases + ": coordination state keyed by functional area " +
			"and partition. No tenant is expressible in either today — the partitions in use are " +
			"fixed names, or name an operator-configured event source, never a tenant — and both " +
			"expire in seconds. 🔴 That first clause has a known expiry " +
			"date: the lease package's own doc names a per-tenant partition shape for the DETECT " +
			"engine, and the day one is taken this exemption is stale with nothing to fail on. The " +
			"seconds-long TTL is what bounds the residue either way",
	}
}

// DeviceCredentialCacheName names device-management's in-process credential cache: the one
// cache of that service with no bucket, whose evictions travel only on its eviction subject.
// It is declared here, with the buckets, so a tenant's erasure can reach it.
const DeviceCredentialCacheName = "device-credentials"

// BroadcastTenantCacheEviction tells every device-management replica to drop every entry of
// tenant from its in-process caches: one message on each tenant-scoped cache's eviction
// subject, and one for the credential cache.
//
// 🔑 IT IS THE OTHER HALF OF PurgeTenantKv. The purge clears the buckets, which every replica
// shares, but each replica also holds what it read in its own memory for up to the caches'
// time to live (minutes), and nothing in the bucket tells it to let go. Without this a
// deleted tenant's devices keep resolving, and their credentials keep authenticating, from
// memory for that long after the data they describe is gone. It sends to every cache even
// when the purge deleted nothing, as the purge repeats until it finds nothing, and a replica
// that missed a pass hears the next.
//
// Like every eviction it only removes, so a lost message costs hit rate, never correctness,
// and the time to live bounds it. The returned error is the first publish failure, after all
// were tried; the purge treats it as retryable.
func BroadcastTenantCacheEviction(nc *nats.Conn, instanceId, tenant string) error {
	if err := core.ValidateToken(tenant); err != nil {
		return fmt.Errorf("refusing to evict the in-process caches for tenant %q: %w", tenant, err)
	}
	if strings.TrimSpace(instanceId) == "" {
		return errors.New("refusing to evict the in-process caches with no instance id: every " +
			"subject is rooted at it, so nothing would hear it and the replicas would report clean")
	}
	names := []string{DeviceCredentialCacheName}
	for _, b := range tenantScopedBuckets {
		if b.area == tenantCacheArea && b.logical != retiredBucketMetricDefsByType &&
			b.logical != retiredBucketProfileScopeByType {
			names = append(names, b.logical)
		}
	}
	var first error
	for _, name := range names {
		b := newEvictionBroadcast(nc, CacheEvictSubject(instanceId, tenantCacheArea, name), name, nil)
		if err := b.PublishTenant(tenant); err != nil && first == nil {
			first = err
		}
	}
	return first
}
