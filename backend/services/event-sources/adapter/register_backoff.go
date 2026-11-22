// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"sync"
	"time"
)

// A short negative backoff for a device whose auto-registration keeps failing.
//
// 🔴 WHY. A create that fails and leaves no device behind (a device type that passes the grammar
// check but names no existing type, a create device-management refuses) was retried on EVERY
// message from that device, each one spending a lookup, a create and a re-lookup against
// device-management and the caller's bounded retry budget. One config mistake on one device
// throttled the whole source. Now the device waits out a window: messages inside it are a
// definitive drop (ResolveRegisterBackoff), with no call to device-management, and the window
// doubles while the failures continue, up to registerBackoffMax.
//
// It is PER (tenant, external id), so no device waits on another's mistake, and it only ever
// follows a create that failed WITH device-management answering the lookups: an unreachable
// device-management fails the lookup first and is still returned as a retryable error.
//
// It is BOUNDED. A table full of other devices' entries admits no new one: that device simply
// gets no backoff, which is the behaviour before this existed, and nothing is evicted to make
// room. Entries long past their window are pruned whenever the table is full.
const (
	// registerBackoffBase is the first window after a failed create.
	registerBackoffBase = 10 * time.Second
	// registerBackoffMax caps the window while the failures continue.
	registerBackoffMax = 5 * time.Minute
	// registerBackoffMaxEntries caps the table.
	registerBackoffMaxEntries = 10_000
)

type registerBackoff struct {
	mu sync.Mutex
	m  map[backoffKey]backoffEntry
}

type backoffKey struct{ tenant, externalId string }

type backoffEntry struct {
	until time.Time
	delay time.Duration
}

func newRegisterBackoff() *registerBackoff {
	return &registerBackoff{m: map[backoffKey]backoffEntry{}}
}

// blocked reports whether the device is inside its window at now, and until when.
func (b *registerBackoff) blocked(tenant, externalId string, now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[backoffKey{tenant, externalId}]
	if !ok || !now.Before(e.until) {
		return time.Time{}, false
	}
	return e.until, true
}

// fail records a failed create at now and returns the end of the device's new window. The window
// doubles from the previous one while the device keeps failing; one forgotten (long past its
// window) starts again at registerBackoffBase.
func (b *registerBackoff) fail(tenant, externalId string, now time.Time) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := backoffKey{tenant, externalId}
	e, ok := b.m[k]
	if ok && forgotten(e, now) {
		ok = false
	}
	delay := registerBackoffBase
	if ok {
		if delay = e.delay * 2; delay > registerBackoffMax {
			delay = registerBackoffMax
		}
	}
	if !ok && len(b.m) >= registerBackoffMaxEntries {
		for key, old := range b.m {
			if forgotten(old, now) {
				delete(b.m, key)
			}
		}
		if len(b.m) >= registerBackoffMaxEntries {
			return time.Time{}
		}
	}
	until := now.Add(delay)
	b.m[k] = backoffEntry{until: until, delay: delay}
	return until
}

// forgotten is an entry whose window ended more than registerBackoffMax ago: the device stopped
// failing (or stopped sending), and its next failure starts from the base again.
func forgotten(e backoffEntry, now time.Time) bool {
	return now.After(e.until.Add(registerBackoffMax))
}

// clear drops the device's entry after it resolved.
func (b *registerBackoff) clear(tenant, externalId string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, backoffKey{tenant, externalId})
}

// invalidateTenant drops every entry of a deleted tenant.
func (b *registerBackoff) invalidateTenant(tenant string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.m {
		if k.tenant == tenant {
			delete(b.m, k)
		}
	}
}

func (b *registerBackoff) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}
