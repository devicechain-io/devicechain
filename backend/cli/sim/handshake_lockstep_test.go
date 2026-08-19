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
// 🔴 RUN WITH -count=1 AFTER EDITING EITHER STRUCT. The parsed file lives outside
// this module and Go's test cache does not track it, so an edit there is served a
// stale PASS here.

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
		if rv.Field(i).String() == "" {
			t.Errorf("ResolveEndpoints leaves %s empty, so every sim record dcctl writes carries a blank %q",
				rv.Type().Field(i).Name, strings.Split(rv.Type().Field(i).Tag.Get("json"), ",")[0])
		}
	}
}
