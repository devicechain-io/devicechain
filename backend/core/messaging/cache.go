// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	nats "github.com/nats-io/nats.go"
)

// Cache is a distributed key/value cache backed by a NATS JetStream KV bucket
// (ADR-007: NATS KV replaces Redis). One Cache wraps one bucket; the bucket's
// per-entry TTL bounds staleness, so a positive entry is evicted automatically
// even if its source row changes between explicit invalidations.
//
// Values are JSON-encoded, so any json-serializable value round-trips. Cache
// keys are arbitrary caller strings (e.g. "tenant|token"); they are base64url-
// encoded before use because the NATS KV key charset is restricted and the
// caller's keys are not guaranteed to fall within it.
//
// 🔴 THE ctx ON EVERY METHOD BELOW IS NOT OBSERVED, AND A CALLER MUST NOT PLAN
// AROUND IT. It is not an oversight to be fixed at these call sites: the nats.go v1
// KeyValue interface takes no context on Get, Put, Create or Delete, so there is
// nowhere to put one. Each round trip is bounded only by the JetStream client's own
// request timeout.
//
// The parameters stay because they are the right shape for the operation and because
// removing them would make every caller's context stop here rather than merely be
// ignored here — but a caller wrapping a cache read in a short deadline expecting to
// fall back to Postgres when it expires does NOT get that behaviour, and writing code
// that depends on it produces a timeout that never fires. Honouring them means moving
// this bucket onto the jetstream package's KV, whose methods do take a context; that
// is a migration, not an edit.
type Cache struct {
	kv cacheStore
}

// cacheStore is the part of nats.KeyValue a Cache actually uses: three methods out of
// the interface's twenty-odd. nats.KeyValue satisfies it structurally, so NewCache still
// hands the real bucket straight in and nothing about production changes.
//
// 🔑 IT IS NARROWED SO THE CACHE CAN BE TESTED AT ALL. Wrapping the full nats.KeyValue
// meant a Cache could only exist with a live JetStream connection behind it, which is why
// nothing in the repository had ever tested a Cache, or any of the decorators built on one
// — and a caching override that silently stopped overriding would have gone on passing
// every test. Depending on the three methods actually called is what makes an in-memory
// double a dozen lines instead of a JetStream server.
type cacheStore interface {
	Put(key string, value []byte) (uint64, error)
	Get(key string) (nats.KeyValueEntry, error)
	Delete(key string, opts ...nats.DeleteOpt) error
}

// NewCacheOver builds a Cache over any store providing the three methods a Cache uses.
//
// It exists for tests: production goes through NewCache, which supplies the real
// JetStream bucket. It is exported because the decorators worth testing this way live in
// the service modules, not in core.
func NewCacheOver(store cacheStore) *Cache {
	return &Cache{kv: store}
}

// NewCache returns a Cache over a JetStream KV bucket named for this instance,
// functional area, and the given cache name, creating it with the given TTL if
// it does not yet exist. The name is sanitized into the bucket-name charset.
func (nmgr *NatsManager) NewCache(name string, ttl time.Duration) (*Cache, error) {
	bucket := CacheBucketName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, name)
	store, err := nmgr.KeyValueStore(name, bucket, ttl)
	if err != nil {
		return nil, err
	}
	return &Cache{kv: store}, nil
}

// Set stores value under key, JSON-encoding it. The entry expires after the
// bucket TTL configured at construction.
func (c *Cache) Set(ctx context.Context, key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.kv.Put(kvKey(key), data)
	return err
}

// Get loads the entry for key into dest (a pointer) and reports whether it was
// present. A miss returns (false, nil); only a transport/decode error returns a
// non-nil error, so callers can degrade a miss-or-error to a DB lookup.
func (c *Cache) Get(ctx context.Context, key string, dest interface{}) (bool, error) {
	entry, err := c.kv.Get(kvKey(key))
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(entry.Value(), dest); err != nil {
		return false, err
	}
	return true, nil
}

// Delete evicts an entry, tolerating a miss. Used to invalidate a cached entry
// on mutation so a stale value is not served (bounded further by the TTL).
func (c *Cache) Delete(ctx context.Context, key string) error {
	if err := c.kv.Delete(kvKey(key)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		return err
	}
	return nil
}
