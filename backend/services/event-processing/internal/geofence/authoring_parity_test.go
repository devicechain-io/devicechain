// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/geo"
)

// 🔴 THE PROPERTY THE WHOLE SLICE EXISTS TO HOLD:
//
//	a ring the AUTHORING gate accepts must always compile in the EVALUATOR.
//
// Testing the two checks separately would not give this. Two suites can both be
// green while the predicates disagree — they agree today by construction, and this
// is what turns "by construction" into something that fails out loud if a later
// change moves one side. The failure it guards against is not a crash: it is a
// fence that saves cleanly, sits in the registry looking healthy, and answers
// nothing because the engine refused it at compile time. That is precisely the
// state this slice was written to end, and nothing about it looks wrong.
//
// The corpus deliberately mixes shapes that MUST pass with shapes that must be
// refused by both, so the property cannot be satisfied by a validator that says no
// to everything — the counts below are asserted for exactly that reason.
func TestEveryRingAuthoringAcceptsAlsoCompiles(t *testing.T) {
	circle := func(n int) [][]float64 {
		ring := make([][]float64, 0, n+1)
		for i := 0; i < n; i++ {
			theta := 2 * math.Pi * float64(i) / float64(n)
			ring = append(ring, []float64{-84.0 + 0.01*math.Cos(theta), 33.0 + 0.01*math.Sin(theta)})
		}
		return append(ring, []float64{ring[0][0], ring[0][1]})
	}

	corpus := []struct {
		name string
		ring [][]float64
		// 🔴 Per case, not a comment and not an aggregate. The floors below used to
		// be the only assertion, and they were exact only by coincidence (9 + 6
		// happened to equal the corpus size). Adding one "should pass" ring that
		// both sides refuse would have kept `accepted` above its floor and grown
		// `refused`, and nothing would have failed.
		wantAccepted bool
	}{
		// Expected to pass both.
		{"triangle", [][]float64{{0, 0}, {1, 0}, {0.5, 1}, {0, 0}}, true},
		{"axis-aligned box", [][]float64{{-84, 33}, {-83, 33}, {-83, 34}, {-84, 34}, {-84, 33}}, true},
		{"clockwise box", [][]float64{{-84, 33}, {-84, 34}, {-83, 34}, {-83, 33}, {-84, 33}}, true},
		{"circle of 8", circle(8), true},
		{"circle of 511", circle(511), true},
		{"concave L", [][]float64{{0, 0}, {2, 0}, {2, 1}, {1, 1}, {1, 2}, {0, 2}, {0, 0}}, true},
		{"high latitude box", [][]float64{{-10, 80}, {10, 80}, {10, 81}, {-10, 81}, {-10, 80}}, true},
		{"spanning the antimeridian", [][]float64{{179, 0}, {-179, 0}, {-179, 1}, {179, 1}, {179, 0}}, true},
		{"southern hemisphere", [][]float64{{151, -33}, {152, -33}, {152, -34}, {151, -34}, {151, -33}}, true},

		// Expected to be refused by both.
		{"bow-tie", [][]float64{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}}, false},
		{"pinched", [][]float64{{0, 0}, {1, 0}, {0, 0}, {1, 1}, {0, 0}}, false},
		{"duplicated corner", [][]float64{{0, 0}, {1, 0}, {1, 0}, {0.5, 1}, {0, 0}}, false},
		{"two distinct corners", [][]float64{{0, 0}, {1, 1}, {0, 0}, {0, 0}}, false},
		{"too short", [][]float64{{0, 0}, {1, 0}, {0, 0}}, false},
		{"not closed", [][]float64{{0, 0}, {1, 0}, {0.5, 1}, {0.4, 0.9}}, false},
	}

	for _, tc := range corpus {
		authoringErr := geo.ValidateClosedRing(tc.ring)
		_, compileErr := loopFromRing(tc.ring)

		if got := authoringErr == nil; got != tc.wantAccepted {
			t.Errorf("%s: authoring accepted=%v, want %v (err: %v)", tc.name, got, tc.wantAccepted, authoringErr)
		}

		// THE PROPERTY. The converse is deliberately not asserted: the evaluator
		// may refuse MORE than authoring without stranding anyone, because nothing
		// gets stored. It is this direction that leaves a dead fence in the
		// registry — saved, healthy-looking, answering nothing.
		if authoringErr == nil && compileErr != nil {
			t.Errorf("%s: authoring accepted it, the engine refused it (%v) — a fence like this "+
				"saves clean and then answers nothing", tc.name, compileErr)
		}
	}
}
