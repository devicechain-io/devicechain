// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tenantflight reserves worker capacity for other tenants during slow sends.
package tenantflight

import "sync"

// Limit tracks only tenants with active work, so idle tenants consume no memory.
type Limit struct {
	mu     sync.Mutex
	share  int
	active map[string]int
}

// New gives each tenant half the pool (rounded down), with at least one slot.
// A one-worker pool necessarily serializes all tenants.
func New(workers int) *Limit {
	return &Limit{share: max(1, workers/2), active: make(map[string]int)}
}

// TryAcquire never waits: excess work must return its reader/worker slot promptly.
func (l *Limit) TryAcquire(tenant string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[tenant] >= l.share {
		return false
	}
	l.active[tenant]++
	return true
}

// Release returns an admitted slot on every completion path, including failures.
func (l *Limit) Release(tenant string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active[tenant]--
	if l.active[tenant] == 0 {
		delete(l.active, tenant)
	}
}
