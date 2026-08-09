// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"container/list"
	"strconv"
	"sync"
)

// Default capacities for the two levels of the undeclared-location memo. Both are
// sized by what they actually key on, not by fleet size, and neither is a tuning
// knob a user sees — they are ceilings on memory, chosen so the failure mode of
// overflowing them is a repeated log line rather than a missed one.
const (
	// undeclaredLocationAnswerCapacity bounds the cache of "does this (device type,
	// profile version) declare a location?". It keys on device TYPES, a schema-scale
	// quantity — a tenant with a hundred device types is a large tenant — so this
	// holds many versions of many types with room to spare.
	undeclaredLocationAnswerCapacity = 512
	// undeclaredLocationWarnCapacity bounds the "already warned about this (device,
	// profile version)" set. It is fleet-scale, but only for the MISCONFIGURED part of
	// a fleet: an entry is created only for a device whose profile does NOT declare a
	// location, so a correctly-declared fleet of any size never allocates one.
	undeclaredLocationWarnCapacity = 4096
)

// undeclaredLocationMemo bounds the "device reported a position its profile never
// declared" warning to at most one line per (device, profile version) — the ADR-078
// volume rule. Without it a 600-device fleet reporting position at 1 Hz would emit
// 600 warnings per second, which is not a louder warning but a silent one: nobody
// reads a log that scrolls.
//
// The profile VERSION is part of the key rather than just the profile, because
// re-publishing a profile is exactly the moment an operator would want to be told
// again — they just edited the thing the warning is about, and a warning suppressed
// forever by the first event a device ever sent would never resurface to say the edit
// did not fix it.
//
// # Two levels, and why the split is load-bearing
//
// answers caches the DECLARATION lookup, keyed by (device type, profile version).
// That key is correct by construction rather than by invalidation: publishing,
// rolling back, renaming the profile, or re-pointing the type at a different profile
// all change the version token, so a stale answer is unreachable rather than wrong.
// Without this level the resolver would issue a database read for every location
// event of every device whose profile is undeclared-and-unpublished — a per-event
// query on the hot path, added by a logging feature.
//
// warned is the warn-once set, keyed by (tenant, device, profile version), and it is
// reached ONLY when the answer above is "undeclared". That is what keeps it small: a
// fleet whose profiles all declare position never inserts into it at all.
//
// # Eviction, and what it costs
//
// Both levels are LRU with a fixed capacity, because devices are unbounded over time
// — a fleet churns, and a plain map keyed by device token is a slow leak in a process
// that is meant to run for months. Eviction is safe HERE in a way it would not be in
// a cache of authoritative data: the only consequence of evicting an entry is that a
// warning is printed a second time. It can never cause a warning to be MISSED, and it
// can never affect whether an event is stored. Given the choice between a bounded
// structure that occasionally repeats itself and an unbounded one that is always
// exactly right, a log-volume feature takes the bound.
//
// Tenancy: device tokens are unique per tenant, not globally, so the tenant is part
// of the warn key. Two tenants with a device called "gateway-1" are two devices, and
// muting one because the other was already reported would hide a real gap.
type undeclaredLocationMemo struct {
	answers *lruSet[bool]
	warned  *lruSet[struct{}]
}

// newUndeclaredLocationMemo builds the shared memo. One instance is created per
// process and handed to EVERY resolver worker: the resolvers run as a pool over one
// channel, so a per-worker memo would warn once per worker — the same misconfiguration
// reported as many times as the pool is wide, which is the bug this type exists to
// prevent, just scaled down by a constant.
func newUndeclaredLocationMemo() *undeclaredLocationMemo {
	return &undeclaredLocationMemo{
		answers: newLruSet[bool](undeclaredLocationAnswerCapacity),
		warned:  newLruSet[struct{}](undeclaredLocationWarnCapacity),
	}
}

// declarationAnswerKey identifies the (device type, active profile version) pair a
// cached declaration answer belongs to. The tenant is not needed: a device type row
// id is already globally unique, and the version token is derived from that same row.
func declarationAnswerKey(deviceTypeId uint, profileVersionToken string) string {
	return strconv.FormatUint(uint64(deviceTypeId), 10) + "|" + profileVersionToken
}

// warnKey identifies the (tenant, device, profile version) a warning belongs to.
func warnKey(tenant, deviceToken, profileVersionToken string) string {
	return tenant + "|" + deviceToken + "|" + profileVersionToken
}

// declared returns a cached answer to "does this (device type, profile version)
// declare a location?", and whether one was cached at all.
func (m *undeclaredLocationMemo) declared(deviceTypeId uint, profileVersionToken string) (bool, bool) {
	return m.answers.get(declarationAnswerKey(deviceTypeId, profileVersionToken))
}

// rememberDeclared caches the answer for a (device type, profile version).
func (m *undeclaredLocationMemo) rememberDeclared(deviceTypeId uint, profileVersionToken string, declared bool) {
	m.answers.put(declarationAnswerKey(deviceTypeId, profileVersionToken), declared)
}

// alreadyWarned reports whether this (tenant, device, profile version) has been
// warned about, WITHOUT recording it. It is the cheap hot-path gate: an event whose
// pair is already known short-circuits before any lookup runs.
func (m *undeclaredLocationMemo) alreadyWarned(tenant, deviceToken, profileVersionToken string) bool {
	_, found := m.warned.get(warnKey(tenant, deviceToken, profileVersionToken))
	return found
}

// claimWarning records this (tenant, device, profile version) and reports whether the
// caller is the one that claimed it. Exactly one caller gets true, so two resolver
// workers handling two events from the same device concurrently produce one warning
// between them rather than one each.
func (m *undeclaredLocationMemo) claimWarning(tenant, deviceToken, profileVersionToken string) bool {
	return m.warned.putIfAbsent(warnKey(tenant, deviceToken, profileVersionToken), struct{}{})
}

// lruSet is a small fixed-capacity LRU map, safe for concurrent use by the resolver
// worker pool. It is deliberately local and minimal: the memo needs get / put /
// put-if-absent and a hard bound, and nothing else — no TTL, no metrics, no eviction
// callbacks — so a dependency (or the NATS KV cache the API layer uses, which is a
// network hop) would cost far more than it saves.
type lruSet[V any] struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	order    *list.List // front = most recently used
}

// lruEntry is what the list holds, carrying its own key so an eviction from the back
// of the list can also remove the map entry that points at it.
type lruEntry[V any] struct {
	key   string
	value V
}

func newLruSet[V any](capacity int) *lruSet[V] {
	if capacity < 1 {
		capacity = 1
	}
	return &lruSet[V]{
		capacity: capacity,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// get returns the value for key and whether it was present, marking it most recently
// used on a hit.
func (l *lruSet[V]) get(key string) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.order.MoveToFront(el)
		return el.Value.(*lruEntry[V]).value, true
	}
	var zero V
	return zero, false
}

// put stores a value, replacing any existing one, and evicts the least recently used
// entry if that pushed the set over capacity.
func (l *lruSet[V]) put(key string, value V) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.setLocked(key, value)
}

// putIfAbsent stores a value only if the key is absent, returning true when it did.
// The check and the insert happen under one lock, which is what makes it usable as a
// "claim exactly once" primitive across the worker pool — a get-then-put pair would
// let two workers both observe absence and both claim.
func (l *lruSet[V]) putIfAbsent(key string, value V) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.order.MoveToFront(el)
		return false
	}
	l.setLocked(key, value)
	return true
}

// setLocked inserts or replaces an entry and enforces the capacity bound. The caller
// holds the lock.
func (l *lruSet[V]) setLocked(key string, value V) {
	if el, ok := l.entries[key]; ok {
		el.Value.(*lruEntry[V]).value = value
		l.order.MoveToFront(el)
		return
	}
	l.entries[key] = l.order.PushFront(&lruEntry[V]{key: key, value: value})
	for l.order.Len() > l.capacity {
		oldest := l.order.Back()
		if oldest == nil {
			return
		}
		l.order.Remove(oldest)
		delete(l.entries, oldest.Value.(*lruEntry[V]).key)
	}
}

// len reports how many entries are held. Used by tests to prove the bound holds.
func (l *lruSet[V]) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
