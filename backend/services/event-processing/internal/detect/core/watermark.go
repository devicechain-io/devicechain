// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

// watermark is the engine's logical clock: a single, monotonic event-time frontier with
// bounded out-of-orderness. observe(t) folds an event (or, in live operation, an idle
// wall-clock) timestamp into the frontier, holding it `lateness` behind the maximum
// timestamp ever seen so a bounded amount of out-of-order arrival still lands before its
// window/timer closes. The frontier never moves backward, so replaying the log re-derives
// an identical sequence of frontier values — the property every timer and window firing
// depends on for replay-correctness.
//
// One GLOBAL frontier (the max over all keys) is correct for the single durable stream
// this singleton owns, and it is deliberate: an idle key's absence/session timers SHOULD
// fire off other keys' progress — that is exactly how "this device stopped reporting" is
// detected while the rest of the fleet keeps talking. A per-source MIN watermark only
// becomes necessary when DETECT is sharded across independently-progressing partitions
// (the post-GA intra-Instance fleet, ADR-052); it is deferred, not overlooked.
type watermark struct {
	now      time.Time
	lateness time.Duration
}

// observe advances the frontier to max(now, t-lateness) and reports whether it moved.
// It is the only path that mutates logical time, so bounded-out-of-orderness and
// monotonicity are enforced in exactly one place.
func (w *watermark) observe(t time.Time) bool {
	cand := t.Add(-w.lateness)
	if cand.After(w.now) {
		w.now = cand
		return true
	}
	return false
}
