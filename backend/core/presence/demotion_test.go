// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"testing"
	"time"
)

var demoT0 = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// TestDecideRefusesAnUnsetClaim pins the reason ClaimUnset is the zero value. A struct
// literal that forgets Claim must be REFUSED, not folded as a disconnect — which is the
// failure the enum replaced: a state string compared against "CONNECTED" yields false,
// and a consumer reading that as a death applies one for a device nobody said had died.
//
// Killing mutation: drop the !in.Claim.Valid() guard from isOrdered. The unset claim then
// falls through to the connectivity arms, a higher session is "ordered", and the fold
// applies a disconnect.
func TestDecideRefusesAnUnsetClaim(t *testing.T) {
	prior := Prior{SessionId: 100, Time: demoT0, HasTime: true, Connected: true}
	for _, c := range []Claim{ClaimUnset, Claim(99)} {
		in := Incoming{SessionId: 200, OccurredAt: demoT0.Add(time.Hour), Claim: c}
		d := Decide(prior, in)
		if d.Ordered || d.Flipped || d.NewSession || d.Demoted {
			t.Fatalf("claim %d must be refused outright, got %+v", c, d)
		}
	}
}

// TestAcceptsDemotionConjunctByConjunct calls the predicate DIRECTLY, the way
// TestAcceptsRegressedSessionGuardsZero does, so each conjunct is pinned on its own terms
// rather than only in the context isOrdered happens to call it from.
//
// Each row kills a distinct deletion: remove prior.HasTime and "no prior custody" passes;
// remove the session equality and "the row moved on" passes; relax .After to !Before and
// the equal-stamp row passes.
func TestAcceptsDemotionConjunctByConjunct(t *testing.T) {
	base := Prior{SessionId: 100, Time: demoT0, HasTime: true, Connected: true}
	cases := []struct {
		name  string
		prior Prior
		in    Incoming
		want  bool
	}{
		{
			name:  "matching session, later stamp: applies",
			prior: base,
			in:    Incoming{SessionId: 100, OccurredAt: demoT0.Add(time.Minute), Claim: ClaimDemoted},
			want:  true,
		},
		{
			name:  "no prior time: nothing to release",
			prior: Prior{SessionId: 100, HasTime: false},
			in:    Incoming{SessionId: 100, OccurredAt: demoT0.Add(time.Minute), Claim: ClaimDemoted},
			want:  false,
		},
		{
			// A higher stored session means the source spoke again AFTER the read the
			// demotion is built on — it is demonstrably alive, so the premise is gone.
			name:  "demotion names a DIFFERENT (higher) session than the row holds",
			prior: base,
			in:    Incoming{SessionId: 200, OccurredAt: demoT0.Add(time.Minute), Claim: ClaimDemoted},
			want:  false,
		},
		{
			name:  "demotion names a DIFFERENT (lower) session than the row holds",
			prior: base,
			in:    Incoming{SessionId: 50, OccurredAt: demoT0.Add(time.Minute), Claim: ClaimDemoted},
			want:  false,
		},
		{
			name:  "equal stamp is not newer",
			prior: base,
			in:    Incoming{SessionId: 100, OccurredAt: demoT0, Claim: ClaimDemoted},
			want:  false,
		},
		{
			name:  "earlier stamp is not newer",
			prior: base,
			in:    Incoming{SessionId: 100, OccurredAt: demoT0.Add(-time.Minute), Claim: ClaimDemoted},
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := acceptsDemotion(c.prior, c.in); got != c.want {
				t.Fatalf("acceptsDemotion = %v, want %v", got, c.want)
			}
			// The predicate and the public entry point must agree, or the rule is only
			// half-wired: a conjunct could be correct here and unreachable through Decide.
			if got := Decide(c.prior, c.in).Ordered; got != c.want {
				t.Fatalf("Decide(...).Ordered = %v, want %v", got, c.want)
			}
		})
	}
}

// TestADemotionCarriesNoConnectivityEdge is written at the ONE prior shape that can kill
// the mutant. With a differing session or an unordered stamp the assertion is vacuous —
// Ordered is already false, which forces Flipped and NewSession false by construction
// (see Decide). It has to be an ACCEPTED demotion over a CONNECTED prior on the SAME
// session, because that is where the connectivity arithmetic would produce a flip if the
// demotion were allowed to reach it.
//
// Killing mutation: delete the early `if in.Claim == ClaimDemoted` return from Decide.
// Flipped is then `ordered && (ClaimDemoted == ClaimConnected) != true` = true, and every
// demoted device reads as a disconnect edge.
func TestADemotionCarriesNoConnectivityEdge(t *testing.T) {
	prior := Prior{SessionId: 100, Time: demoT0, HasTime: true, Connected: true}
	in := Incoming{SessionId: 100, OccurredAt: demoT0.Add(time.Minute), Claim: ClaimDemoted}

	d := Decide(prior, in)
	if !d.Ordered {
		t.Fatalf("precondition: this demotion must be accepted, got %+v", d)
	}
	if !d.Demoted {
		t.Fatalf("an accepted demotion must report Demoted, got %+v", d)
	}
	if d.Flipped {
		t.Fatal("a demotion reported a connectivity flip: DETECT would raise an offline " +
			"alarm and the projection would write a disconnect, for a device nobody said had died")
	}
	if d.NewSession {
		t.Fatal("a demotion reported a new session: it names the session it read, not a new one")
	}
}

// TestADemotionIsIdempotentByRejection is the redelivery contract. There is no dedup at
// this layer, so the SECOND delivery of the identical transition must be refused by the
// rule itself — applying a demotion advances the stored time to its stamp, and the replay
// is then no longer strictly After.
//
// Killing mutation: make the projection's demotion fold leave PresenceTime alone. The
// replay is then still After the old time and applies a second time. (Mirrored in the
// device-state fold test, since the invariant spans both.)
func TestADemotionIsIdempotentByRejection(t *testing.T) {
	prior := Prior{SessionId: 100, Time: demoT0, HasTime: true, Connected: true}
	at := demoT0.Add(time.Minute)
	in := Incoming{SessionId: 100, OccurredAt: at, Claim: ClaimDemoted}

	if !Decide(prior, in).Ordered {
		t.Fatal("precondition: the first delivery must apply")
	}
	// What the projection holds after applying it: same session, time advanced to the
	// demotion's stamp, connectivity untouched.
	applied := Prior{SessionId: 100, Time: at, HasTime: true, Connected: prior.Connected}
	if Decide(applied, in).Ordered {
		t.Fatal("the identical demotion applied twice: redelivery is not idempotent")
	}
}

// TestOrderingAroundADemotion is the reordering table. A demotion runs BECAUSE the source
// went away, so the events most likely to arrive around it are that source's stragglers.
func TestOrderingAroundADemotion(t *testing.T) {
	at := demoT0.Add(time.Minute)
	// The projection after an accepted demotion: session preserved, time advanced,
	// Connected left exactly as it was.
	demoted := Prior{SessionId: 100, Time: at, HasTime: true, Connected: true}

	t.Run("a straggler DISCONNECT on the demoted session, stamped earlier, is refused", func(t *testing.T) {
		in := Incoming{SessionId: 100, OccurredAt: at.Add(-time.Second), Claim: ClaimDisconnected}
		if Decide(demoted, in).Ordered {
			t.Fatal("a late echo from the departed source re-asserted the row")
		}
	})
	t.Run("a genuinely NEWER session re-promotes: real evidence beats an absence of it", func(t *testing.T) {
		in := Incoming{SessionId: 200, OccurredAt: at.Add(time.Second), Claim: ClaimConnected}
		d := Decide(demoted, in)
		if !d.Ordered || !d.NewSession {
			t.Fatalf("a higher session must supersede a demotion, got %+v", d)
		}
	})
	t.Run("a demotion of a session the row no longer holds is refused", func(t *testing.T) {
		moved := Prior{SessionId: 300, Time: at, HasTime: true, Connected: true}
		in := Incoming{SessionId: 100, OccurredAt: at.Add(time.Second), Claim: ClaimDemoted}
		if Decide(moved, in).Ordered {
			t.Fatal("a demotion applied against a row that had already moved on")
		}
	})
	t.Run("a demotion older than the row's own stamp is refused", func(t *testing.T) {
		in := Incoming{SessionId: 100, OccurredAt: at.Add(-time.Hour), Claim: ClaimDemoted}
		if Decide(demoted, in).Ordered {
			t.Fatal("a demotion regressed the cursor")
		}
	})
}

// TestAConnectivityClaimIsUnaffectedByTheDemotionBranch is the counterweight. Adding a
// third claim is only safe while the two that carry the system today behave identically,
// so this re-asserts the ordinary rules through the widened predicate.
func TestAConnectivityClaimIsUnaffectedByTheDemotionBranch(t *testing.T) {
	prior := Prior{SessionId: 100, Time: demoT0, HasTime: true, Connected: true}

	if d := Decide(prior, Incoming{SessionId: 200, OccurredAt: demoT0.Add(-time.Hour), Claim: ClaimDisconnected}); !d.Ordered || !d.Flipped {
		t.Fatalf("a higher session still supersedes at an earlier clock: %+v", d)
	}
	if d := Decide(prior, Incoming{SessionId: 100, OccurredAt: demoT0, Claim: ClaimDisconnected}); !d.Ordered || !d.Flipped {
		t.Fatalf("equal stamp: DISCONNECTED still wins over a connected device: %+v", d)
	}
	if d := Decide(prior, Incoming{SessionId: 100, OccurredAt: demoT0, Claim: ClaimConnected}); d.Ordered {
		t.Fatalf("equal stamp: CONNECTED still loses: %+v", d)
	}
	if d := Decide(prior, Incoming{SessionId: 50, OccurredAt: demoT0.Add(time.Hour), Claim: ClaimConnected}); d.Ordered {
		t.Fatalf("a lower session with no compare-and-set is still refused: %+v", d)
	}
	repair := Incoming{SessionId: 50, ExpectedSessionId: 100, OccurredAt: demoT0.Add(time.Hour), Claim: ClaimConnected}
	if d := Decide(prior, repair); !d.Ordered || !d.NewSession {
		t.Fatalf("the compare-and-set repair still applies: %+v", d)
	}
}

// TestAcceptsRegressedSessionRefusesADemotionOnItsOwnTerms calls the predicate DIRECTLY,
// for the same reason TestAcceptsRegressedSessionGuardsZero does: at the ONE call site
// this is unreachable, because isOrdered routes a demotion to acceptsDemotion before the
// regressed-session branch. Reachability is a property of today's caller, not of the rule.
//
// It matters because the compare-and-set exception is the only way a LOWER session is ever
// accepted, and a demotion carrying a lower session must never take it: that would let a
// custody release re-file a device onto a dead session and mark it CONNECTED.
//
// Killing mutation: `in.Claim == ClaimConnected` → `in.Claim != ClaimDisconnected`, the
// natural-looking rewrite. It is invisible through Decide and lethal to any fourth claim
// added later, which would fall into this exception by default.
func TestAcceptsRegressedSessionRefusesADemotionOnItsOwnTerms(t *testing.T) {
	prior := Prior{SessionId: 200, Time: demoT0, HasTime: true, Connected: false}
	in := Incoming{
		SessionId:         100,
		ExpectedSessionId: 200,
		OccurredAt:        demoT0.Add(time.Hour),
		Claim:             ClaimDemoted,
	}
	if acceptsRegressedSession(prior, in) {
		t.Fatal("a demotion satisfied the regressed-session compare-and-set: a custody " +
			"release would re-file the device onto a lower session and mark it connected")
	}
	// The counterweight: the identical shape with a CONNECTED claim is exactly what the
	// exception exists for, and must still be accepted.
	in.Claim = ClaimConnected
	if !acceptsRegressedSession(prior, in) {
		t.Fatal("the repair the exception exists for is now refused")
	}
}
