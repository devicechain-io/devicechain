// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package msgtest

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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

	// Gets, Puts and Deletes count the calls that reached this store.
	Gets, Puts, Deletes int
}

// NewMemoryKV returns an empty in-memory KV store.
func NewMemoryKV() *MemoryKV {
	return &MemoryKV{values: map[string][]byte{}}
}

// NewCache returns a messaging.Cache backed by this store.
func (m *MemoryKV) NewCache() *messaging.Cache { return messaging.NewCacheOver(m) }

func (m *MemoryKV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Puts++
	stored := make([]byte, len(value))
	copy(stored, value)
	m.values[key] = stored
	return 1, nil
}

func (m *MemoryKV) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Gets++
	value, ok := m.values[key]
	if !ok {
		// The real bucket's miss, which messaging.Cache translates into (false, nil).
		// Returning a nil entry and a nil error instead would make a miss look like a hit
		// holding no data, and every cache test would then pass against a broken Get.
		return nil, jetstream.ErrKeyNotFound
	}
	return memoryEntry{key: key, value: value}, nil
}

func (m *MemoryKV) Delete(ctx context.Context, key string, _ ...jetstream.KVDeleteOpt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

// memoryEntry is the jetstream.KeyValueEntry a MemoryKV hands back. messaging.Cache reads only
// Value(); the rest satisfy the interface.
type memoryEntry struct {
	key   string
	value []byte
}

func (e memoryEntry) Bucket() string                  { return "memory" }
func (e memoryEntry) Key() string                     { return e.key }
func (e memoryEntry) Value() []byte                   { return e.value }
func (e memoryEntry) Revision() uint64                { return 1 }
func (e memoryEntry) Created() time.Time              { return time.Time{} }
func (e memoryEntry) Delta() uint64                   { return 0 }
func (e memoryEntry) Operation() jetstream.KeyValueOp { return jetstream.KeyValuePut }

// SilentKV is a store that, once armed, never answers until the caller gives up: every
// Get and Put blocks until its ctx is done and returns ctx.Err(), the way a request routed
// to a replica that has gone silent does. Before Arm it delegates to the embedded
// MemoryKV, so a rig can warm up; Delete always does.
//
// 🔴 IT ALSO GIVES UP BY ITSELF AFTER 5 s, with the JetStream request timeout's error,
// because that is what production did. A Cache that stopped passing its context down then
// waits 5 s per call and fails a test by its numbers, instead of hanging it forever.
type SilentKV struct {
	*MemoryKV

	armed atomic.Bool
	// Silenced counts the calls it swallowed.
	Silenced atomic.Int64
}

// Arm switches the silence on.
func (s *SilentKV) Arm() { s.armed.Store(true) }

// Disarm switches it off again: the store answers from the embedded MemoryKV.
func (s *SilentKV) Disarm() { s.armed.Store(false) }

func (s *SilentKV) silence(ctx context.Context) error {
	s.Silenced.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return nats.ErrTimeout
	}
}

func (s *SilentKV) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	if s.armed.Load() {
		return nil, s.silence(ctx)
	}
	return s.MemoryKV.Get(ctx, key)
}

func (s *SilentKV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	if s.armed.Load() {
		return 0, s.silence(ctx)
	}
	return s.MemoryKV.Put(ctx, key, value)
}
