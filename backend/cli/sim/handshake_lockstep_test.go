// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// The handshake is a WIRE CONTRACT between two modules: dcctl writes it, dc-simulator
// reads it, and each declares its own struct because neither depends on the other.
// Both files say "keep these in lockstep" — and until this test, saying so was the
// entire mechanism. A field added to one and forgotten in the other compiles, vets
// and unit-tests clean in both modules; the failure surfaces as a harness refusing to
// start against a record dcctl just wrote, with an endpoint that is simply empty.
//
// A comment asserting an invariant is not an enforcer of it. This is the enforcer: it
// parses dc-simulator's declaration from source and compares the json tags field by
// field against the one here.
//
// 🔴 THE PARSED FILE LIVES OUTSIDE THIS MODULE AND GO'S TEST CACHE DOES NOT TRACK IT,
// so a plain `go test` serves a stale PASS over an edit there. CI is safe — it runs
// every module with `-count=1` — and the pre-commit sweep in CLAUDE.md now does too.

const simHandshakePath = "../../sims/dc-simulator/sim/handshake.go"

// jsonTagsOfDeclaredStruct parses one Go file and returns the json tag names of the
// named struct's fields, in declaration order.
func jsonTagsOfDeclaredStruct(t *testing.T, path, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var tags []string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != name {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		found = true
		for _, f := range st.Fields.List {
			if f.Tag == nil {
				t.Errorf("%s.%v carries no struct tag, so it has no wire name", name, f.Names)
				continue
			}
			// The tag literal includes its backquotes; reflect.StructTag wants the inside.
			tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`"))
			jsonTag := strings.Split(tag.Get("json"), ",")[0]
			if jsonTag == "" {
				t.Errorf("%s.%v has no json tag", name, f.Names)
				continue
			}
			// 🔴 `json:"-"` IS NOT A WIRE NAME, IT IS THE ABSENCE OF ONE. Treated as a
			// name it compares equal on both sides and passes every check here, while
			// the field never crosses the wire at all — the endpoint arrives empty with
			// both instruments reporting clean.
			if jsonTag == "-" {
				t.Errorf(`%s.%v is tagged json:"-", so it never crosses the wire; a field in a shared wire contract cannot be excluded from it`, name, f.Names)
				continue
			}
			// One tag over several names yields one entry here and one PER NAME from
			// reflection, which shows up as spurious drift. Refuse the form rather than
			// guess which side is right.
			if len(f.Names) > 1 {
				t.Errorf("%s declares %v on one line with a single tag; write one field per line so the two sides can be compared", name, f.Names)
				continue
			}
			tags = append(tags, jsonTag)
		}
		return false
	})
	// A parse that found nothing would compare an empty list against an empty list and
	// pass — the shape of gate this test exists to be the opposite of.
	if !found {
		t.Fatalf("no struct named %q in %s; this test would otherwise compare nothing and pass", name, path)
	}
	if len(tags) == 0 {
		t.Fatalf("struct %q in %s declared no json-tagged fields", name, path)
	}
	return tags
}

// jsonTagsOfLocalStruct returns the json tag names of a local struct, in declaration
// order, via reflection.
func jsonTagsOfLocalStruct(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	tags := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" {
			t.Errorf("%s.%s has no json tag", rt.Name(), rt.Field(i).Name)
			continue
		}
		if tag == "-" {
			t.Errorf(`%s.%s is tagged json:"-", so it never crosses the wire`, rt.Name(), rt.Field(i).Name)
			continue
		}
		tags = append(tags, tag)
	}
	return tags
}

// TestEndpointsStayInLockstepWithTheSimulator is the gate. Order is compared too, not
// just membership: the two files are read side by side by whoever maintains them, and
// a shared shape whose fields have drifted apart in order is one nobody can diff.
func TestEndpointsStayInLockstepWithTheSimulator(t *testing.T) {
	theirs := jsonTagsOfDeclaredStruct(t, simHandshakePath, "Endpoints")
	ours := jsonTagsOfLocalStruct(t, Endpoints{})

	if !reflect.DeepEqual(ours, theirs) {
		t.Errorf("the handshake endpoints have drifted between the two modules.\n"+
			"  dcctl writes:      %v\n"+
			"  dc-simulator reads: %v\n"+
			"Add the missing field to whichever side lacks it (and, if dcctl gained one, "+
			"populate it in ResolveEndpoints) — a field only one side knows about reaches "+
			"the harness as an empty string, not as an error.", ours, theirs)
	}
}

// Every endpoint dcctl DECLARES must also be one it WRITES. A field added to the
// struct and forgotten in ResolveEndpoints is worse than one missing from the struct:
// the record carries the key with an empty value, so a reader that checks for
// presence rather than emptiness sees a configured endpoint that goes nowhere.
func TestEveryDeclaredEndpointIsActuallyWritten(t *testing.T) {
	ep := ResolveEndpoints("example.invalid", "", "", true)
	rv := reflect.ValueOf(ep)
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		// 🔴 reflect.Value.String() RETURNS "<int Value>" FOR A NON-STRING rather than
		// panicking, so an emptiness test over it silently skips every field that is not
		// a string — including the case this test exists for. Fail on the SHAPE rather
		// than pass over it.
		if rv.Field(i).Kind() != reflect.String {
			t.Fatalf("Endpoints.%s is a %s, not a string; this check can only see strings and would silently skip it. Extend the check before adding the field",
				f.Name, rv.Field(i).Kind())
		}
		if rv.Field(i).String() == "" {
			t.Errorf("ResolveEndpoints leaves %s empty, so every sim record dcctl writes carries a blank %q",
				f.Name, strings.Split(f.Tag.Get("json"), ",")[0])
		}
	}
}

// 🔴 THE OUTER HALF OF THE CONTRACT, WHICH NOTHING GUARDED. The Endpoints check above
// covers the nested struct; the eight SHARED TOP-LEVEL fields had no enforcer at all
// — and that is the half carrying the credential. Renaming `simPassword` on one side
// leaves both modules green, both lockstep tests passing, and a sim logging in with an
// empty password; renaming `mqttTLSInsecure` silently drops the trust decision without
// which the command far end cannot reach a self-signed local broker.
//
// The two shapes are NOT equal — Record carries fields dc-simulator never reads — so
// this is a subset-in-order assertion: every field the simulator consumes must appear
// in what dcctl writes, in the same relative order, so the two files stay diffable.
func TestEveryHandshakeFieldTheSimulatorReadsIsOneDcctlWrites(t *testing.T) {
	theirs := jsonTagsOfDeclaredStruct(t, simHandshakePath, "Handshake")
	ours := jsonTagsOfLocalStruct(t, Record{})

	oursAt := map[string]int{}
	for i, tag := range ours {
		oursAt[tag] = i
	}

	prev := -1
	for _, tag := range theirs {
		at, ok := oursAt[tag]
		if !ok {
			t.Errorf("dc-simulator's Handshake reads %q and dcctl's Record does not write it — the field arrives as a zero value, not as an error.\n"+
				"  dc-simulator reads: %v\n  dcctl writes:       %v", tag, theirs, ours)
			continue
		}
		if at < prev {
			t.Errorf("%q appears out of order relative to the simulator's declaration; keep the shared fields in the same order so the two files can be read side by side.\n"+
				"  dc-simulator reads: %v\n  dcctl writes:       %v", tag, theirs, ours)
		}
		prev = at
	}
}

// The Endpoints shapes ARE equal, so their field count is pinned too: a field removed
// from both sides at once is symmetric drift that the equality check cannot see.
func TestTheEndpointsContractHasNotSilentlyShrunk(t *testing.T) {
	const knownEndpoints = 10
	if got := len(jsonTagsOfLocalStruct(t, Endpoints{})); got != knownEndpoints {
		t.Errorf("the handshake declares %d endpoints; this contract has carried %d. If that is a deliberate change, update this number in the same commit — otherwise a field dropped from BOTH sides at once passes the lockstep check unnoticed",
			got, knownEndpoints)
	}
}
