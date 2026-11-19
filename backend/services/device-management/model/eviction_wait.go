// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/rs/zerolog/log"
)

// DefaultEvictionWait is how long a write that has committed waits for the cache evictions it
// owes before it answers. A healthy bucket answers an eviction in milliseconds and a leader
// move in about a second, so the wait is almost never reached. What it bounds is a broker that
// does not answer at all: every eviction is a bucket write bounded by its own timeout (5 s per
// entry, and a profile publish evicts two entries per adopting device type, one after another),
// and without this bound a delete made while the service was cut off from the broker held its
// caller for the sum of them and answered an error for a change that had been saved.
const DefaultEvictionWait = 2 * time.Second

// maxDetachedEvictions bounds the evictions running at once in this process. A healthy eviction
// takes milliseconds, so the bound is reached only while the broker does not answer, when each one
// can run for as long as its bucket deletes time out (5 s per entry, and a fan-out deletes many).
// Past it an eviction is not started at all: its entries are served until their time to live, the
// same bound a failed bucket delete leaves, and a long outage cannot pile up goroutines.
const maxDetachedEvictions = 256

// evictionSlots admits the evictions maxDetachedEvictions allows.
var evictionSlots = make(chan struct{}, maxDetachedEvictions)

// evictionReadTimeout bounds the database read an eviction makes to learn what to evict (the
// device types of a profile or a tenant). The eviction runs on a context its caller cannot
// cancel, so without this a stalled read would hold it for good.
const evictionReadTimeout = 30 * time.Second

// evictAfterCommit runs evict, the cache evictions a committed write owes, and waits for it at
// most capi.evictionWait. Past that the write answers and the eviction carries on by itself:
// it runs on a context its caller cannot cancel (each bucket delete is bounded by its own
// timeout, see messaging.Cache.Delete), so it finishes or fails exactly as it would have with
// the caller still waiting.
//
// 🔴 A MISSED OR LATE EVICTION IS THE BOUNDED STALENESS EVERY CACHE HERE ALREADY HAS: the entry
// is served until its time to live, a failed bucket delete is logged and counted by the cache,
// and the change itself is in the database either way. Holding the caller does not make the
// eviction any likelier to land; it only turns a saved change into an error the caller reads as
// "not saved". On a healthy broker the eviction finishes well inside the wait, so the replica
// that made a change still drops its own copy before the change is answered.
func (capi *CachedApi) evictAfterCommit(ctx context.Context, what string, evict func(context.Context)) {
	ctx = context.WithoutCancel(ctx)
	select {
	case evictionSlots <- struct{}{}:
	default:
		log.Warn().Str("eviction", what).Int("running", maxDetachedEvictions).
			Msg("Too many cache evictions are still running (the broker is not answering them), so this one was " +
				"not started; the entries it would have removed are served until their time to live.")
		return
	}
	done := make(chan struct{})
	go func() {
		defer func() { <-evictionSlots }()
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Str("eviction", what).Str("stack", string(debug.Stack())).
					Msg("A cache eviction owed by a committed change panicked; the entries expire at their time to live.")
			}
		}()
		evict(ctx)
	}()
	wait := capi.evictionWait
	if wait <= 0 {
		wait = DefaultEvictionWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Warn().Str("eviction", what).Dur("waited", wait).
			Msg("A cache eviction owed by a committed change has not finished; the change is answered without " +
				"it, and the eviction carries on in the background (an entry it misses is served until its time to live).")
	}
}
