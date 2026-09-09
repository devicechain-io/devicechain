// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"math"
	"os"
	"strings"
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

		// 🔴 OUT-OF-RANGE DEGREES ARE IN THE CORPUS BECAUSE THE CONVERSION TO THE
		// SPHERE WRAPS THEM RATHER THAN REFUSING THEM. s2.LatLngFromDegrees turns
		// latitude 91, longitude 1 into latitude 89, longitude -179 — measured — so a
		// ring carrying such a corner does not fail to build. It builds a perfectly
		// valid quadrilateral 180° around the planet from where it was drawn, and
		// every containment answer it then gives looks entirely reasonable.
		{"latitude past the north pole", [][]float64{{0, 0}, {1, 0}, {1, 91}, {0, 1}, {0, 0}}, false},
		{"latitude past the south pole", [][]float64{{0, 0}, {0, -1}, {1, -91}, {1, 0}, {0, 0}}, false},
		{"longitude past the antimeridian", [][]float64{{0, 0}, {181, 0}, {181, 1}, {0, 1}, {0, 0}}, false},
		{"longitude past the western antimeridian", [][]float64{{0, 0}, {0, 1}, {-181, 1}, {-181, 0}, {0, 0}}, false},

		// The counterweight to those four: the range BOUNDARY is a real place to draw
		// a fence, and a builder that refused it would satisfy every refusal above.
		{"on the antimeridian", [][]float64{{180, 0}, {179, 0}, {179, 1}, {180, 1}, {180, 0}}, true},
		{"touching the north pole", [][]float64{{0, 89}, {1, 89}, {0.5, 90}, {0, 89}}, true},
	}

	for _, tc := range corpus {
		authoringErr := geo.ValidateClosedRing(tc.ring)
		_, compileErr := loopFromRing(tc.ring)

		if got := authoringErr == nil; got != tc.wantAccepted {
			t.Errorf("%s: authoring accepted=%v, want %v (err: %v)", tc.name, got, tc.wantAccepted, authoringErr)
		}

		// THE PROPERTY, now asserted in BOTH directions.
		//
		// It used to be one-way, on the reasoning that the evaluator may safely
		// refuse MORE than authoring because nothing gets stored. That is true of
		// the consequence, but it left "the engine accepts a ring authoring refuses"
		// as the one thing this test explicitly did not look at — and that is the
		// direction a duplicated ring builder drifts in. The evaluator carried its
		// own copy of the builder; a range check was added to core/geo and not to
		// the copy; a ring with a corner past the pole was refused at authoring and
		// silently wrapped by the engine, and nothing here could see it.
		//
		// Both sides now call one function, so a disagreement in EITHER direction
		// means a second implementation has appeared again.
		if authoringErr == nil && compileErr != nil {
			t.Errorf("%s: authoring accepted it, the engine refused it (%v) — a fence like this "+
				"saves clean and then answers nothing", tc.name, compileErr)
		}
		if authoringErr != nil && compileErr == nil {
			t.Errorf("%s: authoring refused it (%v), the engine compiled it anyway — the two sides "+
				"are running different predicates again", tc.name, authoringErr)
		}
	}
}

// TestOutOfRangeDegreesAreRefusedByTheWholeCompilePath asks the corpus's refusal of the whole
// document path rather than of a single ring, and names the failure so a break says what broke.
//
// The hole case is not decoration. compilePolygon2D walks every ring of the polygon, and a check
// that ran on the exterior only would leave a fence whose HOLE sits on the far side of the
// planet — which subtracts nothing where it was drawn, so the fence answers "inside" over ground
// the author cut out of it.
func TestOutOfRangeDegreesAreRefusedByTheWholeCompilePath(t *testing.T) {
	unitSquare := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	for _, tc := range []struct {
		name  string
		rings [][][2]float64
	}{
		{"exterior past the pole", [][][2]float64{
			{{0, 0}, {1, 0}, {1, 91}, {0, 1}, {0, 0}}}},
		{"exterior past the antimeridian", [][][2]float64{
			{{0, 0}, {181, 0}, {181, 1}, {0, 1}, {0, 0}}}},
		{"a hole past the pole", [][][2]float64{
			unitSquare,
			{{0.2, 0.2}, {0.8, 0.2}, {0.8, 91}, {0.2, 0.8}, {0.2, 0.2}}}},
	} {
		if _, err := CompileGeometry(polygonDocument(tc.rings...)); err == nil {
			t.Errorf("%s: compiled without error — the corner was wrapped, not refused", tc.name)
		}
	}
}

// TestTheEvaluatorBuildsNoRingLoopOfItsOwn keeps the deleted copy from coming back.
//
// The behavioural tests above are the real gate, but they can only catch a second builder that
// has ALREADY diverged. A fresh copy agrees with core/geo on the day it is written, passes
// everything here, and then drifts exactly as the last one did — in a commit that touches only
// one of the two. This one fails on the copy itself, on the day it appears.
//
// s2.LoopFromPoints is the whole of the check because it is the whole of the door: it is the
// only constructor in the library that turns a sequence of positions into a Loop. EmptyLoop,
// FullLoop, LoopFromCell and RegularLoop build from something that is not a ring, and Loop's
// fields are unexported so a literal cannot be populated. This is not a ban on s2 in this
// package — the boundary index, the containment predicate and the distance maths are all s2 and
// all of them are this package's own work.
func TestTheEvaluatorBuildsNoRingLoopOfItsOwn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		scanned++
		if strings.Contains(string(body), "s2.LoopFromPoints") {
			t.Errorf("%s builds an s2.Loop from positions itself; ring construction belongs to "+
				"core/geo, so that authoring and evaluation refuse the same rings", name)
		}
	}
	// Without this the test passes by scanning nothing: a renamed suffix, a moved file, or a
	// directory read that quietly comes back empty would all read as a clean result.
	if scanned == 0 {
		t.Fatal("scanned no source files, so the check proved nothing")
	}
}
