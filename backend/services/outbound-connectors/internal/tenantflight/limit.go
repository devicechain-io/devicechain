// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tenantflight reserves worker capacity for other tenants during slow sends.
package tenantflight

import (
	"context"
	"sync"
	"time"
)

// Limit tracks only tenants with active work, so idle tenants consume no memory.
type Limit struct {
	mu      sync.Mutex
	share   int
	total   int
	active  map[string]int
	changed chan struct{}
}

// New gives each tenant half the pool while another tenant holds a slot.
// A tenant running alone may use the entire worker pool.
// A one-worker pool necessarily serializes all tenants.
func New(workers int) *Limit {
	return &Limit{share: max(1, workers/2), active: make(map[string]int), changed: make(chan struct{})}
}

// TryAcquire reports whether this tenant can start work immediately.
func (l *Limit) TryAcquire(tenant string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[tenant] >= l.share && l.total-l.active[tenant] > 0 {
		return false
	}
	l.active[tenant]++
	l.total++
	return true
}

// Wait keeps a refused message on its current delivery until capacity changes.
// Progress renews the broker's AckWait, so contention cannot exhaust MaxDeliver.
func (l *Limit) Wait(ctx context.Context, tenant string, progress func() error) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := progress(); err != nil {
			return err
		}
		l.mu.Lock()
		changed := l.changed
		l.mu.Unlock()
		if l.TryAcquire(tenant) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-ticker.C:
		}
	}
}

// Release returns an admitted slot on every completion path, including failures.
func (l *Limit) Release(tenant string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active[tenant]--
	l.total--
	if l.active[tenant] == 0 {
		delete(l.active, tenant)
	}
	close(l.changed)
	l.changed = make(chan struct{})
}
