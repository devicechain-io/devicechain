// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package msgtest

import (
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
)

// MemoryKV is an in-memory stand-in for the JetStream KV bucket behind a
// messaging.Cache, and it COUNTS what it was asked to do.
//
// 🔴 IT EXISTS BECAUSE NOTHING IN THE REPOSITORY HAD EVER TESTED A CACHE. A Cache used to
// wrap the whole nats.KeyValue interface, so one could not be built without a live
// JetStream connection — which meant the caching decorators layered on top (device
// lookups and a device's tracked relationships on the inbound event-resolution hot path)
// had no tests either. A decorator that silently stopped decorating, by being renamed or
// deleted so its call fell through to the embedded plain API, went on passing everything.
//
// 🔑 THE COUNTERS ARE THE POINT. Asserting a cached call returns the right VALUE proves
// nothing: the plain API returns the right value too, which is exactly why a lost override
// is invisible. What distinguishes them is whether the second call touched the database,
// so the double records reads and writes and the tests assert on those.
type MemoryKV struct {
	mu     sync.Mutex
	values map[string][]byte

	// Gets and Puts count the calls that reached this store.
	Gets, Puts, Deletes int
}

// NewMemoryKV returns an empty in-memory KV store.
func NewMemoryKV() *MemoryKV {
	return &MemoryKV{values: map[string][]byte{}}
}

// NewCache returns a messaging.Cache backed by this store.
func (m *MemoryKV) NewCache() *messaging.Cache { return messaging.NewCacheOver(m) }

func (m *MemoryKV) Put(key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Puts++
	stored := make([]byte, len(value))
	copy(stored, value)
	m.values[key] = stored
	return 1, nil
}

func (m *MemoryKV) Get(key string) (nats.KeyValueEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Gets++
	value, ok := m.values[key]
	if !ok {
		// The real bucket's miss, which messaging.Cache translates into (false, nil).
		// Returning a nil entry and a nil error instead would make a miss look like a hit
		// holding no data, and every cache test would then pass against a broken Get.
		return nil, nats.ErrKeyNotFound
	}
	return memoryEntry{key: key, value: value}, nil
}

func (m *MemoryKV) Delete(key string, _ ...nats.DeleteOpt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Deletes++
	delete(m.values, key)
	return nil
}

// Len reports how many entries the store holds.
func (m *MemoryKV) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.values)
}

// memoryEntry is the nats.KeyValueEntry a MemoryKV hands back. messaging.Cache reads only
// Value(); the rest satisfy the interface.
type memoryEntry struct {
	key   string
	value []byte
}

func (e memoryEntry) Bucket() string             { return "memory" }
func (e memoryEntry) Key() string                { return e.key }
func (e memoryEntry) Value() []byte              { return e.value }
func (e memoryEntry) Revision() uint64           { return 1 }
func (e memoryEntry) Created() time.Time         { return time.Time{} }
func (e memoryEntry) Delta() uint64              { return 0 }
func (e memoryEntry) Operation() nats.KeyValueOp { return nats.KeyValuePut }
