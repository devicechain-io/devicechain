// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"sort"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	maxPlaceholderSessions = 1024
	maxNodeSessions        = 50_000
	placeholderIdleTTL     = 10 * time.Minute
	offlineIdleTTL         = 24 * time.Hour
	maxSeenIDs             = 50_000
)

// SweepIdle forgets only idle non-online sessions. Epochs remain tracker-wide.
func (tr *SessionTracker) SweepIdle() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.sweepIdleLocked(tr.now(), false)
}

// A probe timeout retires a vanished node only if no message raced the verdict.
func (tr *SessionTracker) markProbeOffline(key nodeKey, since time.Time) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s := tr.sessions[key]
	if s == nil || !s.online || !s.lastTouched.Before(since) {
		return
	}
	s.online = false
	s.lastTouched = tr.now()
	tr.nonOnline++
	s.aliases = map[uint64]string{}
	s.types = birthTypes{}
	s.devices = map[string]uint64{}
	tr.clearMetrics(s)
}

func (c *Client) warnCapacity(stamp *atomic.Int64) bool {
	now := c.now().UnixNano()
	last := stamp.Load()
	return (last == 0 || now-last >= int64(recoveryLogQuiet)) && stamp.CompareAndSwap(last, now)
}

func (c *Client) countSessionRefusal(reason string) {
	if reason == "" {
		return
	}
	if c.metrics.SessionRefused != nil {
		c.metrics.SessionRefused.WithLabelValues(reason).Inc()
	}
	if c.warnCapacity(&c.capacityWarnedAt) {
		log.Warn().Str("tenant", c.tenant).Str("source", c.policy.Source).Str("reason", reason).
			Msg("Sparkplug tracking capacity exhausted; refusing new identity.")
	}
}

func (tr *SessionTracker) sweepIdleLocked(now time.Time, placeholdersOnly bool) {
	for key, s := range tr.sessions {
		if s.online || (placeholdersOnly && s.born) {
			continue
		}
		ttl := offlineIdleTTL
		if !s.born {
			ttl = placeholderIdleTTL
		}
		if now.Sub(s.lastTouched) >= ttl {
			tr.deleteSessionLocked(key)
		}
	}
}

func (tr *SessionTracker) deleteSessionLocked(key nodeKey) {
	s := tr.sessions[key]
	if !s.born {
		tr.placeholders--
	}
	if !s.online {
		tr.nonOnline--
	}
	delete(tr.sessions, key)
}

// Evict a batch so a full tracker does not sort on every newly born node.
func (tr *SessionTracker) evictOfflineLocked() {
	keys := make([]nodeKey, 0, tr.nonOnline)
	for key, s := range tr.sessions {
		if !s.online {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return tr.sessions[keys[i]].lastTouched.Before(tr.sessions[keys[j]].lastTouched)
	})
	if len(keys) > 64 {
		keys = keys[:64]
	}
	for _, key := range keys {
		tr.deleteSessionLocked(key)
	}
}
