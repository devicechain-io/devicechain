// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type Cache struct {
	kv nats.KeyValue
}

// NewCache returns a Cache over a JetStream KV bucket named for this instance,
// functional area, and the given cache name, creating it with the given TTL if
// it does not yet exist. The name is sanitized into the bucket-name charset.
func (nmgr *NatsManager) NewCache(name string, ttl time.Duration) (*Cache, error) {
	bucket := sanitizeName(fmt.Sprintf("%s_%s_%s",
		nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, name))
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
