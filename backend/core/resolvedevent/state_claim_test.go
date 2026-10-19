// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/presence"
)

// TestClaimMapsEveryNamedState is the totality table. Claim replaced a string compare
// precisely because a compare is total onto a bool and therefore answers "is this device
// up?" even for inputs that are not about the device being up.
func TestClaimMapsEveryNamedState(t *testing.T) {
	cases := []struct {
		state string
		want  presence.Claim
		ok    bool
	}{
		{string(presence.StateConnected), presence.ClaimConnected, true},
		{string(presence.StateDisconnected), presence.ClaimDisconnected, true},
		{string(presence.StateDemoted), presence.ClaimDemoted, true},
		{"", presence.ClaimUnset, false},
		{"connected", presence.ClaimUnset, false}, // the wire enum is not case-folded
		{"RETIRED", presence.ClaimUnset, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.state), func(t *testing.T) {
			got, ok := (&ResolvedStateChangePayload{State: tc.state}).Claim()
			if ok != tc.ok {
				t.Fatalf("Claim() ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("Claim() = %v, want %v", got, tc.want)
			}
			// An unmapped state must never leak a claim presence.Decide would honour.
			if !ok && got.Valid() {
				t.Fatalf("an unmapped state produced the usable claim %v", got)
			}
		})
	}
}

// TestClaimCoversTheDeclaredVocabulary is the guard the table above cannot be: a table
// enumerates what its author remembered. The failure this exists for is adding a fourth
// presence.State and not teaching the mapper — everything still compiles, every test still
// passes, and the new state is read as a malformed payload and dropped in production.
//
// So the vocabulary is read from the file that DECLARES it. That file is in another
// package (core/presence), read by path rather than imported, because an import cannot
// enumerate constants. Run with -count=1, as CI and the workspace sweep both do.
//
// 🔴 IT PARSES THE SOURCE RATHER THAN GREPPING IT, AND THE DIFFERENCE IS THE WHOLE POINT.
// A regexp sees one spelling. It misses the idiomatic untyped form inside the same const
// block (`PresenceRetired = "RETIRED"`, whose line names no type at all), and any value
// carrying a character its class did not anticipate. Both compile, both are real states,
// and a guard that cannot see them is green while the thing it guards is broken — which is
// exactly the failure it was written to prevent, wearing the guard's own uniform.
func TestClaimCoversTheDeclaredVocabulary(t *testing.T) {
	const decl = "../presence/state.go"
	states := declaredPresenceStates(t, decl)
	if len(states) < 3 {
		t.Fatalf("found %d declared presence states in %s (%v); the scan is broken, not the vocabulary",
			len(states), decl, states)
	}
	for _, state := range states {
		if _, ok := (&ResolvedStateChangePayload{State: state}).Claim(); !ok {
			t.Errorf("presence state %q is declared on the wire but Claim() cannot map it; "+
				"every consumer will treat it as a malformed payload", state)
		}
	}
}

// declaredPresenceStates returns every string constant declared with the presence.State
// type in path, including the untyped continuations of a typed const block — which is how
// Go lets a group share the type written once on its first spec.
func declaredPresenceStates(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse the presence-state declaration at %s: %v", path, err)
	}

	var states []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		// Within one const block the type carries forward from the last spec that named
		// one, so track it across specs rather than reading each in isolation.
		typed := false
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				id, ok := vs.Type.(*ast.Ident)
				typed = ok && id.Name == "State"
			}
			if !typed {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unreadable presence-state literal %s in %s: %v", lit.Value, path, err)
				}
				states = append(states, unquoted)
			}
		}
	}
	return states
}
