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

// Decision splits the two effects that the old fused guard conflated. Ordered is the
// stale-guard (is the incoming transition not older than Prior?). Flipped reports
// whether the device's connectivity STATE actually changes — the semantic edge that a
// projection writes as a (dis)connect timestamp and that DETECT raises/resolves on.
// NewSession reports whether the transition opens a strictly higher session; because a
// new SessionId is by construction a new physical connection, a same-state CONNECT on a
// NewSession is still a genuine reconnect (it must refresh LastConnectTime), whereas a
// same-state DISCONNECT is a late echo about an already-dead device (it must not move
// LastDisconnectTime). NewSession always implies Ordered.
type Decision struct {
	Ordered    bool
	NewSession bool
	Flipped    bool
}

// Decide evaluates an incoming transition (sessionId, occurredAt, connected) against
// Prior. It is pure and order-independent so both consumers and their unit tests can
// call it off the DB.
func Decide(prior Prior, sessionId uint64, occurredAt time.Time, connected bool) Decision {
	ordered := isOrdered(prior, sessionId, occurredAt, connected)
	return Decision{
		Ordered:    ordered,
		NewSession: sessionId > prior.SessionId,
		Flipped:    ordered && connected != prior.Connected,
	}
}

// isOrdered is the monotonic stale-guard, mirroring the alarm integrator's discipline:
// a newer session always supersedes (even at an earlier wall clock — a producer may
// mint on a different host's clock); within the same session the newer OccurredAt wins;
// the first transition in a session applies; at an equal (session, time) only a
// DISCONNECTED over a currently-CONNECTED device applies (a session that births and
// dies at one instant is net-dead), so CONNECTED-over-DISCONNECTED and any same-state
// re-apply are rejected.
func isOrdered(prior Prior, sessionId uint64, occurredAt time.Time, connected bool) bool {
	if sessionId != prior.SessionId {
		return sessionId > prior.SessionId
	}
	if !prior.HasTime {
		return true
	}
	if occurredAt.After(prior.Time) {
		return true
	}
	if occurredAt.Equal(prior.Time) {
		return !connected && prior.Connected
	}
	return false
}
