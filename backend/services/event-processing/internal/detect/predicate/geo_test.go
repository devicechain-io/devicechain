// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
)

// fenceSet builds a one-fence frozen set holding an axis-aligned lon/lat box.
func fenceSet(version int32, token string, lonMin, latMin, lonMax, latMax float64) *geofence.FenceSet {
	geom, err := json.Marshal(map[string]any{
		"kind": geofence.KindPolygon2D,
		"geometry": map[string]any{
			"type": "Polygon",
			"coordinates": [][][2]float64{{
				{lonMin, latMin}, {lonMax, latMin}, {lonMax, latMax}, {lonMin, latMax}, {lonMin, latMin},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return geofence.NewFenceSet(version, []geofence.SnapshotFence{{Token: token, Geometry: geom}})
}

// geoInput is one location sample: a position plus the frozen fence set its event was stamped with.
func geoInput(lon, lat float64, fs *geofence.FenceSet) Input {
	return Input{
		Device:   "d1",
		Occurred: time.Unix(1, 0),
		Position: &geofence.Position{Lat: lat, Lon: lon},
		Fences:   fs,
	}
}

// TestInFenceIsTheOnlyGeoSurfaceAndItEvaluates: the containment function type-checks against the
// shared env, and answers both ways for one compiled predicate.
func TestInFenceIsTheOnlyGeoSurfaceAndItEvaluates(t *testing.T) {
	p := mustCompile(t, `geo.inFence("yard")`)
	fs := fenceSet(1, "yard", 0, 0, 1, 1)

	if ok, err := p.Eval(geoInput(0.5, 0.5, fs)); err != nil || !ok {
		t.Fatalf("a position inside the fence did not match: ok=%v err=%v", ok, err)
	}
	if ok, err := p.Eval(geoInput(5, 5, fs)); err != nil || ok {
		t.Fatalf("a position outside the fence matched: ok=%v err=%v", ok, err)
	}
}

// TestGeoIsOpaqueToTheRuleLanguage: `geo` carries a position and a whole fence set, so what a rule
// can do with it is a security question, not a style one. The answer is: exactly one thing. Every
// expression below is rejected by the TYPE CHECKER at publish — a rule cannot read a coordinate,
// index the binding, enumerate fences, compare two of them, or convert one to anything.
func TestGeoIsOpaqueToTheRuleLanguage(t *testing.T) {
	for _, src := range []string{
		`geo.lat > 0.0`,           // read the position out
		`geo["yard"]`,             // index it
		`size(geo) > 0`,           // enumerate it
		`string(geo) == "x"`,      // convert it
		`geo == geo`,              // compare (non-boolean leaf aside, the point is the type)
		`"yard" in geo`,           // membership
		`geo.inFence("a", "b")`,   // a second argument
		`geo.inFence(1)`,          // a non-token argument
		`inFence(geo, "yard")`,    // the global (non-receiver) form is not declared
		`geo.fences()`,            // any other member
		`geo.inFence("yard") > 0`, // a non-boolean leaf
	} {
		if _, err := Compile(src, testCeiling); err == nil {
			t.Errorf("%q compiled; the geo binding must expose nothing but inFence(string)->bool", src)
		}
	}
}

// TestInFenceAcceptsOnlyATokenItIsGiven documents the one thing the list above cannot assert as a
// rejection: a token expression need not be a literal. `geo.inFence(device)` type-checks, because
// it is a string. That is not a hole — the fence set it resolves against is still ONLY this
// event's tenant's frozen set, so the worst a computed token can do is name a fence of the same
// tenant or miss (an error). It is asserted rather than left implicit because "only literals are
// allowed" would be a comfortable and false thing to believe.
func TestInFenceAcceptsOnlyATokenItIsGiven(t *testing.T) {
	p, err := Compile(`geo.inFence(device)`, testCeiling)
	if err != nil {
		t.Fatalf("a computed fence token did not compile: %v", err)
	}
	fs := fenceSet(1, "d1", 0, 0, 1, 1) // the fence is named after the device token
	in := geoInput(0.5, 0.5, fs)
	if ok, err := p.Eval(in); err != nil || !ok {
		t.Fatalf("computed token did not resolve: ok=%v err=%v", ok, err)
	}
}

// TestInFenceErrorsRatherThanAnsweringFalse pins the three ways a containment question can be
// unanswerable, and that NONE of them degrades to a boolean. A false is a positive claim that the
// device is outside; it cancels a Duration rule's in-flight hold and reads as a healthy, quiet
// rule. The runtime skips a sample whose leaf errors, which is the safe outcome.
//
// The positive control runs first: the same predicate answers true for a resolvable question, so
// the three failures below are failures of the INPUT, not of a predicate that never ran.
func TestInFenceErrorsRatherThanAnsweringFalse(t *testing.T) {
	p := mustCompile(t, `geo.inFence("yard")`)
	fs := fenceSet(1, "yard", 0, 0, 1, 1)
	if ok, err := p.Eval(geoInput(0.5, 0.5, fs)); err != nil || !ok {
		t.Fatalf("control: an answerable question did not answer true: ok=%v err=%v", ok, err)
	}

	for _, c := range []struct {
		name string
		in   Input
		want error
	}{
		{"no position", Input{Device: "d1", Occurred: time.Unix(1, 0), Fences: fs}, geofence.ErrNoPosition},
		{"no fence set", geoInput(0.5, 0.5, nil), geofence.ErrNoFenceSet},
		{"fence not in the set", geoInput(0.5, 0.5, fenceSet(1, "depot", 0, 0, 1, 1)), geofence.ErrUnknownFence},
	} {
		ok, err := p.Eval(c.in)
		if err == nil {
			t.Errorf("%s: evaluated to %v with no error; an unanswerable question must not answer", c.name, ok)
			continue
		}
		if !strings.Contains(err.Error(), strings.SplitN(c.want.Error(), ":", 2)[0]) {
			t.Errorf("%s: error %v does not carry %v", c.name, err, c.want)
		}
	}
}

// TestReferencesFencesDrivesThePositionScopedFeed: the compile-time analysis that keeps a fence
// leaf away from positionless events. The first case is the one a descendant-walk that skips the
// ROOT node would get wrong — the whole leaf IS the call.
func TestReferencesFencesDrivesThePositionScopedFeed(t *testing.T) {
	for _, c := range []struct {
		src  string
		want bool
	}{
		{`geo.inFence("yard")`, true},                             // the call is the root node
		{`!geo.inFence("yard")`, true},                            // negated
		{`"t" in m && m["t"] > 1.0 && geo.inFence("yard")`, true}, // mixed with a metric gate
		{`cel.bind(x, geo.inFence("yard"), x && x)`, true},        // inside a scoping macro
		{`"t" in m && m["t"] > 1.0`, false},                       // no fence reference
		{`true`, false},
	} {
		if got := mustCompile(t, c.src).ReferencesFences(); got != c.want {
			t.Errorf("ReferencesFences(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

// TestSchemaVersionCoversTheGeoDeclarations: the version names the environment a compiled artifact
// was CHECKED AGAINST, and that environment is now strictly larger — a rule naming `geo` does not
// compile under the previous one. The literal is asserted so the declarations and the version
// cannot drift apart silently.
func TestSchemaVersionCoversTheGeoDeclarations(t *testing.T) {
	if SchemaVersion != 3 {
		t.Fatalf("SchemaVersion = %d, want 3 (geo + inFence entered the declared shape)", SchemaVersion)
	}
	if _, err := Compile(`geo.inFence("yard")`, testCeiling); err != nil {
		t.Fatalf("the version claims geo is declared, but a geo leaf does not compile: %v", err)
	}
}

// TestGeoDoesNotDisturbTheExistingVocabulary is the counterweight to widening the environment:
// every pre-existing leaf must compile and evaluate byte-identically. Widening is only safe while
// what was already there is untouched.
func TestGeoDoesNotDisturbTheExistingVocabulary(t *testing.T) {
	p := mustCompile(t, `"t" in m && m["t"] > 30.0 && device == "d1"`)
	in := Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{"t": 35}}
	if ok, err := p.Eval(in); err != nil || !ok {
		t.Fatalf("a pre-geo leaf changed behaviour: ok=%v err=%v", ok, err)
	}
	if p.ReferencesFences() {
		t.Error("a leaf that never mentions geo was scoped as requiring a position")
	}
	// An unbound geo (no position, no fence set) must not perturb a leaf that does not use it.
	if ok, err := p.Eval(Input{Device: "d1", Occurred: time.Unix(1, 0), M: map[string]float64{"t": 35}}); err != nil || !ok {
		t.Fatalf("a pre-geo leaf failed when geo carried nothing: ok=%v err=%v", ok, err)
	}
}

// TestUnknownFenceSentinelIsWrapped keeps the sentinel usable through the CEL boundary: the
// runtime and the preview classify by message, but a Go caller holding the error must still be
// able to ask what kind of failure it was.
func TestUnknownFenceSentinelIsWrapped(t *testing.T) {
	fs := fenceSet(4, "yard", 0, 0, 1, 1)
	_, err := fs.Contains("depot", geofence.Position{Lat: 0.5, Lon: 0.5})
	if !errors.Is(err, geofence.ErrUnknownFence) {
		t.Fatalf("Contains error %v does not wrap ErrUnknownFence", err)
	}
}
