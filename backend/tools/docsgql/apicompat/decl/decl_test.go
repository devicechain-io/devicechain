// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decl

import (
	"strings"
	"testing"
)

var testKinds = map[string]Severity{"thing-removed": Breaking, "thing-changed": Dangerous, "thing-added": Safe}

func chg(coord, kind string, sev Severity) Change {
	return Change{Surface: "s", Coord: coord, Kind: kind, Severity: sev}
}

func TestParseRejectsWhatCouldCoverTheWrongThing(t *testing.T) {
	src := strings.Join([]string{
		"# a comment",
		"",
		"v1.2.3 s a thing-removed ok with a reason",
		"v1.2.3 s b thing-removed",             // no reason
		"1.2.3 s c thing-removed reason",       // not a tag
		"v1.2.3 s d thing-vanished reason",     // unknown kind
		"v1.2.3 s e thing-changed reason",      // not breaking
		"v1.2.3 s a thing-removed again twice", // duplicate
	}, "\n")
	decls, bad := Parse(strings.NewReader(src), "breaks.txt", testKinds)
	if len(decls) != 1 || decls[0].Coord != "a" || decls[0].Reason != "ok with a reason" {
		t.Fatalf("decls = %+v", decls)
	}
	if len(bad) != 5 {
		t.Fatalf("want 5 problems, got %d: %v", len(bad), bad)
	}
}

func TestEvaluate(t *testing.T) {
	decls := []Declaration{
		{Baseline: "v1.0.0", Surface: "s", Coord: "a", Kind: "thing-removed", Line: 1},
		{Baseline: "v1.0.0", Surface: "s", Coord: "stale", Kind: "thing-removed", Line: 2},
		{Baseline: "v0.9.0", Surface: "s", Coord: "b", Kind: "thing-removed", Line: 3},
	}
	changes := []Change{
		chg("a", "thing-removed", Breaking),
		chg("b", "thing-removed", Breaking),
		chg("c", "thing-changed", Dangerous),
		chg("d", "thing-added", Safe),
	}
	r := Evaluate("v1.0.0", changes, decls)
	if len(r.Declared) != 1 || r.Declared[0].Change.Coord != "a" {
		t.Errorf("declared = %+v", r.Declared)
	}
	// b is covered only by a declaration for ANOTHER baseline, which covers nothing.
	if len(r.Undeclared) != 1 || r.Undeclared[0].Coord != "b" {
		t.Errorf("undeclared = %+v", r.Undeclared)
	}
	if len(r.Stale) != 1 || r.Stale[0].Coord != "stale" {
		t.Errorf("stale = %+v", r.Stale)
	}
	if len(r.Expired) != 1 || len(r.Dangerous) != 1 || len(r.Safe) != 1 || !r.Failed() {
		t.Errorf("result = %+v", r)
	}
}

func TestADeclarationCoversOnlyItsKind(t *testing.T) {
	decls := []Declaration{{Baseline: "v1.0.0", Surface: "s", Coord: "a", Kind: "thing-removed", Line: 1}}
	r := Evaluate("v1.0.0", []Change{chg("a", "other-removed", Breaking)}, decls)
	if len(r.Undeclared) != 1 || len(r.Stale) != 1 {
		t.Fatalf("a declaration for another kind must not cover the change: %+v", r)
	}
}

func TestExpiredNeverFails(t *testing.T) {
	r := Evaluate("v1.1.0", nil, []Declaration{{Baseline: "v1.0.0", Surface: "s", Coord: "a", Kind: "thing-removed"}})
	if r.Failed() || len(r.Expired) != 1 {
		t.Fatalf("%+v", r)
	}
}
