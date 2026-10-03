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
//
// The AGE refusal (CheckAge) is the one part applied at more than one call site, and that
// does not contradict the rule above, because it decides no time: it refuses a reading
// outright. The event-sources decoder applies it so HTTP can answer 400; device-management
// resolution applies it to every event, which covers every producer that does not decode
// JSON; and the gateway emitter applies it per sample (see CheckAge). All of them read the
// one constant MaxAge and measure against the same immutable ProcessedTime, so they cannot
// disagree. The instant a reading is resolved TO is still decided once, at resolution.
package eventtime

import (
	"errors"
	"fmt"
	"time"
)

// MaxAge is how long before the platform received it a reported time may be. A reading
// dated earlier is refused, never moved to a later time: a clamped time would invent a
// reading at an instant it was not taken.
//
// 🔴 WHY THERE IS A PAST BOUND AT ALL. A device's clock chooses which time partition (chunk)
// of the event store its reading is stored in, and retention is off by default. With no
// floor, one credential could create a partition for every chunk interval back to year 1
// from a few thousand tiny messages, and every operation that touches each partition (an
// upgrade's key rebuild, an index drop, the planner) would then scale with an attacker's
// choice. With the floor, the history a device can create is bounded by the instance's age
// plus MaxAge, in DAYS. How many chunks that is depends on the configured chunk interval,
// so the floor alone keeps no table under any chunk count: the migrations that lock every
// chunk check a ceiling of their own before they lock anything (event-management's
// eventStoreMaxChunks).
//
// 366 days, so a reading exactly one calendar year old is accepted across a leap day. It is
// far above the edge agent's documented outage horizon (hours to a day).
const MaxAge = 366 * 24 * time.Hour

// ErrTooOld marks a reported time more than MaxAge before it was received.
var ErrTooOld = errors.New("event time is older than the platform accepts")

// CheckAge refuses a reported time more than MaxAge before processed, the receipt instant.
// occurred == processed-MaxAge is accepted; one nanosecond earlier is not. A time AFTER
// processed is not this function's business (Effective bounds it).
//
// A zero processed time disables the check, exactly as it disables Effective. Every producer
// stamps receipt: the event-sources decoder (the capture stream's append time where there is
// one, else its own clock at decode), the gateway emitter the Sparkplug and LwM2M services
// share (its own clock), and device-state's presence demotion (its own clock, and it dates
// the demotion now too). So a zero is a test fixture, not a device. A producer added later
// must stamp it as well, or none of its events is ever age-checked.
func CheckAge(occurred, processed time.Time) error {
	if processed.IsZero() {
		return nil
	}
	floor := processed.Add(-MaxAge)
	if occurred.Before(floor) {
		return fmt.Errorf("%w: %s is more than %d days before the platform received it (%s); "+
			"the earliest time accepted for it is %s",
			ErrTooOld, occurred.UTC().Format(time.RFC3339Nano), int(MaxAge/(24*time.Hour)),
			processed.UTC().Format(time.RFC3339Nano), floor.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// Reported is the entry-versus-envelope rule on its own: a sample's own time when it
// reported one, else the message's. ForEntry and every age check read it from here.
func Reported(entry *time.Time, envelope time.Time) time.Time {
	if entry != nil {
		return *entry
	}
	return envelope
}

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
// Only FUTURE skew is CLAMPED here. A late or out-of-order reading is a normal fact about
// store-and-forward devices and is left to each consumer's bounded-lateness handling. A
// reading older than MaxAge is REFUSED instead, by CheckAge. The two directions differ
// because the harms differ: a projection needs SOME instant for a reading whose clock runs
// ahead, while a reading from far in the past would need a partition of the store all of
// its own, so it is not kept at all rather than kept at a time it was not taken.
//
// The processed time is stamped by the server at ingest (the time the platform received
// the event, identical on every redelivery of a captured message) and travels immutably
// in the payload, so the bound is deterministic under replay: re-running the same bytes a week
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
	return Effective(Reported(entry, envelope), processed, maxSkew)
}
