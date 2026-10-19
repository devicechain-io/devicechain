// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// requireScopeRefusal fails unless the request was refused, and refused by the scope: any
// other error (an authority the caller lacks, a missing group) proves nothing about it.
func requireScopeRefusal(t *testing.T, res served, what string) {
	t.Helper()
	if len(res.errs) == 0 {
		t.Fatalf("%s was served to a scoped member: %v", what, res.data)
	}
	for _, e := range res.errs {
		if !isScopeRefusal(e) {
			t.Fatalf("%s was refused, but not by the scope: %v", what, res.errs)
		}
	}
}

func isScopeRefusal(e *gqlerrors.QueryError) bool {
	return errors.Is(e.ResolverError, auth.ErrScopeUnsupported) || e.Message == auth.ErrScopeUnsupported.Error()
}

// A member limited to a group reads the group's token and name and nothing that describes it:
// description, metadata and the selector are the operator's own text about what the group
// holds and can name other customers (ADR-081). The operator reads them all, so the refusal
// is the scope's and the fields are not simply empty.
func TestScopedMemberReadsAGroupsTokenAndNameOnly(t *testing.T) {
	f := newGroupScopeFixture(t)
	ctx := f.operator()
	describe := serve(t, ctx, `mutation($r: EntityGroupUpdateRequest!) { updateEntityGroup(token: "north", request: $r) { token } }`,
		map[string]any{"r": map[string]any{"description": "devices of acme and globex", "metadata": `{"note":"globex"}`}})
	requireServed(t, describe, "describe the group")

	const base = `{ entityGroupsByToken(tokens: ["north"]) { token name %s } }`
	for _, field := range []string{"description", "metadata", "selector"} {
		q := fmt.Sprintf(base, field)
		if res := serve(t, f.operator(), q, nil); len(res.errs) != 0 {
			t.Fatalf("operator refused %s: %v", field, res.errs)
		}
		requireScopeRefusal(t, serve(t, f.member("group:north"), q, nil), field)
	}
	res := serve(t, f.member("group:north"), fmt.Sprintf(base, ""), nil)
	if len(res.errs) != 0 {
		t.Fatalf("a scoped member was refused token and name: %v", res.errs)
	}
	if got := tokensAt(t, res.data, "entityGroupsByToken"); len(got) != 1 || got[0] != "north" {
		t.Fatalf("a scoped member read groups %v, want [north]", got)
	}
}

// A scoped member cannot write a group at all, so it cannot plant a selector or metadata:
// create and update are refused whole, with the scope refusal. The member holds device:write
// here, so the refusal cannot be the missing authority's.
func TestScopedMemberCannotWriteAGroup(t *testing.T) {
	f := newGroupScopeFixture(t)
	writer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities:  []string{string(auth.DeviceRead), string(auth.DeviceWrite)},
		ScopeAnchors: []string{"group:north"}})
	create := `mutation($r: EntityGroupCreateRequest!) { createEntityGroup(request: $r) { token } }`
	requireScopeRefusal(t, serve(t, writer, create, map[string]any{"r": map[string]any{
		"token": "sneaky", "memberType": "device", "membershipMode": "dynamic", "selector": `true`, "metadata": "{}"}}),
		"createEntityGroup")
	update := `mutation($r: EntityGroupUpdateRequest!) { updateEntityGroup(token: "north", request: $r) { token } }`
	requireScopeRefusal(t, serve(t, writer, update, map[string]any{"r": map[string]any{"selector": `true`, "metadata": "{}"}}),
		"updateEntityGroup")
}
