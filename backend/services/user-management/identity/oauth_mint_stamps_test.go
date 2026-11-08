// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

// 🔴 EVERY OAUTH-MINTED TOKEN NAMES ITS CLIENT AND ITS SCOPE, on the grant AND on every refresh.
//
// auth.IsAgentOrigin treats a token as an agent's when it carries a client id or a scope, and
// the audience planes that could back it up run in observe mode. So the predicate is only as
// reliable as the mint paths that stamp those two claims; a refresh that dropped one would hand
// an agent a token the gate reads as a person's. This drives the real grant and the real
// refresh and reads both tokens of both mints back through the ordinary validator.
func TestOAuthMintAlwaysStampsClientIdAndScope(t *testing.T) {
	const client = "claude-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly, auth.ScopeLocation)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme", string(auth.LocationRead))
	e.seedSuperuser(t, "root@example.invalid")

	scope := auth.ScopeReadOnly + " " + auth.ScopeLocation
	for _, who := range []string{"pat@example.invalid", "root@example.invalid"} {
		t.Run(who, func(t *testing.T) {
			check := func(stage string, toks *OAuthTokens) {
				access := e.claimsOf(t, toks.AccessToken)
				refresh, err := e.validator.ValidateRefresh(toks.RefreshToken)
				require.NoError(t, err, stage)
				for kind, c := range map[string]*auth.Claims{"access": access, "refresh": refresh} {
					require.Equal(t, client, c.ClientId, "%s: the %s token carries no client_id", stage, kind)
					require.Equal(t, scope, c.Scope, "%s: the %s token carries no scope", stage, kind)
					require.True(t, auth.IsAgentOrigin(c), "%s: the %s token is not agent-origin", stage, kind)
				}
			}

			toks, err := e.m.mintScopedGrant(context.Background(), who, e.epochOf(t, who), e.sidOf(t, who),
				"acme", scope, scope, []string{"https://mcp.example.invalid"}, client)
			require.NoError(t, err)
			check("grant", toks)

			// Two refreshes: the rotated token must itself still be refreshable and still marked.
			for i := 1; i <= 2; i++ {
				toks, err = e.m.RefreshOAuth(context.Background(), toks.RefreshToken, "", client, nil)
				require.NoError(t, err, "refresh %d", i)
				check("refresh", toks)
			}

			// A refresh narrowed at the request keeps the client on both tokens; the ACCESS
			// scope narrows, the REFRESH scope stays the grant.
			narrowed, err := e.m.RefreshOAuth(context.Background(), toks.RefreshToken, auth.ScopeReadOnly, client, nil)
			require.NoError(t, err)
			a := e.claimsOf(t, narrowed.AccessToken)
			require.Equal(t, client, a.ClientId)
			require.Equal(t, auth.ScopeReadOnly, a.Scope)
			r, err := e.validator.ValidateRefresh(narrowed.RefreshToken)
			require.NoError(t, err)
			require.Equal(t, client, r.ClientId)
			require.Equal(t, scope, r.Scope)
		})
	}
}

// 🔴 THE SAME SID, THROUGH THE WHOLE LIFECYCLE. The access token is the one that reaches the data
// plane, so it is the one that has to name the sign-in a journal row descends from: code
// redemption (here, the equivalent mint), then every refresh, each yielding an access token
// and a replacement refresh token carrying the SAME non-empty sid.
func TestOAuthAccessCarriesSidThroughRefresh(t *testing.T) {
	const client = "claude-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme")
	e.seedSuperuser(t, "root@example.invalid")

	for _, who := range []string{"pat@example.invalid", "root@example.invalid"} {
		t.Run(who, func(t *testing.T) {
			sid := e.sidOf(t, who)
			require.NotEmpty(t, sid)
			toks, err := e.m.mintScopedGrant(context.Background(), who, e.epochOf(t, who), sid,
				"acme", auth.ScopeReadOnly, auth.ScopeReadOnly, nil, client)
			require.NoError(t, err)

			for stage := 0; stage < 3; stage++ {
				access := e.claimsOf(t, toks.AccessToken)
				refresh, err := e.validator.ValidateRefresh(toks.RefreshToken)
				require.NoError(t, err)
				require.Equal(t, sid, access.SessionID, "stage %d: the access token lost the sign-in's sid", stage)
				require.Equal(t, sid, refresh.SessionID, "stage %d: the refresh token lost the sign-in's sid", stage)
				require.True(t, auth.SameSession(access.SessionID, refresh.SessionID))

				toks, err = e.m.RefreshOAuth(context.Background(), toks.RefreshToken, "", client, nil)
				require.NoError(t, err)
			}
		})
	}
}

// A token minted without a sid (an old-shape token, or a service account's) is still valid and
// reads as "unknown", never as a match for another unknown.
func TestAMissingSidReadsAsUnknown(t *testing.T) {
	e := newMintTestEnv(t)
	tok, err := e.m.issuer.IssueOAuthAccess("acme", "a@example.invalid", nil, []string{"device:read"},
		auth.ScopeReadOnly, nil, false, "old-client", "jti-old")
	require.NoError(t, err)
	c, err := e.validator.Validate(tok.Token)
	require.NoError(t, err, "a token without sid must still validate")
	require.Empty(t, c.SessionID)
	require.False(t, auth.SameSession(c.SessionID, c.SessionID), "two unknown sessions matched")
}
