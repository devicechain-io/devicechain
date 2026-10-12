// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// The session policy as a superuser and a signing-in person reach it: the served public and
// admin schemas over the real handlers, the real resolvers, a real identity.Manager.

func TestSessionPolicyOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	s.s.CreateMember("member@example.com", "pw-1")
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)

	const (
		enforceDoc = `mutation($e:Boolean!){setTenantSsoEnforced(tenant:"acme",enforced:$e){token ssoEnforced}}`
		ageDoc     = `mutation($s:Int!){setIdpSessionMaxAge(connection:"acme-idp",seconds:$s){token sessionMaxAgeSeconds}}`
		revokeDoc  = `mutation($e:String!){revokeSessions(email:$e)}`
		clearDoc   = `mutation($e:String!){clearPassword(email:$e){email}}`
	)

	// None of the four is open to the unauthenticated.
	for _, doc := range []string{enforceDoc, ageDoc, revokeDoc, clearDoc} {
		require.Equal(t, http.StatusUnauthorized, post(t, s.admin, doc,
			map[string]any{"e": true, "s": 3600}, "", "", "").Status)
	}

	// A password session inside acme from BEFORE enforcement, whose refresh is refused after;
	// and an identity that belongs to no tenant at all, the outsider.
	const refreshDoc = `mutation($r:String!){refresh(refreshToken:$r){accessToken}}`
	var before struct {
		IdentityToken string
	}
	post(t, s.pub, loginSu, map[string]any{"e": "member@example.com", "p": "pw-1"}, "", "", "").field(t, "login", &before)
	var pair struct{ RefreshToken string }
	post(t, s.pub, `mutation($i:String!){selectTenant(identityToken:$i,tenant:"acme"){refreshToken}}`,
		map[string]any{"i": before.IdentityToken}, "", "", "").field(t, "selectTenant", &pair)
	s.s.CreateIdentity("outsider@example.com", "pw-out")
	var outsider struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": "outsider@example.com", "p": "pw-out"}, "", "", "").field(t, "login", &outsider)

	// Enforcement: read back on the tenant, refused to a password member with the typed
	// code, not to the superuser, and listed to the member MARKED.
	var tenant struct {
		Token       string
		SsoEnforced bool
	}
	post(t, s.admin, enforceDoc, map[string]any{"e": true}, su.IdentityToken, "", "").field(t, "setTenantSsoEnforced", &tenant)
	require.Equal(t, "acme", tenant.Token)
	require.True(t, tenant.SsoEnforced)

	var member struct {
		IdentityToken string
		Memberships   []struct {
			Tenant      string
			SsoRequired bool
		}
	}
	const loginMember = `mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken memberships{tenant ssoRequired}}}`
	post(t, s.pub, loginMember, map[string]any{"e": "member@example.com", "p": "pw-1"}, "", "", "").field(t, "login", &member)
	require.Len(t, member.Memberships, 1, "the password sign-in hid a tenant that requires single sign-on")
	require.Equal(t, "acme", member.Memberships[0].Tenant)
	require.True(t, member.Memberships[0].SsoRequired, "the password sign-in listed an enforced tenant unmarked")
	var reread []struct{ SsoRequired bool }
	post(t, s.pub, `query($i:String!){identityMemberships(identityToken:$i){ssoRequired}}`,
		map[string]any{"i": member.IdentityToken}, "", "", "").field(t, "identityMemberships", &reread)
	require.Len(t, reread, 1)
	require.True(t, reread[0].SsoRequired, "the membership re-read listed an enforced tenant unmarked")

	r := post(t, s.pub, selectSso, map[string]any{"i": member.IdentityToken}, "", "", "")
	require.Equal(t, identity.CodeSsoRequired, r.code(t), "selectTenant refused a member without the typed code: %s", r.Body)
	r = post(t, s.pub, refreshDoc, map[string]any{"r": pair.RefreshToken}, "", "", "")
	require.Equal(t, identity.CodeSsoRequired, r.code(t), "refresh refused a member's password session without the typed code: %s", r.Body)
	r = post(t, s.pub, refreshDoc, map[string]any{"r": pair.RefreshToken}, "", "", "")
	require.Equal(t, "invalid or expired token", r.failed(t), "the refused refresh token was not consumed")
	require.Empty(t, r.code(t))

	// Nobody but a member learns that acme requires single sign-on: not an identity that is
	// not a member, and not a caller with no valid token.
	r = post(t, s.pub, selectSso, map[string]any{"i": outsider.IdentityToken}, "", "", "")
	require.Equal(t, "invalid credentials", r.failed(t))
	require.Empty(t, r.code(t), "a non-member was told the tenant requires single sign-on")
	r = post(t, s.pub, selectSso, map[string]any{"i": "not-a-token"}, "", "", "")
	require.Equal(t, "invalid or expired token", r.failed(t))
	require.Empty(t, r.code(t), "an unauthenticated caller was told the tenant requires single sign-on")

	var su2 struct{ AccessToken string }
	post(t, s.pub, selectSso, map[string]any{"i": su.IdentityToken}, "", "", "").field(t, "selectTenant", &su2)
	require.NotEmpty(t, su2.AccessToken, "control: the superuser enters an enforced tenant")

	// Turning it off lets the member in, and an unknown tenant is a named failure.
	post(t, s.admin, enforceDoc, map[string]any{"e": false}, su.IdentityToken, "", "").field(t, "setTenantSsoEnforced", &tenant)
	require.False(t, tenant.SsoEnforced)
	var ok struct{ AccessToken string }
	post(t, s.pub, selectSso, map[string]any{"i": member.IdentityToken}, "", "", "").field(t, "selectTenant", &ok)
	require.NotEmpty(t, ok.AccessToken)
	r = post(t, s.admin, `mutation{setTenantSsoEnforced(tenant:"nowhere",enforced:true){token}}`, nil, su.IdentityToken, "", "")
	require.Equal(t, "tenant not found", r.failed(t))

	// The ceiling: bounds on the wire, the value readable on the connection.
	var conn struct {
		Token                string
		SessionMaxAgeSeconds int
	}
	post(t, s.admin, ageDoc, map[string]any{"s": 7200}, su.IdentityToken, "", "").field(t, "setIdpSessionMaxAge", &conn)
	require.Equal(t, 7200, conn.SessionMaxAgeSeconds)
	for _, bad := range []int{0, 899, 604801} {
		r = post(t, s.admin, ageDoc, map[string]any{"s": bad}, su.IdentityToken, "", "")
		require.Contains(t, r.failed(t), "session ceiling")
	}
	var read struct {
		IdpConnection struct{ SessionMaxAgeSeconds int }
	}
	r = post(t, s.admin, `{idpConnection(token:"acme-idp"){sessionMaxAgeSeconds}}`, nil, su.IdentityToken, "", "")
	require.Empty(t, r.Errors, r.Body)
	require.NoError(t, json.Unmarshal(r.Data["idpConnection"], &read.IdpConnection))
	require.Equal(t, 7200, read.IdpConnection.SessionMaxAgeSeconds, "a refused ceiling was stored")

	// Sign out everywhere: the member's two sign-ins (before and after enforcement) end, a
	// second call finds nothing, and an unknown identity is named.
	var n int
	post(t, s.admin, revokeDoc, map[string]any{"e": "member@example.com"}, su.IdentityToken, "", "").field(t, "revokeSessions", &n)
	require.Equal(t, 2, n)
	r = post(t, s.pub, selectSso, map[string]any{"i": member.IdentityToken}, "", "", "")
	require.Equal(t, "invalid or expired token", r.failed(t))
	post(t, s.admin, revokeDoc, map[string]any{"e": "member@example.com"}, su.IdentityToken, "", "").field(t, "revokeSessions", &n)
	require.Equal(t, 0, n)
	r = post(t, s.admin, revokeDoc, map[string]any{"e": "ghost@example.com"}, su.IdentityToken, "", "")
	require.Equal(t, "identity not found", r.failed(t))

	// Clearing the password: refused while the identity has no other way in; once it is
	// linked, the identity comes back and the password no longer signs in.
	r = post(t, s.admin, clearDoc, map[string]any{"e": "member@example.com"}, su.IdentityToken, "", "")
	require.Contains(t, r.failed(t), "no way in")
	require.NoError(t, s.s.Manager.LinkIdentity(context.Background(), "member@example.com", s.s.Connection, "sub-member"))
	var id struct{ Email string }
	post(t, s.admin, clearDoc, map[string]any{"e": "member@example.com"}, su.IdentityToken, "", "").field(t, "clearPassword", &id)
	require.Equal(t, "member@example.com", id.Email)
	r = post(t, s.pub, loginMember, map[string]any{"e": "member@example.com", "p": "pw-1"}, "", "", "")
	require.Equal(t, "invalid credentials", r.failed(t))
}

// The refusals that keep a tenant's people from being locked out reach the administrator
// with a code the console can translate, and with prose that names no API operation.
func TestSsoLockoutRefusalsOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)
	admin := func(doc string, vars map[string]any) wireReply {
		t.Helper()
		return post(t, s.admin, doc, vars, su.IdentityToken, "", "")
	}
	const (
		enforceDoc = `mutation($e:Boolean!){setTenantSsoEnforced(tenant:"acme",enforced:$e){ssoEnforced}}`
		tenantsDoc = `mutation($ts:[String!]!){setIdpConnectionTenants(token:"acme-idp",tenants:$ts){token}}`
		disableDoc = `mutation{updateIdpConnection(token:"acme-idp",request:{enabled:false}){token}}`
		deleteDoc  = `mutation{deleteIdpConnection(token:"acme-idp")}`
	)
	refused := func(r wireReply, code string) {
		t.Helper()
		require.Equal(t, code, r.code(t), r.Body)
		msg := r.failed(t)
		for _, op := range []string{"setTenantSsoEnforced", "setIdpConnectionTenants", "updateIdpConnection", "deleteIdpConnection"} {
			require.NotContains(t, msg, op, "the refusal's prose names an API operation")
		}
	}

	// No connection serves acme: requiring single sign-on is refused, by code.
	require.Empty(t, admin(tenantsDoc, map[string]any{"ts": []string{}}).Errors)
	refused(admin(enforceDoc, map[string]any{"e": true}), identity.CodeSsoEnforceNoConnection)

	// Served again and required: every way of removing the last connection is refused, by
	// code, naming the tenant it would strand.
	require.Empty(t, admin(tenantsDoc, map[string]any{"ts": []string{"acme"}}).Errors)
	require.Empty(t, admin(enforceDoc, map[string]any{"e": true}).Errors)
	r := admin(tenantsDoc, map[string]any{"ts": []string{}})
	refused(r, identity.CodeSsoEnforcedTenantStranded)
	require.Equal(t, []any{"acme"}, r.Errors[0].Extensions["tenants"])
	refused(admin(disableDoc, nil), identity.CodeSsoEnforcedTenantStranded)
	refused(admin(deleteDoc, nil), identity.CodeSsoEnforcedTenantStranded)

	// Control: once the requirement is off, the connection can go.
	require.Empty(t, admin(enforceDoc, map[string]any{"e": false}).Errors)
	r = admin(deleteDoc, nil)
	require.Empty(t, r.Errors, r.Body)
}
