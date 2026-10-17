// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// addWireMember adds an enabled member of acme holding roles, limited to anchors.
func (w *wireEnv) addWireMember(t *testing.T, email string, roles []string, anchors ...admin.ScopeAnchorInput) {
	t.Helper()
	ctx := context.Background()
	_, err := w.svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: email, Password: "pw", Enabled: true})
	require.NoError(t, err)
	_, err = w.svc.AddScopedMembership(ctx, email, "acme", roles, anchors)
	require.NoError(t, err)
}

// The service decides what a change may grant from what the caller holds, not whether the
// caller may change members at all: that is the resolver's member:write gate. A member who
// may only READ members, changing a viewer into a viewer limited to a customer, is inside
// every rule the service applies, so only the gate refuses it. Each of the three ways to
// change a member's access is asked, and the member is left as it was.
func TestChangingAMembersAccessNeedsMemberWrite(t *testing.T) {
	w := newWireEnv(t)
	ctx := context.Background()
	_, err := w.svc.CreateRole(ctx, admin.RoleInput{Scope: "tenant", Token: "reader", Authorities: []string{"member:read"}})
	require.NoError(t, err)
	w.addWireMember(t, "reader@acme.example", []string{"reader"})
	w.addWireMember(t, "pat@acme.example", []string{iam.ViewerRoleToken})
	reader := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "reader@acme.example",
		Authorities: append([]string{"member:read"}, identity.ViewerAuthorities()...)}
	vars := map[string]any{"e": "pat@acme.example", "r": []any{iam.ViewerRoleToken},
		"s": []any{map[string]any{"kind": "CUSTOMER", "token": "c1"}}}
	for name, doc := range map[string]string{
		"setMemberRoles":  `mutation($e: String!, $r: [String!]!) { setMemberRoles(email: $e, roleTokens: $r) { email } }`,
		"setMemberScope":  `mutation($e: String!, $s: [ScopeAnchorInput!]!) { setMemberScope(email: $e, scope: $s) { email } }`,
		"setMemberAccess": `mutation($e: String!, $r: [String!]!, $s: [ScopeAnchorInput!]!) { setMemberAccess(email: $e, roleTokens: $r, scope: $s) { email } }`,
	} {
		v := map[string]any{"e": vars["e"]}
		if strings.Contains(doc, "$r") {
			v["r"] = vars["r"]
		}
		if strings.Contains(doc, "$s") {
			v["s"] = vars["s"]
		}
		_, ext := w.exec(t, reader, doc, v)
		require.NotNilf(t, ext, "%s answered a caller without member:write", name)
		require.Equalf(t, "FORBIDDEN", ext["code"], "%s: %v", name, ext)
	}
	m, err := w.svc.Member(w.ctx(bossClaims()), "pat@acme.example")
	require.NoError(t, err)
	require.Empty(t, m.Membership.Scope, "a refused change limited the member")

	// The counterweight: the administrator's combined change lands both halves on the wire.
	data, ext := w.exec(t, bossClaims(), `mutation($e: String!, $r: [String!]!, $s: [ScopeAnchorInput!]!) {
		setMemberAccess(email: $e, roleTokens: $r, scope: $s) { email roles { token } scope { kind token } } }`, vars)
	require.Nil(t, ext)
	got := data["setMemberAccess"].(map[string]any)
	require.Equal(t, []any{map[string]any{"token": iam.ViewerRoleToken}}, got["roles"])
	require.Equal(t, []any{map[string]any{"kind": "CUSTOMER", "token": "c1"}}, got["scope"])
}

// The member list's three server-side cuts arrive at the store as sent: a role token, a
// scope anchor of one KIND (a group named like a customer is not that customer), and who
// manages the membership. Each is asked alone and the answer named.
func TestTheMemberFiltersReachTheStore(t *testing.T) {
	w := newWireEnv(t)
	ctx := context.Background()
	w.addWireMember(t, "ann@acme.example", []string{iam.ViewerRoleToken}, admin.ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: "c1"})
	w.addWireMember(t, "dee@acme.example", []string{iam.ViewerRoleToken}, admin.ScopeAnchorInput{Kind: auth.AnchorKindGroup, Token: "c1"})
	ann, err := w.store.IdentityByEmail(ctx, "ann@acme.example")
	require.NoError(t, err)
	require.NoError(t, w.store.MarkMembershipsManagedBy(ctx, ann.ID, []string{"acme"}, 1))

	const q = `query($c: MemberSearchCriteria!) { members(criteria: $c) { results { email } pagination { totalRecords } } }`
	emails := func(extra map[string]any) []string {
		t.Helper()
		c := map[string]any{"pageNumber": 1, "pageSize": 50}
		for k, v := range extra {
			c[k] = v
		}
		data, ext := w.exec(t, bossClaims(), q, map[string]any{"c": c})
		require.Nil(t, ext, "%v", ext)
		out := []string{}
		for _, r := range data["members"].(map[string]any)["results"].([]any) {
			out = append(out, r.(map[string]any)["email"].(string))
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, []string{"ann@acme.example", "boss@acme.example", "dee@acme.example"}, emails(nil), "control: no filter")
	require.Equal(t, []string{"boss@acme.example"}, emails(map[string]any{"roleToken": iam.TenantAdminRoleToken}))
	require.Equal(t, []string{"ann@acme.example"}, emails(map[string]any{"scopeAnchor": map[string]any{"kind": "CUSTOMER", "token": "c1"}}))
	require.Equal(t, []string{"dee@acme.example"}, emails(map[string]any{"scopeAnchor": map[string]any{"kind": "GROUP", "token": "c1"}}))
	require.Equal(t, []string{"ann@acme.example"}, emails(map[string]any{"managedByIdentityProvider": true}))
	require.Equal(t, []string{"boss@acme.example", "dee@acme.example"}, emails(map[string]any{"managedByIdentityProvider": false}))

	// What is not an anchor is refused, not read as "no filter".
	_, ext := w.exec(t, bossClaims(), q, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 50,
		"scopeAnchor": map[string]any{"kind": "CUSTOMER", "token": "not a token"}}})
	require.NotNil(t, ext, "a malformed anchor was answered")
	_, ext = w.exec(t, bossClaims(), q, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 50,
		"roleToken": strings.Repeat("r", 257)}})
	require.NotNil(t, ext, "an over-long role token was answered")
	require.Equal(t, "LIMIT_EXCEEDED", ext["code"], "%v", ext)
}

// The combined change bounds its role list before anything reads it, as setMemberRoles does:
// a list over the bound is refused LIMIT_EXCEEDED rather than resolved token by token.
func TestSetMemberAccessBoundsItsRoleList(t *testing.T) {
	w := newWireEnv(t)
	w.addWireMember(t, "pat@acme.example", []string{iam.ViewerRoleToken})
	over := make([]any, maxAdminListEntries+1)
	for i := range over {
		over[i] = iam.ViewerRoleToken
	}
	const doc = `mutation($r: [String!]!) { setMemberAccess(email: "pat@acme.example", roleTokens: $r, scope: []) { email } }`
	_, ext := w.exec(t, bossClaims(), doc, map[string]any{"r": over})
	require.NotNil(t, ext, "an over-long role list was answered")
	require.Equal(t, "LIMIT_EXCEEDED", ext["code"], "%v", ext)
	// The counterweight: a list at the bound is not refused for its length.
	_, ext = w.exec(t, bossClaims(), doc, map[string]any{"r": over[:maxAdminListEntries]})
	require.Nil(t, ext, "%v", ext)
}
