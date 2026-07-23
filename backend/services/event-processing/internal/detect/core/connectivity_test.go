// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// feedPresence feeds one authoritative presence edge to a Connectivity rule and returns its
// detections. session is the producer's monotone connect epoch; connected is the direction.
func feedPresence(e *Engine, seq uint64, rule, series string, sec int, session uint64, connected bool) []Detection {
	e.ProcessEvent(Event{
		Seq:      seq,
		Key:      SeriesKey{Rule: rule, Series: series},
		Time:     at(sec),
		Presence: &PresenceEdge{SessionId: session, Connected: connected},
	})
	return e.Drain()
}

func onlyEdge(t *testing.T, ds []Detection, want EdgeKind, whenSec int) {
	t.Helper()
	if len(ds) != 1 || ds[0].Edge != want {
		t.Fatalf("want exactly one %v edge, got %+v", want, ds)
	}
	if !ds[0].At.Equal(at(whenSec)) {
		t.Fatalf("edge stamped at %v, want %v", ds[0].At, at(whenSec))
	}
}

// TestConnectivityRaiseOnDisconnectResolveOnConnect is the core offline-alarm behaviour: an
// authoritative DISCONNECT raises, the next CONNECT resolves. A device online at activation
// that dies must alarm, so a first DISCONNECT (no prior CONNECT seen) still raises.
func TestConnectivityRaiseOnDisconnectResolveOnConnect(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	// First-ever edge is a DISCONNECT: the device was online at activation, so it raises.
	onlyEdge(t, feedPresence(e, 1, "r", "d", 10, 100, false), EdgeRaised, 10)
	// A CONNECT at a newer session resolves it.
	onlyEdge(t, feedPresence(e, 2, "r", "d", 20, 200, true), EdgeResolved, 20)
	// A second DISCONNECT raises again (a new outage).
	onlyEdge(t, feedPresence(e, 3, "r", "d", 30, 300, false), EdgeRaised, 30)
}

// TestConnectivityFirstConnectIsNoOp proves a first CONNECT (device comes online, nothing was
// raised) emits nothing — assume-online means a connect is the non-event, a disconnect is the edge.
func TestConnectivityFirstConnectIsNoOp(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)
	if d := feedPresence(e, 1, "r", "d", 10, 100, true); len(d) != 0 {
		t.Fatalf("a first CONNECT must emit nothing (already assumed online): %+v", d)
	}
	// It set the cursor connected; a same-session later DISCONNECT then raises.
	onlyEdge(t, feedPresence(e, 2, "r", "d", 20, 100, false), EdgeRaised, 20)
}

// TestConnectivitySameSessionCleanDeath is the Blocker-1 regression: a session's BIRTH and its
// DEATH share one SessionId (the will is built at connect time), so a "reject unless newer
// session" cursor would never fire on a normal death. The same-session DISCONNECT must raise.
func TestConnectivitySameSessionCleanDeath(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)
	if d := feedPresence(e, 1, "r", "d", 0, 100, true); len(d) != 0 {
		t.Fatalf("connect primes, emits nothing: %+v", d)
	}
	// DISCONNECT at the SAME session 100, later time — the normal clean death. Must raise.
	onlyEdge(t, feedPresence(e, 2, "r", "d", 10, 100, false), EdgeRaised, 10)
}

// TestConnectivitySameStateHigherSessionNonEvent is the S3b non-event: a day-late shadow-expiry
// DISCONNECT at a HIGHER session over an already-offline device advances the ordering cursor but
// fires NOTHING (no re-raise). The cursor advance is protective — a later intermediate-session
// stale CONNECT is then dropped.
func TestConnectivitySameStateHigherSessionNonEvent(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)
	onlyEdge(t, feedPresence(e, 1, "r", "d", 10, 100, false), EdgeRaised, 10) // offline @ session 100
	// A higher-session DISCONNECT (the L3b reconstruction echo) — same state, must NOT re-raise.
	if d := feedPresence(e, 2, "r", "d", 100, 200, false); len(d) != 0 {
		t.Fatalf("a same-state higher-session DISCONNECT must be a non-event, got %+v", d)
	}
	// The cursor advanced to 200, so a stale intermediate-session (150) CONNECT is dropped: the
	// device stays offline (no spurious resolve).
	if d := feedPresence(e, 3, "r", "d", 50, 150, true); len(d) != 0 {
		t.Fatalf("a stale intermediate-session CONNECT must be dropped by the advanced cursor, got %+v", d)
	}
	// A genuinely newer CONNECT (session 300) resolves.
	onlyEdge(t, feedPresence(e, 4, "r", "d", 120, 300, true), EdgeResolved, 120)
}

// TestConnectivityStaleLowerSessionDropped proves a stale DISCONNECT from an OLDER session cannot
// flip a device the newer session brought online — the stale-will guard, engine side.
func TestConnectivityStaleLowerSessionDropped(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 30*time.Second)
	if d := feedPresence(e, 1, "r", "d", 10, 200, true); len(d) != 0 { // online @ session 200
		t.Fatalf("connect primes: %+v", d)
	}
	// A stale DISCONNECT from the OLD session 100, arriving later in wall time, must be dropped.
	if d := feedPresence(e, 2, "r", "d", 25, 100, false); len(d) != 0 {
		t.Fatalf("a stale lower-session DISCONNECT must not raise, got %+v", d)
	}
}

// TestConnectivityResolveClampedOnEarlierWallClock is the Blocker-2 regression: connectivity
// ordering is session-DOMINANT, so a failover reconnect can carry a newer session at an EARLIER
// wall clock. The resolve must still fire (clamped forward to the rising edge), not be swallowed
// by the value kinds' stale-falling-edge guard — else the offline alarm strands raised forever.
func TestConnectivityResolveClampedOnEarlierWallClock(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, time.Hour)
	onlyEdge(t, feedPresence(e, 1, "r", "d", 100, 100, false), EdgeRaised, 100) // offline, raisedAt=100
	// Failover reconnect: newer session 200 but stamped at t=80 (earlier wall clock than the raise).
	d := feedPresence(e, 2, "r", "d", 80, 200, true)
	if len(d) != 1 || d[0].Edge != EdgeResolved {
		t.Fatalf("an earlier-stamped newer-session CONNECT must still resolve: %+v", d)
	}
	// Resolved is clamped forward to the rising edge (never a negative-duration alarm).
	if !d[0].At.Equal(at(100)) {
		t.Fatalf("resolve must clamp to the rising edge %v, got %v", at(100), d[0].At)
	}
}

// TestConnectivitySnapshotRestoresCursorAndLatch proves the ordering cursor AND the raised latch
// survive a restart: a restored engine does not re-raise an already-raised offline alarm, and it
// still drops a stale lower-session edge (the cursor was preserved).
func TestConnectivitySnapshotRestoresCursorAndLatch(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Connectivity}}
	e := NewEngine(rules, 30*time.Second)
	feedPresence(e, 1, "r", "d", 10, 200, true)                               // online @ 200
	onlyEdge(t, feedPresence(e, 2, "r", "d", 20, 200, false), EdgeRaised, 20) // offline

	blob, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	re, err := Restore(rules, 30*time.Second, blob)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The latch survived: a redelivered same DISCONNECT does not re-raise.
	if d := feedPresence(re, 3, "r", "d", 20, 200, false); len(d) != 0 {
		t.Fatalf("restored engine re-raised an already-raised alarm: %+v", d)
	}
	// The cursor survived: a stale lower-session CONNECT is still dropped.
	if d := feedPresence(re, 4, "r", "d", 25, 100, true); len(d) != 0 {
		t.Fatalf("restored engine lost the cursor and applied a stale edge: %+v", d)
	}
	// A genuinely newer CONNECT resolves.
	onlyEdge(t, feedPresence(re, 5, "r", "d", 30, 300, true), EdgeResolved, 30)
}
