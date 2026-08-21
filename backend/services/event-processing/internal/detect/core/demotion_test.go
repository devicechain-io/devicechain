// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/presence"
)

// feedDemotion feeds one custody release for a Connectivity rule and returns its detections.
// A demotion names the session it releases and asserts nothing about connectivity.
func feedDemotion(e *Engine, seq uint64, rule, series string, sec int, session uint64) []Detection {
	e.ProcessEvent(Event{
		Seq:      seq,
		Key:      SeriesKey{Rule: rule, Series: series},
		Time:     at(sec),
		Presence: &PresenceEdge{SessionId: session, Claim: presence.ClaimDemoted},
	})
	return e.Drain()
}

// TestADemotionResolvesAnAlarmNothingElseEverWould is the DETECT half of the release-gate
// defect. An offline alarm is resolved by a CONNECT — and a source that has released
// custody sends none, ever. So without this the alarm outlives the assertion it was raised
// on, for as long as the rule exists.
func TestADemotionResolvesAnAlarmNothingElseEverWould(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	onlyEdge(t, feedPresence(e, 1, "r", "d", 10, 100, false), EdgeRaised, 10)
	onlyEdge(t, feedDemotion(e, 2, "r", "d", 20, 100), EdgeResolved, 20)

	// The latch is genuinely cleared, not merely reported. Note the device is still believed
	// DOWN — a demotion carries connectivity forward, it does not revive anything — so the
	// series has to come back up before it can go down again. Both edges below would be
	// swallowed if the latch had survived the resolve.
	if d := feedPresence(e, 3, "r", "d", 30, 200, true); len(d) != 0 {
		t.Fatalf("a reconnect with nothing latched must emit nothing: %+v", d)
	}
	onlyEdge(t, feedPresence(e, 4, "r", "d", 40, 300, false), EdgeRaised, 40)
}

// TestADemotionCarriesConnectivityForward is the killing test for deriving the cursor's
// Connected from the claim. ClaimDemoted is not ClaimConnected, so the ordinary derivation
// records the device as DOWN — and the next genuine disconnect then reads as a non-flip and
// never raises. A demotion must leave the engine believing exactly what it believed before.
func TestADemotionCarriesConnectivityForward(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	if d := feedPresence(e, 1, "r", "d", 10, 100, true); len(d) != 0 {
		t.Fatalf("a first CONNECT must emit nothing: %+v", d)
	}
	if d := feedDemotion(e, 2, "r", "d", 20, 100); len(d) != 0 {
		t.Fatalf("a demotion over a healthy series must emit nothing: %+v", d)
	}
	// The device was believed UP before the release and must still be believed up, so a
	// genuine disconnect afterwards is a flip and raises.
	onlyEdge(t, feedPresence(e, 3, "r", "d", 30, 200, false), EdgeRaised, 30)
}

// TestADemotionAdvancesTheOrderingCursor pins the other half of the mirror with the
// projection: the released session stays named AND the stamp moves, so a late echo from the
// session that was just released cannot re-open the series behind the demotion's back.
func TestADemotionAdvancesTheOrderingCursor(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	if d := feedPresence(e, 1, "r", "d", 10, 100, true); len(d) != 0 {
		t.Fatalf("a first CONNECT must emit nothing: %+v", d)
	}
	feedDemotion(e, 2, "r", "d", 20, 100)

	// An echo from the RELEASED session, stamped before the demotion but delivered after it.
	// Against a cursor left at t=10 this is in order and raises; against one advanced to
	// t=20 it is stale and must be refused outright.
	if d := feedPresence(e, 3, "r", "d", 15, 100, false); len(d) != 0 {
		t.Fatalf("an echo from the released session was applied behind the demotion: %+v", d)
	}
}

// TestADemotionForAnUnseenSeriesIsRefused covers the conjunct that stops a demotion
// synthesizing a cursor out of nothing. With no prior edge the engine holds only the
// assume-online default, and admitting a demotion there would stamp a cursor at the
// demotion's time — silently making a genuinely earlier first DISCONNECT look stale.
func TestADemotionForAnUnseenSeriesIsRefused(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	if d := feedDemotion(e, 1, "r", "d", 20, 100); len(d) != 0 {
		t.Fatalf("a demotion over an unseen series emitted something: %+v", d)
	}
	// The cursor must be untouched, so an earlier first DISCONNECT still raises normally.
	onlyEdge(t, feedPresence(e, 2, "r", "d", 15, 100, false), EdgeRaised, 15)
}

// TestADemotionResolvesAtTheRaiseWhenItIsStampedEarlier pins the clamp that resolveLatched
// shares with a descope and a reconnect. It looks unreachable from a demotion — the cursor
// and the raise are stamped from the same edge, so a demotion ordered after the cursor is
// necessarily after the raise. It is reachable one step further out, because ordering is
// session-DOMINANT and a higher-session edge applies at an EARLIER wall clock: a same-state
// higher-session disconnect drags the cursor backwards while leaving the alarm latched at
// the original raise. A demotion then legitimately carries a stamp before the raise it is
// resolving, and passed to resolve() unclamped it would be discarded — leaving an alarm no
// later edge can ever reach, which is the exact immortality this arc exists to prevent.
func TestADemotionResolvesAtTheRaiseWhenItIsStampedEarlier(t *testing.T) {
	e := NewEngine([]Rule{{ID: "r", Kind: Connectivity}}, 0)

	// Raise at t=50 on a host whose clock runs ahead.
	onlyEdge(t, feedPresence(e, 1, "r", "d", 50, 100, false), EdgeRaised, 50)
	// A higher session repeats the same state at an earlier clock: not a flip, so the alarm
	// stays latched at 50 while the cursor moves back to 10.
	if d := feedPresence(e, 2, "r", "d", 10, 200, false); len(d) != 0 {
		t.Fatalf("a same-state higher-session edge must emit nothing: %+v", d)
	}
	// The release is ordered against the cursor (20 > 10) but earlier than the raise (50).
	onlyEdge(t, feedDemotion(e, 3, "r", "d", 20, 200), EdgeResolved, 50)
}
