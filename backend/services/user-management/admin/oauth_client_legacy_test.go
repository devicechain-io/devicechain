// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestLegacyOAuthClientMaintenance(t *testing.T) {
	s := newPartialUpdateService(t, &iam.OAuthClient{})
	ctx := context.Background()
	secret, hash, err := generateClientSecret()
	require.NoError(t, err)
	// Upgrade fixture: bypass registration, as older releases admitted this ID.
	legacy := &iam.OAuthClient{
		ClientId: "dcsa_legacy", Enabled: true, SecretHash: hash,
		NamedEntity:  rdb.NamedEntity{Name: rdb.NullStrOf(strp("Legacy")), Description: rdb.NullStrOf(strp("Original"))},
		RedirectURIs: []string{"https://example.invalid/callback"}, Scopes: []string{"read-only"},
	}
	require.NoError(t, s.iam.CreateOAuthClient(ctx, legacy))
	for _, id := range []string{"dcsa_x", "DCSA_x", "Dcsa_x"} {
		t.Run("create/"+id, func(t *testing.T) {
			client, cleartext, err := s.CreateOAuthClient(ctx, OAuthClientInput{
				ClientId: id, RedirectURIs: legacy.RedirectURIs, Scopes: legacy.Scopes, Confidential: true,
			})
			// Guards validateClientId's case-insensitive reservation on CREATE.
			require.EqualError(t, err, `clientId may not start with "dcsa_", which is reserved for service accounts`)
			require.Nil(t, client)
			require.Empty(t, cleartext)
		})
	}

	cases := []struct {
		name    string
		request OAuthClientUpdateRequest
		change  func(*iam.OAuthClient)
	}{
		{"name", OAuthClientUpdateRequest{Name: dcgraphql.OptionalStringOf("Maintained")}, func(c *iam.OAuthClient) { c.Name = rdb.NullStrOf(strp("Maintained")) }},
		{"description", OAuthClientUpdateRequest{Description: dcgraphql.OptionalStringOf("Updated")}, func(c *iam.OAuthClient) { c.Description = rdb.NullStrOf(strp("Updated")) }},
		{"redirectUris", OAuthClientUpdateRequest{RedirectUris: dcgraphql.OptionalStringListOf([]string{"https://example.invalid/new"})}, func(c *iam.OAuthClient) { c.RedirectURIs = []string{"https://example.invalid/new"} }},
		{"scopes", OAuthClientUpdateRequest{Scopes: dcgraphql.OptionalStringListOf([]string{"location"})}, func(c *iam.OAuthClient) { c.Scopes = []string{"location"} }},
	}
	for _, tc := range cases {
		t.Run("update/"+tc.name, func(t *testing.T) {
			before, err := s.iam.OAuthClientByClientId(ctx, legacy.ClientId)
			require.NoError(t, err)
			updated, err := s.UpdateOAuthClient(ctx, legacy.ClientId, &tc.request)
			// Guards UpdateOAuthClient's loadOAuthClient line against applying create-only ID validation.
			require.NoError(t, err)
			want := *before
			tc.change(&want)
			want.UpdatedAt = updated.UpdatedAt
			// Guards the update assignments: only the named field (and modification time) may change;
			// the prefix must not rewrite identity, enabled state, secret or omitted allowlists.
			require.Equal(t, want, *updated)
			stored, err := s.iam.OAuthClientByClientId(ctx, legacy.ClientId)
			require.NoError(t, err)
			require.Equal(t, want, *stored)
		})
	}
	before, err := s.iam.OAuthClientByClientId(ctx, legacy.ClientId)
	require.NoError(t, err)
	rotated, fresh, err := s.RotateOAuthClientSecret(ctx, legacy.ClientId)
	// Guards RotateOAuthClientSecret's load/set-hash lines: legacy IDs retain normal rotation.
	require.NoError(t, err)
	require.NotEmpty(t, fresh)
	require.NotEqual(t, secret, fresh)
	require.NotEqual(t, before.SecretHash, rotated.SecretHash)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(rotated.SecretHash), []byte(fresh)))
	require.ErrorIs(t, bcrypt.CompareHashAndPassword([]byte(rotated.SecretHash), []byte(secret)), bcrypt.ErrMismatchedHashAndPassword)
	want := *before
	want.SecretHash, want.UpdatedAt = rotated.SecretHash, rotated.UpdatedAt
	require.Equal(t, want, *rotated, "rotation changes only the hash and modification time")
	stored, err := s.iam.OAuthClientByClientId(ctx, legacy.ClientId)
	require.NoError(t, err)
	require.Equal(t, want, *stored)
}
