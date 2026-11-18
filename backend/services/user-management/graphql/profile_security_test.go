// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestUpdateProfileRequiresHumanAccessToken(t *testing.T) {
	w := newWireEnv(t)
	const doc = `mutation { updateProfile(request: {firstName: "Changed"}) { firstName } }`
	for _, kind := range []string{"oauth read-only", "proposal-only", "identity", "refresh", "service"} {
		t.Run(kind, func(t *testing.T) {
			claims := bossClaims()
			switch kind {
			case "oauth read-only":
				claims.ClientId, claims.Scope = "agent-client", "read-only"
				claims.Authorities = []string{"user:read"}
			case "proposal-only":
				// No client, scope or audience: the proposal authority alone identifies an agent.
				claims.Authorities = []string{"device:propose"}
			case "identity":
				claims.TokenType = auth.TokenTypeIdentity
			case "refresh":
				claims.TokenType = auth.TokenTypeRefresh
			case "service":
				claims.TokenType = auth.TokenTypeService
			}
			before, err := w.store.IdentityByEmail(context.Background(), claims.Username)
			require.NoError(t, err)
			res := w.schema.Exec(w.ctx(claims), doc, "", nil)
			require.NotEmpty(t, res.Errors)
			require.Contains(t, res.Errors[0].Message, auth.ErrForbidden.Error())
			after, err := w.store.IdentityByEmail(context.Background(), claims.Username)
			require.NoError(t, err)
			require.Equal(t, before.FirstName, after.FirstName, "refused token changed the profile")
		})
	}

	res := w.schema.Exec(w.ctx(bossClaims()), doc, "", nil)
	require.Empty(t, res.Errors, "human console access token must pass")
	id, err := w.store.IdentityByEmail(context.Background(), bossClaims().Username)
	require.NoError(t, err)
	require.True(t, id.FirstName.Valid)
	require.Equal(t, "Changed", id.FirstName.String)
}
