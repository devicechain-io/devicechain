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
		at         time.Time
		connected  bool
		ordered    bool
		newSession bool
		flipped    bool
	}{
		// --- first transition (no prior time) ---
		{"first connect applies", Prior{}, 5, t0, true, true, true, true},
		{"first disconnect applies", Prior{}, 5, t0, false, true, true, false}, // was Connected:false, still false → not a flip, but ordered
		{"first disconnect over zero-session is a non-flip", Prior{SessionId: 0}, 0, t0, false, true, false, false},

		// --- the BUG this slice fixes: same-state HIGHER-session ---
		// A day-late shadow-expiry DISCONNECT at a higher session over an already-dead device:
		// ordered (advance the marker) but NOT flipped (must NOT move LastDisconnectTime / re-fire DETECT).
		{"higher-session disconnect over disconnect = non-event", prior(100, t0, false), 200, t1, false, true, true, false},
		// A higher-session CONNECT over an already-connected device is a GENUINE reconnect (new epoch =
		// new physical connection): ordered, NOT flipped, but NewSession=true drives the LastConnectTime refresh.
		{"higher-session connect over connect = reconnect (newSession)", prior(100, t0, true), 200, t1, true, true, true, false},

		// --- genuine flips ---
		{"higher-session disconnect over connect = flip", prior(100, t0, true), 200, t1, false, true, true, true},
		{"higher-session connect over disconnect = flip", prior(100, t0, false), 200, t1, true, true, true, true},
		{"same-session later disconnect over connect = flip", prior(100, t0, true), 100, t1, false, true, false, true},
		{"same-session later connect over disconnect = flip", prior(100, t0, false), 100, t1, true, true, false, true},

		// --- same-session non-flips ---
		{"same-session later connect over connect = rebirth, not flipped, not newSession", prior(100, t0, true), 100, t1, true, true, false, false},
		{"same-session later disconnect over disconnect = non-event", prior(100, t0, false), 100, t1, false, true, false, false},

		// --- equal-stamp tiebreak: birth-and-death at one instant is net-dead ---
		{"equal-stamp disconnect over connect applies (net-dead)", prior(100, t0, true), 100, t0, false, true, false, true},
		{"equal-stamp connect over disconnect rejected", prior(100, t0, false), 100, t0, true, false, false, false},
		{"equal-stamp same-state disconnect rejected", prior(100, t0, false), 100, t0, false, false, false, false},
		{"equal-stamp same-state connect rejected", prior(100, t0, true), 100, t0, true, false, false, false},

		// --- stale (lower session or older time) rejected ---
		{"lower-session disconnect rejected (stale will)", prior(200, t0, true), 100, t1, false, false, false, false},
		{"lower-session connect rejected", prior(200, t0, false), 100, t1, true, false, false, false},
		{"same-session earlier rejected", prior(100, t1, true), 100, t0, false, false, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.prior, c.session, c.at, c.connected)
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
// NewSession always implies Ordered (a strictly higher session is always in-order).
func TestDecideFlippedImpliesOrdered(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	for _, session := range []uint64{0, 50, 100, 150} {
		for _, connected := range []bool{true, false} {
			for _, dt := range []time.Duration{-time.Hour, 0, time.Hour} {
				prior := Prior{SessionId: 100, Time: t0, HasTime: true, Connected: true}
				d := Decide(prior, session, t0.Add(dt), connected)
				if d.Flipped && !d.Ordered {
					t.Fatalf("Flipped without Ordered: session=%d connected=%v dt=%v", session, connected, dt)
				}
				if d.NewSession && !d.Ordered {
					t.Fatalf("NewSession without Ordered: session=%d connected=%v dt=%v", session, connected, dt)
				}
			}
		}
	}
}
