// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventtime owns the ONE rule that decides what instant a reported reading
// happened at, so that every surface which displays, stores, queries or evaluates that
// reading answers with the same value.
//
// 🔴 THE RULE IS APPLIED EXACTLY ONCE, AT EVENT RESOLUTION, AND NEVER AGAIN. A resolved
// event carries times that are already resolved and already bounded; a consumer READS a
// time and never computes one. That is deliberate and it is the whole design:
//
//   - The alternative — each consumer applying the rule itself — was what the platform
//     did before, and it produced three different answers from five consumers. The
//     historian used the envelope, the live projection used the sample's own time, and
//     the replay preview clamped with a different tolerance than live detection, which
//     broke the replay-correctness property the authoring canvas is built on.
//   - One knob stays one knob. The tolerance below is configured in exactly one service
//     (the resolver's), not in every service that reads an event.
//   - The sixth consumer is correct without being told anything. There is no rule for it
//     to reimplement and no fallback for it to get wrong.
//
// The value travels immutably from resolution onward, so live evaluation, a replay a week
// later and a stored row agree even across a configuration change.
package eventtime

import "time"

// Effective bounds a device-reported time against the server-stamped processed time plus
// a tolerance, and reports whether the bound was applied.
//
// 🔴 WHY A DEVICE'S CLOCK CANNOT BE TRUSTED AS AN ORDERING KEY. Several shared projections
// advance under a strictly-newer guard driven by this value, and none of them has a repair
// path short of dropping the row:
//
//   - The detection engine's watermark is a monotonic, snapshotted frontier shared by every
//     tenant. One event dated 2099 advances it past every open window and fires every
//     tenant's absence/duration/session timers at once, and the snapshot makes the poisoning
//     survive a restart.
//   - The live connectivity projection advances last-activity the same way, so a single
//     future timestamp pins it forever: the inactivity sweep never fires again and the
//     device can never be seen to go offline. A device can therefore freeze its own
//     presence — which is precisely the signal command delivery is being taught to trust.
//   - The latest-measurement and last-known-position projections are strictly-newer too, so
//     a poisoned row can never be superseded by a real reading.
//
// Only FUTURE skew is bounded. A late or out-of-order reading is a normal fact about
// store-and-forward devices and is left to each consumer's bounded-lateness handling; there
// is no honest ceiling on how old a buffered reading may legitimately be.
//
// The processed time is stamped by the server at ingest and travels immutably in the
// payload, so the bound is deterministic under replay: re-running the same bytes a week
// later yields the same instant it yielded live. Disabled — returning occurred unchanged —
// when the processed time is unset or maxSkew is non-positive.
func Effective(occurred, processed time.Time, maxSkew time.Duration) (time.Time, bool) {
	if processed.IsZero() || maxSkew <= 0 {
		return occurred, false
	}
	if limit := processed.Add(maxSkew); occurred.After(limit) {
		return limit, true
	}
	return occurred, false
}

// ForEntry resolves ONE sample's effective time out of a batch, and is the single place the
// entry-versus-envelope rule is written down.
//
// A message may batch many samples — a store-and-forward device uploading a run of buffered
// readings — and each sample carries the instant IT was taken, while the envelope carries
// the instant the message was assembled. The sample's own time therefore wins whenever it
// is present; the envelope is the fallback for a device that reports a single reading and
// times only the message.
//
// 🔴 The fallback lives HERE and nowhere downstream. A resolved entry always carries a real
// time, so no consumer has a nil to interpret — which is what stops the next consumer from
// inventing a sixth interpretation of an absent value.
func ForEntry(entry *time.Time, envelope, processed time.Time, maxSkew time.Duration) (time.Time, bool) {
	occurred := envelope
	if entry != nil {
		occurred = *entry
	}
	return Effective(occurred, processed, maxSkew)
}
