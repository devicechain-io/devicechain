// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

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

	// Enforcement: read back on the tenant, refused to a password member, not to the
	// superuser, and listed nowhere for the member.
	var tenant struct {
		Token       string
		SsoEnforced bool
	}
	post(t, s.admin, enforceDoc, map[string]any{"e": true}, su.IdentityToken, "", "").field(t, "setTenantSsoEnforced", &tenant)
	require.Equal(t, "acme", tenant.Token)
	require.True(t, tenant.SsoEnforced)

	var member struct {
		IdentityToken string
		Memberships   []struct{ Tenant string }
	}
	const loginMember = `mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken memberships{tenant}}}`
	post(t, s.pub, loginMember, map[string]any{"e": "member@example.com", "p": "pw-1"}, "", "", "").field(t, "login", &member)
	require.Empty(t, member.Memberships, "the password sign-in listed a tenant that requires single sign-on")
	r := post(t, s.pub, selectSso, map[string]any{"i": member.IdentityToken}, "", "", "")
	require.Equal(t, "invalid credentials", r.failed(t))
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

	// Sign out everywhere: the member's sign-in ends, a second call finds nothing, and an
	// unknown identity is named.
	var n int
	post(t, s.admin, revokeDoc, map[string]any{"e": "member@example.com"}, su.IdentityToken, "", "").field(t, "revokeSessions", &n)
	require.Equal(t, 1, n)
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
