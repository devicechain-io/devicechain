// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package presence holds the pure ordering predicate for authoritative device
// presence transitions (ADR-067). It is shared by the two independent consumers of a
// resolved StateChange event — the device-state projection (which writes the
// connect/disconnect fields) and the event-processing DETECT engine (which raises/
// resolves a "device offline" alarm) — so both order a transition IDENTICALLY. A
// hand-mirrored copy diverges immediately: a session's BIRTH and its DEATH share one
// SessionId (a Sparkplug NDEATH will is built at connect time), so a naive
// "newer session wins" rule rejects every normal death. Decide encodes the full rule:
// a newer session supersedes; within a session the newer time wins; at an equal stamp
// DISCONNECTED wins over CONNECTED; a same-state re-apply is not a state change.
//
// One deliberate exception to "newer session supersedes" exists, and it is the only
// way a LOWER session is ever accepted: a producer that can name the exact session the
// projection is holding may compare-and-set against it. See Incoming.ExpectedSessionId.
package presence

import "time"

// Prior is the last-applied transition for a device (the projection's stored
// SessionId/PresenceTime/Active, or the DETECT engine's per-series cursor). HasTime is
// false before any transition has been applied (the first-transition case).
type Prior struct {
	SessionId uint64
	Time      time.Time
	HasTime   bool
	Connected bool
}

// Incoming is one transition as claimed by a producer, weighed against a Prior.
//
// It is a struct rather than positional arguments for one specific reason: SessionId
// and ExpectedSessionId are both uint64 and mean nearly opposite things — "the session
// I am reporting" versus "the session I believe you are holding". Adjacent, they are a
// transposition waiting to happen, and a transposed pair still compiles. Named fields
// make that mistake unwritable.
type Incoming struct {
	// SessionId is the session this transition is ABOUT — the connection whose birth
	// or death is being reported.
	SessionId uint64

	// ExpectedSessionId is a compare-and-set precondition, and it is how a producer
	// reports a connection whose session id went BACKWARDS.
	//
	// 🔴 WHY A LOWER SESSION CAN BE THE TRUTH. A session id is minted from the wall
	// clock of whichever broker node the device landed on. A device reconnecting onto a
	// node whose clock trails its peers gets a genuinely-current session id LOWER than
	// the one the projection is holding, so the ordinary "higher wins" rule rejects
	// every event about the live connection — permanently, since no later event about
	// that connection carries a higher id either.
	//
	// A producer that has READ the projection can break the tie with evidence instead of
	// with the id: "I am reporting session S, and I observed you holding session E."
	// If the projection still holds E, the read has not been overtaken and the report is
	// applied, re-filing the device under the session it is actually on. If it holds
	// anything else — a higher session, or an already-applied repair — the precondition
	// fails and nothing happens.
	//
	// Zero means "no claim", which is the default and therefore the behaviour of every
	// producer that does not set it. That default is load-bearing: zero is also a
	// legitimate Prior.SessionId (an INFERRED row has never been told a session), so a
	// rule that let zero match zero would open this exception to exactly the emitters
	// that never made a claim.
	ExpectedSessionId uint64

	// OccurredAt is the instant the transition happened, on the producing clock.
	OccurredAt time.Time

	// Connected is the state being reported: true for a birth, false for a death.
	Connected bool
}

// Decision splits the two effects that the old fused guard conflated. Ordered is the
// stale-guard (is the incoming transition not older than Prior?). Flipped reports
// whether the device's connectivity STATE actually changes — the semantic edge that a
// projection writes as a (dis)connect timestamp and that DETECT raises/resolves on.
// NewSession reports whether the transition moves the device onto a DIFFERENT session;
// because a different SessionId is by construction a different physical connection, a
// same-state CONNECT on a NewSession is still a genuine reconnect (it must refresh
// LastConnectTime), whereas a same-state DISCONNECT is a late echo about an
// already-dead device (it must not move LastDisconnectTime). NewSession always implies
// Ordered — structurally, since it is computed from Ordered.
type Decision struct {
	Ordered    bool
	NewSession bool
	Flipped    bool
}

// Decide evaluates an incoming transition against Prior. It is pure so both consumers
// and their unit tests can call it off the DB.
//
// 🔴 IT IS NO LONGER ORDER-INDEPENDENT, AND THE COMMENT THAT SAID SO WAS TRUE BEFORE
// COMPARE-AND-SET EXISTED. Without ExpectedSessionId the rule is a monotone join — a
// max over (session, time) with a death-wins tiebreak — so any interleaving of the same
// events converges to the same state. A compare-and-set cannot be that, by definition:
// its whole purpose is to depend on what the state currently is. device-state merges
// with five parallel workers and no per-device ordering
// (device-state/processor/StateProcessor.go), so the property that actually has to hold
// there is not commutativity but SAFETY UNDER REORDERING, which this rule keeps:
//
//   - a compare-and-set never applies over a HIGHER session, so it cannot lose a newer
//     connection;
//   - it never applies over a later stamp WITHIN the expected session, so it cannot
//     regress the cursor;
//   - it is idempotent on redelivery — once applied, the projection holds the reported
//     session, which is by construction not the expected one, so the replay fails the
//     precondition.
//
// What is genuinely given up is convergence *timing*: a repair and a true death that
// cross in flight can settle either way, and the losing case is corrected by the next
// reconcile pass rather than by the fold itself. That residual is bounded and
// self-healing, which "order-independent" would have promised away.
func Decide(prior Prior, in Incoming) Decision {
	ordered := isOrdered(prior, in)
	return Decision{
		Ordered: ordered,
		// A different session is a different physical connection, whichever direction
		// the id moved. Gating on Ordered is what keeps the "NewSession implies Ordered"
		// invariant true now that a lower session can be accepted; before
		// compare-and-set, "higher than prior" implied Ordered on its own.
		NewSession: ordered && in.SessionId != prior.SessionId,
		Flipped:    ordered && in.Connected != prior.Connected,
	}
}

// isOrdered is the monotonic stale-guard, mirroring the alarm integrator's discipline:
// a newer session always supersedes (even at an earlier wall clock — a producer may
// mint on a different host's clock); within the same session the newer OccurredAt wins;
// the first transition in a session applies; at an equal (session, time) only a
// DISCONNECTED over a currently-CONNECTED device applies (a session that births and
// dies at one instant is net-dead), so CONNECTED-over-DISCONNECTED and any same-state
// re-apply are rejected. A LOWER session is rejected unless it carries a satisfied
// compare-and-set — see acceptsRegressedSession.
func isOrdered(prior Prior, in Incoming) bool {
	if in.SessionId != prior.SessionId {
		if in.SessionId > prior.SessionId {
			return true
		}
		return acceptsRegressedSession(prior, in)
	}
	if !prior.HasTime {
		return true
	}
	if in.OccurredAt.After(prior.Time) {
		return true
	}
	if in.OccurredAt.Equal(prior.Time) {
		return !in.Connected && prior.Connected
	}
	return false
}

// acceptsRegressedSession decides whether a session id LOWER than the stored one is
// nonetheless the current truth. Every conjunct is load-bearing:
//
//   - Connected — the exception exists to re-file a device onto the session it is
//     LIVING on. A death carrying a lower session is the stale-will case the ordinary
//     rule is there to reject, and admitting it would let a dead session's echo kill a
//     live connection.
//   - ExpectedSessionId != 0 — zero means "no claim". 🔑 Be precise about this one: at
//     the ONE call site it is redundant, because the branch is only reached when
//     in.SessionId < prior.SessionId, which already forces prior.SessionId >= 1 and so
//     makes the equality below unsatisfiable at zero. It is kept because the predicate
//     must be safe on its own terms rather than only in the context it happens to be
//     called from — zero is a legitimate Prior.SessionId (an INFERRED row has never been
//     told a session), so evaluating this rule from anywhere else without the guard
//     would open the exception to every producer that never set the field. Pinned
//     directly by TestAcceptsRegressedSessionGuardsZero so it stays true independently
//     of its caller.
//   - ExpectedSessionId == prior.SessionId — the compare-and-set itself. The producer
//     read the projection and is reporting against what it read; if the projection has
//     moved since, this report is built on a stale premise and must not apply.
//   - prior.HasTime && OccurredAt.After(prior.Time) — the surrogate for freshness.
//     Decide is pure and has no clock, so "was this stamped recently" is a SENDER
//     obligation the receiver cannot verify; what the receiver can enforce is that an
//     accepted transition never moves the cursor backwards. Without a prior time there
//     is no "after" to test, and the compare-and-set has nothing to be newer than.
func acceptsRegressedSession(prior Prior, in Incoming) bool {
	return in.Connected &&
		in.ExpectedSessionId != 0 &&
		in.ExpectedSessionId == prior.SessionId &&
		prior.HasTime &&
		in.OccurredAt.After(prior.Time)
}
