// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"
)

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
		res := serve(t, f.member("group:north"), q, nil)
		if len(res.errs) == 0 {
			t.Fatalf("a scoped member read %s: %v", field, res.data)
		}
	}
	if res := serve(t, f.member("group:north"), fmt.Sprintf(base, ""), nil); len(res.errs) != 0 {
		t.Fatalf("a scoped member was refused token and name: %v", res.errs)
	}
}

// A scoped member cannot write a group at all, so it cannot plant a selector or metadata:
// create and update are refused whole, with the scope refusal.
func TestScopedMemberCannotWriteAGroup(t *testing.T) {
	f := newGroupScopeFixture(t)
	create := `mutation($r: EntityGroupCreateRequest!) { createEntityGroup(request: $r) { token } }`
	res := serve(t, f.member("group:north"), create, map[string]any{"r": map[string]any{
		"token": "sneaky", "memberType": "device", "membershipMode": "dynamic", "selector": `true`, "metadata": "{}"}})
	if len(res.errs) == 0 {
		t.Fatalf("a scoped member created a group: %v", res.data)
	}
	update := `mutation($r: EntityGroupUpdateRequest!) { updateEntityGroup(token: "north", request: $r) { token } }`
	res = serve(t, f.member("group:north"), update, map[string]any{"r": map[string]any{"selector": `true`, "metadata": "{}"}})
	if len(res.errs) == 0 {
		t.Fatalf("a scoped member updated a group: %v", res.data)
	}
}
