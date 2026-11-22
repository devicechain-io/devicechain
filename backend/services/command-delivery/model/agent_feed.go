// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"sync"
	"time"
)

const DefaultKillFeedTTL = 30 * time.Second

// KillFeed is process-local evidence that the kill consumer can still reach the broker.
type KillFeed struct {
	mu   sync.RWMutex
	last time.Time
	ttl  time.Duration
	now  func() time.Time
	// latched is set when a kill could not be applied by its final delivery: the broker then
	// moves past it, so no later poll may vouch for the feed again in this process.
	latched bool
}

func NewKillFeed(ttl time.Duration, now func() time.Time) *KillFeed {
	if ttl <= 0 {
		ttl = DefaultKillFeedTTL
	}
	if now == nil {
		now = time.Now
	}
	return &KillFeed{ttl: ttl, now: now}
}

func (f *KillFeed) Now() time.Time {
	if f == nil || f.now == nil {
		return time.Now()
	}
	return f.now()
}
func (f *KillFeed) Beat(now time.Time) { f.mu.Lock(); defer f.mu.Unlock(); f.last = now }

// Latch makes the feed stale for the rest of this process, whatever later polls see. It is
// called when a kill was not applied by its final delivery: JetStream then advances the ack
// floor past it (MaxDeliver), so an idle durable would otherwise read "caught up" and vouch
// for a fence that never closed. Execution and agent-command dispatch stay refused until the
// process restarts; the dead-lettered kill tells an operator to repeat it.
func (f *KillFeed) Latch() { f.mu.Lock(); defer f.mu.Unlock(); f.latched = true }

// Latched reports whether Latch was called.
func (f *KillFeed) Latched() bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.latched
}
func (f *KillFeed) Fresh(now time.Time) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.latched {
		return false
	}
	ttl := f.ttl
	if ttl <= 0 {
		ttl = DefaultKillFeedTTL
	}
	return !f.last.IsZero() && !now.Before(f.last) && now.Sub(f.last) < ttl
}
