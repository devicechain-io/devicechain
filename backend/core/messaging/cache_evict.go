// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	nats "github.com/nats-io/nats.go"
)

// MaxCacheEvictionKeys bounds one eviction message. Publish splits a longer list into
// several messages; Subscribe drops a message carrying more as malformed.
const MaxCacheEvictionKeys = 512

// cacheEvictSubjectSegment names the operation, between the instance id and the cache.
const cacheEvictSubjectSegment = ".cache-evict."

// Results of cache_eviction_broadcasts_total, one per thing a broadcast can do with a
// message: send one, fail to send one, apply one it received, or drop one it could not.
const (
	evictionPublished     = "published"
	evictionPublishFailed = "publish_failed"
	evictionReceived      = "received"
	evictionMalformed     = "malformed"
)

// CacheEvictSubject is the subject one cache's evictions are broadcast on, for one
// instance: "$DC.{instance}.cache-evict.{area}.{name}". Both sides derive it here, as
// DetectPurgeSubject is derived, so a publisher and a subscriber cannot disagree about it,
// which would fail as silence. It sits under controlSubjectRoot for the reason
// tenant_purge_detect.go gives: outside every stream's capture space, so no stream can
// store an eviction and replay it later.
func CacheEvictSubject(instanceId, functionalArea, name string) string {
	return controlSubjectRoot + instanceId + cacheEvictSubjectSegment +
		sanitizeName(functionalArea) + "." + sanitizeName(name)
}

// CacheEviction is one broadcast eviction: drop every entry of Tenant filed under any of
// Keys. What a key means is the cache's own business (device-management sends device row
// ids).
type CacheEviction struct {
	Tenant string   `json:"tenant"`
	Keys   []string `json:"keys"`
}

// EvictionBroadcast tells every replica of a service to drop entries from an IN-PROCESS
// cache. It exists because an in-process copy is reachable only by the process holding it:
// Cache.Delete clears the bucket and its own process's memory, and other replicas wait out
// their copy's TTL. A cache that holds nothing in a bucket has no bucket to clear, so
// without this, a change made through one replica would reach the others only by expiry.
//
// 🔑 IT ONLY EVER REMOVES. A message can make a replica drop an entry and read the
// database again, and nothing else. So a lost, duplicated, reordered or forged message can
// cost hit rate and never correctness, which is why fire-and-forget core NATS is enough and
// why the TTL of the cache using it bounds what a lost message costs. A message is lost
// when a replica is disconnected while it is sent, and also when a replica falls far
// enough behind that the client library drops what it has not yet delivered (a slow
// consumer); the TTL bounds both the same way.
//
// The same property is what a flood of evictions costs: a replica receiving many of them
// keeps reading the database, as it would with no cache at all. That is the direction to
// fail in.
//
// The publisher receives its own message too (core NATS echoes to the connection's own
// subscriptions), and applying it is harmless: the entry is already gone.
type EvictionBroadcast struct {
	nc      *nats.Conn
	name    string
	subject string
	m       *streamMetrics // nil-safe through evictionBroadcast

	mu  sync.Mutex
	sub *nats.Subscription
}

// NewEvictionBroadcast builds the broadcast for one named in-process cache of this
// service, on this manager's connection. It makes no broker call; Subscribe does. The
// manager must be initialized (connected) before Publish or Subscribe is called.
func (nmgr *NatsManager) NewEvictionBroadcast(name string) *EvictionBroadcast {
	ms := nmgr.Microservice
	return newEvictionBroadcast(nmgr.nc,
		CacheEvictSubject(ms.InstanceId, ms.FunctionalArea, name), name, nmgr.metrics)
}

func newEvictionBroadcast(nc *nats.Conn, subject, name string, m *streamMetrics) *EvictionBroadcast {
	b := &EvictionBroadcast{nc: nc, name: name, subject: subject, m: m}
	// Every result exists from the start, so a quiet replica exports zeros rather than
	// nothing and an alert on the rate has a series to read.
	for _, r := range []string{evictionPublished, evictionPublishFailed, evictionReceived, evictionMalformed} {
		m.evictionBroadcastInit(name, r)
	}
	return b
}

// Subject is the subject this broadcast publishes and subscribes on.
func (b *EvictionBroadcast) Subject() string { return b.subject }

// Publish sends (tenant, keys), split into messages of at most MaxCacheEvictionKeys keys.
// An empty tenant is an error and sends nothing: every entry is filed under a tenant, so an
// eviction naming none could only ever evict nothing, and that must be loud. No keys is a
// no-op. It sends every chunk and returns the first error. The caller has normally
// committed the change already, so it logs the error and goes on: the TTL of the cache
// bounds what the lost message would have evicted.
func (b *EvictionBroadcast) Publish(tenant string, keys []string) error {
	if tenant == "" {
		return errors.New("a cache eviction names no tenant, so it could evict nothing")
	}
	var first error
	for start := 0; start < len(keys); start += MaxCacheEvictionKeys {
		end := min(start+MaxCacheEvictionKeys, len(keys))
		body, err := json.Marshal(CacheEviction{Tenant: tenant, Keys: keys[start:end]})
		if err == nil {
			err = b.nc.Publish(b.subject, body)
		}
		if err != nil {
			b.m.evictionBroadcast(b.name, evictionPublishFailed)
			if first == nil {
				first = fmt.Errorf("broadcasting a %s eviction: %w", b.name, err)
			}
			continue
		}
		b.m.evictionBroadcast(b.name, evictionPublished)
	}
	return first
}

// Subscribe applies every eviction this instance broadcasts for the cache. It subscribes
// through SubscribeSynced: the subject is published from OTHER replicas' connections,
// which is exactly the case ConfirmSubscribed exists for. apply runs inline on the
// subscription's goroutine and must be cheap. A message that does not decode, names no
// tenant, or carries no keys or more than MaxCacheEvictionKeys is counted as malformed and
// dropped. Calling it a second time is an error.
func (b *EvictionBroadcast) Subscribe(apply func(CacheEviction)) error {
	if apply == nil {
		return errors.New("a cache eviction subscription needs a function to apply evictions")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sub != nil {
		return fmt.Errorf("the %s eviction broadcast is already subscribed", b.name)
	}
	sub, err := SubscribeSynced(b.nc, b.subject, func(msg *nats.Msg) {
		var e CacheEviction
		if json.Unmarshal(msg.Data, &e) != nil || e.Tenant == "" ||
			len(e.Keys) == 0 || len(e.Keys) > MaxCacheEvictionKeys {
			b.m.evictionBroadcast(b.name, evictionMalformed)
			return
		}
		b.m.evictionBroadcast(b.name, evictionReceived)
		apply(e)
	})
	if err != nil {
		return fmt.Errorf("subscribing to %s evictions: %w", b.name, err)
	}
	b.sub = sub
	return nil
}

// Close unsubscribes. It is safe to call when Subscribe was never called, and twice.
func (b *EvictionBroadcast) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sub == nil {
		return nil
	}
	err := b.sub.Unsubscribe()
	b.sub = nil
	return err
}
