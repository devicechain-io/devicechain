// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	// prior builds a Prior with a valid time.
	prior := func(session uint64, at time.Time, connected bool) Prior {
		return Prior{SessionId: session, Time: at, HasTime: true, Connected: connected}
	}

	cases := []struct {
		name       string
		prior      Prior
		session    uint64
		expected   uint64 // ExpectedSessionId; 0 = no compare-and-set claim
		at         time.Time
		connected  bool
		ordered    bool
		newSession bool
		flipped    bool
	}{
		// --- first transition (no prior time) ---
		{"first connect applies", Prior{}, 5, 0, t0, true, true, true, true},
		{"first disconnect applies", Prior{}, 5, 0, t0, false, true, true, false}, // was Connected:false, still false → not a flip, but ordered
		{"first disconnect over zero-session is a non-flip", Prior{SessionId: 0}, 0, 0, t0, false, true, false, false},

		// --- the BUG the ADR-067 slice fixed: same-state HIGHER-session ---
		// A day-late shadow-expiry DISCONNECT at a higher session over an already-dead device:
		// ordered (advance the marker) but NOT flipped (must NOT move LastDisconnectTime / re-fire DETECT).
		{"higher-session disconnect over disconnect = non-event", prior(100, t0, false), 200, 0, t1, false, true, true, false},
		// A higher-session CONNECT over an already-connected device is a GENUINE reconnect (new epoch =
		// new physical connection): ordered, NOT flipped, but NewSession=true drives the LastConnectTime refresh.
		{"higher-session connect over connect = reconnect (newSession)", prior(100, t0, true), 200, 0, t1, true, true, true, false},

		// --- genuine flips ---
		{"higher-session disconnect over connect = flip", prior(100, t0, true), 200, 0, t1, false, true, true, true},
		{"higher-session connect over disconnect = flip", prior(100, t0, false), 200, 0, t1, true, true, true, true},
		{"same-session later disconnect over connect = flip", prior(100, t0, true), 100, 0, t1, false, true, false, true},
		{"same-session later connect over disconnect = flip", prior(100, t0, false), 100, 0, t1, true, true, false, true},

		// --- same-session non-flips ---
		{"same-session later connect over connect = rebirth, not flipped, not newSession", prior(100, t0, true), 100, 0, t1, true, true, false, false},
		{"same-session later disconnect over disconnect = non-event", prior(100, t0, false), 100, 0, t1, false, true, false, false},

		// --- equal-stamp tiebreak: birth-and-death at one instant is net-dead ---
		{"equal-stamp disconnect over connect applies (net-dead)", prior(100, t0, true), 100, 0, t0, false, true, false, true},
		{"equal-stamp connect over disconnect rejected", prior(100, t0, false), 100, 0, t0, true, false, false, false},
		{"equal-stamp same-state disconnect rejected", prior(100, t0, false), 100, 0, t0, false, false, false, false},
		{"equal-stamp same-state connect rejected", prior(100, t0, true), 100, 0, t0, true, false, false, false},

		// --- stale (lower session or older time) rejected ---
		{"lower-session disconnect rejected (stale will)", prior(200, t0, true), 100, 0, t1, false, false, false, false},
		{"lower-session connect rejected", prior(200, t0, false), 100, 0, t1, true, false, false, false},
		{"same-session earlier rejected", prior(100, t1, true), 100, 0, t0, false, false, false, false},

		// --- compare-and-set: the ONLY way a lower session is accepted ---
		// The trailing-clock repair proper. The projection holds a DEAD higher session and
		// the device is live on a lower one; the producer names what it read, so the row is
		// re-filed onto the session the device is actually on and comes back online.
		{"CAS connect over dead expected session = accepted flip", prior(200, t0, false), 100, 200, t1, true, true, true, true},
		// Trigger A: the row is ACTIVE but filed under a session the device is no longer on
		// (the #727 pin). Re-filing is ordered and IS a new session — which is what refreshes
		// LastConnectTime — but it is NOT a flip: the device was already believed online.
		{"CAS connect over active expected session = re-file, not a flip", prior(200, t0, true), 100, 200, t1, true, true, true, false},

		// --- compare-and-set refusals: each conjunct gets its own case ---
		// The precondition failed: the projection moved since the producer read it, so the
		// report rests on a stale premise.
		{"CAS with mismatched expectation rejected", prior(200, t0, false), 100, 150, t1, true, false, false, false},
		// No claim was made. This is every producer that does not set the field.
		{"CAS with zero expectation rejected", prior(200, t0, false), 100, 0, t1, true, false, false, false},
		// A DEATH may never ride the exception — that is the stale-will case the ordinary
		// rule exists to reject, and admitting it lets a dead session kill a live connection.
		{"CAS disconnect rejected even with a good expectation", prior(200, t0, true), 100, 200, t1, false, false, false, false},
		// Freshness surrogate: an accepted transition may never regress the cursor.
		{"CAS not newer than prior rejected", prior(200, t1, false), 100, 200, t0, true, false, false, false},
		{"CAS equal-stamp rejected", prior(200, t0, false), 100, 200, t0, true, false, false, false},
		// Idempotence on redelivery: once the repair has applied, the projection holds the
		// REPORTED session at the repair's own stamp, so the replay is decided by the
		// ordinary same-session rules — equal stamp, same state — and a now-stale
		// expectation does nothing to rescue it. (Walked end to end in
		// TestDecideCASIsIdempotent; here to keep the case visible in the table.)
		{"CAS replay after it applied is rejected", prior(100, t1, true), 100, 200, t1, true, false, false, false},
		// The exception is scoped to the lower-session branch. A HIGHER session carrying an
		// expectation is decided by the ordinary rule, not by the compare-and-set.
		{"expectation on a higher session is ignored (ordinary rule applies)", prior(100, t0, false), 200, 999, t1, true, true, true, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claim := ClaimDisconnected
			if c.connected {
				claim = ClaimConnected
			}
			d := Decide(c.prior, Incoming{
				SessionId:         c.session,
				ExpectedSessionId: c.expected,
				OccurredAt:        c.at,
				Claim:             claim,
			})
			if d.Ordered != c.ordered {
				t.Errorf("Ordered = %v, want %v", d.Ordered, c.ordered)
			}
			if d.NewSession != c.newSession {
				t.Errorf("NewSession = %v, want %v", d.NewSession, c.newSession)
			}
			if d.Flipped != c.flipped {
				t.Errorf("Flipped = %v, want %v", d.Flipped, c.flipped)
			}
		})
	}
}

// TestDecideFlippedImpliesOrdered pins the invariant a consumer relies on: Flipped is
// never reported without Ordered (a stale edge can never be a state change), and
// NewSession always implies Ordered. The sweep includes compare-and-set claims, because
// that is the case that made the old NewSession formula ("strictly higher than prior")
// wrong: a lower session can now be Ordered, and it must be reported as a new session
// so the projection refreshes LastConnectTime.
func TestDecideFlippedImpliesOrdered(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	for _, session := range []uint64{0, 50, 100, 150} {
		for _, expected := range []uint64{0, 50, 100, 150} {
			// Every claim, INCLUDING the invalid zero and the demotion: the invariant is
			// about Decision's internal consistency, so it must hold for a claim Decide
			// refuses outright and for one that returns from the demotion branch.
			for _, claim := range []Claim{ClaimConnected, ClaimDisconnected, ClaimDemoted, ClaimUnset} {
				for _, dt := range []time.Duration{-time.Hour, 0, time.Hour} {
					prior := Prior{SessionId: 100, Time: t0, HasTime: true, Connected: true}
					d := Decide(prior, Incoming{
						SessionId:         session,
						ExpectedSessionId: expected,
						OccurredAt:        t0.Add(dt),
						Claim:             claim,
					})
					if d.Flipped && !d.Ordered {
						t.Fatalf("Flipped without Ordered: session=%d expected=%d claim=%v dt=%v",
							session, expected, claim, dt)
					}
					if d.NewSession && !d.Ordered {
						t.Fatalf("NewSession without Ordered: session=%d expected=%d claim=%v dt=%v",
							session, expected, claim, dt)
					}
					if d.Demoted && !d.Ordered {
						t.Fatalf("Demoted without Ordered: session=%d expected=%d claim=%v dt=%v",
							session, expected, claim, dt)
					}
				}
			}
		}
	}
}

// TestDecideNeverAcceptsOverAHigherSession pins the property that makes compare-and-set
// safe under reordering: whatever a producer claims to expect, a transition can never
// apply over a session that is already HIGHER than the one it reports. Without this, a
// replayed or crossing repair could pull a device back onto a superseded connection.
//
// This is a property test rather than a case list because the danger is a future
// conjunct being added to acceptsRegressedSession that happens to hold for some
// unenumerated combination.
func TestDecideNeverAcceptsOverAHigherSession(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	for _, priorSession := range []uint64{1, 100, 1 << 62} {
		for _, session := range []uint64{0, 1, 99} {
			if session >= priorSession {
				continue
			}
			for _, expected := range []uint64{0, 1, 99, 100, 1 << 62} {
				for _, connected := range []bool{true, false} {
					for _, dt := range []time.Duration{-time.Hour, 0, time.Hour} {
						prior := Prior{SessionId: priorSession, Time: t0, HasTime: true, Connected: true}
						d := Decide(prior, Incoming{
							SessionId:         session,
							ExpectedSessionId: expected,
							OccurredAt:        t0.Add(dt),
							Claim:             claimOf(connected),
						})
						if !d.Ordered {
							continue
						}
						// The only admissible reason to accept a lower session is a
						// satisfied compare-and-set on a fresher CONNECT.
						if !connected || expected != priorSession || dt <= 0 {
							t.Fatalf("accepted a lower session without a satisfied CAS: "+
								"prior=%d session=%d expected=%d connected=%v dt=%v",
								priorSession, session, expected, connected, dt)
						}
					}
				}
			}
		}
	}
}

// TestAcceptsRegressedSessionGuardsZero pins the zero-expectation guard DIRECTLY rather
// than through Decide.
//
// 🔑 Through Decide the guard is unreachable: the branch is only taken when the incoming
// session is strictly lower than the prior one, which forces prior.SessionId >= 1 and so
// makes an expectation of zero unsatisfiable anyway. That is exactly why it is worth a
// test of its own — a guard whose only justification is "some other line makes it
// impossible" is one refactor away from being deleted as dead code, and the thing it
// protects against (an INFERRED row whose session has always been zero) is real.
func TestAcceptsRegressedSessionGuardsZero(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	prior := Prior{SessionId: 0, Time: t0, HasTime: true, Connected: false}
	in := Incoming{SessionId: 0, ExpectedSessionId: 0, OccurredAt: t0.Add(time.Hour), Claim: ClaimConnected}
	if acceptsRegressedSession(prior, in) {
		t.Fatal("a zero expectation matched a zero prior session: the exception is open to " +
			"every producer that never set ExpectedSessionId")
	}
}

// TestDecideCASIsIdempotent walks the repair through the state it produces and asserts
// the replay is refused. Redelivery is normal on an at-least-once stream, so "applies
// once, then stops applying" is a property the projection depends on rather than a nicety.
func TestDecideCASIsIdempotent(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	repairAt := t0.Add(time.Hour)

	// The projection holds a dead, higher session; the device is live on a lower one.
	prior := Prior{SessionId: 200, Time: t0, HasTime: true, Connected: false}
	in := Incoming{SessionId: 100, ExpectedSessionId: 200, OccurredAt: repairAt, Claim: ClaimConnected}

	first := Decide(prior, in)
	if !first.Ordered || !first.Flipped {
		t.Fatalf("the repair did not apply: %+v", first)
	}

	// Apply it the way device-state's merge does, then redeliver the identical event.
	applied := Prior{SessionId: in.SessionId, Time: in.OccurredAt, HasTime: true, Connected: in.Claim == ClaimConnected}
	if replay := Decide(applied, in); replay.Ordered {
		t.Fatalf("the redelivered repair applied a second time: %+v", replay)
	}
}

// claimOf maps a test table's boolean direction onto a Claim. Tests that predate the
// Claim enum express direction as a bool; this keeps their tables readable without
// letting a bool back into the production type.
func claimOf(connected bool) Claim {
	if connected {
		return ClaimConnected
	}
	return ClaimDisconnected
}
