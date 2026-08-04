// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	nats "github.com/nats-io/nats.go"

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
// 🔴 THE LAYOUTS DIFFER AND ONE OF THEM HAS NO SEPARATOR. Five of these key on
// "{tenant}|something"; scoped-groups-exist keys on the BARE tenant. An implementation that
// split on "|" and took the first field would miss that bucket entirely — silently, because
// a bucket with no matching keys is indistinguishable from one that was already clean. That
// is why the membership test below is equality OR prefix, and why it is one function rather
// than a rule per bucket.
var tenantScopedBuckets = []struct{ area, logical string }{
	{"device-management", kv.BucketDeviceByToken},
	{"device-management", kv.BucketRelationshipsBySource},
	{"device-management", kv.BucketMembershipsByEntity},
	{"device-management", kv.BucketMetricDefsByType},
	{"device-management", kv.BucketProfileScopeByType},
	{"device-management", kv.BucketScopedGroupsExist},
}

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

// purgeBucket scans one bucket and deletes the keys that decode to this tenant's.
func purgeBucket(ctx context.Context, js nats.JetStreamContext, bucket, tenant string) (int64, error) {
	store, err := js.KeyValue(bucket)
	if err != nil {
		// A bucket that was never created holds nothing. device-management creates these
		// at startup, so an instance without that area has none of them.
		if errors.Is(err, nats.ErrBucketNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("opening kv bucket %s: %w", bucket, err)
	}

	keys, err := store.Keys()
	if err != nil {
		// 🔴 AN EMPTY BUCKET IS AN ERROR IN THIS API, NOT AN EMPTY SLICE. nats.go returns
		// ErrNoKeysFound rather than nil, so a caller that treats every error as a failure
		// makes an already-clean bucket permanently fail its own purge.
		if errors.Is(err, nats.ErrNoKeysFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("listing kv bucket %s: %w", bucket, err)
	}

	var deleted int64
	for _, encoded := range keys {
		// A full-bucket scan is the one unbounded thing this store does — device-by-token
		// is documented as the largest KV consumer at fleet scale — and it runs on the
		// coordinator's single goroutine, which stops by cancelling a context. Without this
		// a shutdown mid-scan waits for the whole bucket.
		if err := ctx.Err(); err != nil {
			return deleted, fmt.Errorf("scanning kv bucket %s was interrupted: %w", bucket, err)
		}
		owned, err := keyBelongsTo(encoded, tenant)
		if err != nil {
			// A key this package did not write. Deleting it would be acting on something
			// we cannot read, and leaving it is a retention we cannot describe — so it
			// stops the purge with the bucket and the key named.
			return deleted, fmt.Errorf("kv bucket %s holds key %q, which is not a key this "+
				"platform writes and cannot be attributed to a tenant: %w", bucket, encoded, err)
		}
		if !owned {
			continue
		}
		// Purge rather than Delete: Delete leaves a tombstone carrying the key, and the
		// key's plaintext contains the tenant token. Purge removes the history too.
		if err := store.Purge(encoded); err != nil {
			return deleted, fmt.Errorf("purging %s from kv bucket %s: %w", encoded, bucket, err)
		}
		deleted++
	}
	return deleted, nil
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
			"jti with the owner's email as its value. Neither names a tenant, because a refresh " +
			"token belongs to the PERSON — the same identity the relational sweep deliberately " +
			"keeps. It is also inert for a deleted tenant: the refresh flow re-resolves membership " +
			"at exchange time, and that tenant's membership rows are gone, so the token buys " +
			"nothing. Deleting by the tenant's user emails would log those humans out of their " +
			"OTHER tenants, which is the same mistake as deleting the identity",
		kv.BucketOAuthCodes + ": single-use authorization codes with a 60-second lifetime. A code " +
			"issued before the cut expires long before a purge can complete, because the settle " +
			"window is measured in minutes and is floored well above it",
		kv.BucketLocks + " and " + kv.BucketLeases + ": coordination state keyed by functional area " +
			"and partition. No tenant is expressible in either today — the partitions in use are " +
			"fixed names — and both expire in seconds. 🔴 That first clause has a known expiry " +
			"date: the lease package's own doc names a per-tenant partition shape for the DETECT " +
			"engine, and the day one is taken this exemption is stale with nothing to fail on. The " +
			"seconds-long TTL is what bounds the residue either way",
	}
}
