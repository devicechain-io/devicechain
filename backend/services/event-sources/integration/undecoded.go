// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Undecoded batches the "this uplink had nothing to read" markers an integration carries, so
// the write to device-management happens at most about once a minute per integration however
// many uplinks arrive.
//
// 🔴 A COUNT THAT IS NOT WRITTEN IS NOT LOST SILENTLY. A flush that fails keeps its tally for the
// next attempt; only the bound on how many integrations are held (a sender chose their names)
// drops a tally, and that drop is counted by Dropped.
type Undecoded struct {
	backend  Backend
	interval time.Duration
	max      int
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]*undecodedEntry
	dropped func()
}

type undecodedEntry struct {
	tenant, integration, bearer string
	count                       int
	sample                      string
	lastFlush                   time.Time
	flushing                    bool
}

// NewUndecoded builds the recorder. interval <= 0 means a minute; dropped, when non-nil, is
// called for each tally refused because too many integrations are pending.
func NewUndecoded(backend Backend, interval time.Duration, now func() time.Time, dropped func()) *Undecoded {
	if interval <= 0 {
		interval = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Undecoded{backend: backend, interval: interval, max: 1024, now: now,
		pending: make(map[string]*undecodedEntry), dropped: dropped}
}

// Note records one undecoded uplink. The first one for an integration is written at once (an
// operator looking at a new integration sees it), later ones are batched. The bearer token is
// kept only until the batch is written, because device-management authenticates the write with
// it.
func (u *Undecoded) Note(tenant, integration, bearer, sampleHex string) {
	key := tenant + "\x00" + integration
	u.mu.Lock()
	e, ok := u.pending[key]
	if !ok {
		if len(u.pending) >= u.max {
			for k, old := range u.pending {
				if old.count == 0 && !old.flushing {
					delete(u.pending, k)
				}
			}
		}
		if len(u.pending) >= u.max {
			u.mu.Unlock()
			if u.dropped != nil {
				u.dropped()
			}
			return
		}
		e = &undecodedEntry{tenant: tenant, integration: integration}
		u.pending[key] = e
	}
	e.bearer = bearer
	e.count++
	if sampleHex != "" {
		e.sample = sampleHex
	}
	due := !e.flushing && u.now().Sub(e.lastFlush) >= u.interval
	if due {
		e.flushing = true
	}
	u.mu.Unlock()
	if due {
		go u.flush(e)
	}
}

// Run flushes tallies that are due until ctx ends, so a tally noted just after a flush is
// written without waiting for the next uplink.
func (u *Undecoded) Run(ctx context.Context) {
	t := time.NewTicker(u.interval / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			u.flushDue()
		}
	}
}

func (u *Undecoded) flushDue() {
	u.mu.Lock()
	var due []*undecodedEntry
	for _, e := range u.pending {
		if e.count > 0 && !e.flushing && u.now().Sub(e.lastFlush) >= u.interval {
			e.flushing = true
			due = append(due, e)
		}
	}
	u.mu.Unlock()
	for _, e := range due {
		u.flush(e)
	}
}

func (u *Undecoded) flush(e *undecodedEntry) {
	u.mu.Lock()
	count, sample, bearer := e.count, e.sample, e.bearer
	u.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := u.backend.RecordUndecoded(ctx, e.tenant, e.integration, bearer, count, sample)

	u.mu.Lock()
	defer u.mu.Unlock()
	e.flushing = false
	e.lastFlush = u.now()
	if err != nil {
		log.Warn().Err(err).Str("integration", e.integration).
			Msg("Could not record undecoded uplinks on the integration; the tally is kept for the next attempt.")
		return
	}
	e.count -= count
	if e.count <= 0 {
		// Nothing left to write: the bearer token goes, the entry stays so the next uplink is
		// held to the interval. Idle entries are evicted when the table fills (see Note).
		e.count, e.bearer = 0, ""
	}
}
