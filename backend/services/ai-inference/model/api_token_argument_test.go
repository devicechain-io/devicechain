// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHICH PROVIDER AN UPDATE WRITES, AND WHERE THE RENAME WENT.
//
// updateAiProvider used to take the RENAME rule rather than the reconcile, for the
// reason reloadWithSecret states in its own comment: the write-only key handle is keyed
// by the provider's immutable id "so a token rename in the same update keeps the key
// bound". That is a design intent, not an accident, which is why this family could not
// be converted mechanically — dropping the payload token would have deleted a
// capability.
//
// The capability moved to renameAiProvider, where `newToken` can mean only one thing,
// and the update input then lost its token entirely. So the disagreement the old rule
// governed is UNREPRESENTABLE rather than merely refused, and these tests are
// re-pointed at the rename's own rules.

// 🔴 A BLANK NEW TOKEN IS REFUSED, WHITESPACE INCLUDED. `token: String!` admits "", and
// it used to be written straight onto the row — leaving a provider tenants may still be
// assigned to addressable by nothing. Worse here than elsewhere: the guarded update
// path then reloaded BY that token, so the caller's own success response was already
// reading a row it could no longer name.
func TestRenameAIProvider_ABlankNewTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
			require.NoError(t, err)

			_, err = api.RenameAIProvider(ctx, "prov-a", blank)
			require.Error(t, err, "a blank new token %q was accepted", blank)

			found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a"})
			require.NoError(t, ferr)
			require.Len(t, found, 1, "the provider is no longer findable by its own token")
			assert.Equal(t, "prov-a", found[0].Token)
		})
	}
}

// THE COUNTERWEIGHT: the refusal above has not been bought by removing the rename the
// service's own design depends on.
func TestRenameAIProvider_RenamesAProvider(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
	require.NoError(t, err)

	renamed, err := api.RenameAIProvider(ctx, "prov-a", "prov-b")
	require.NoError(t, err, "a rename was refused")
	assert.Equal(t, "prov-b", renamed.Token)

	found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a", "prov-b"})
	require.NoError(t, ferr)
	require.Len(t, found, 1, "the provider answers to both tokens, so the rename copied rather than moved")
	assert.Equal(t, "prov-b", found[0].Token)
}

// 🔴 THE KEY SURVIVES THE RENAME, which is the whole reason a provider rename is safe:
// the handle is keyed by the immutable id, never by the token. This is the ai-inference
// equivalent of outbound-connectors' TestSecretSurvivesTokenRename, and it is
// RE-POINTED at the new mutation rather than deleted — it is the only evidence that
// moving the rename out of the update did not orphan the key.
func TestRenameAIProvider_KeySurvivesTheRename(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateAIProvider(ctx, claudeReq("old", strp("sk-keep")))
	require.NoError(t, err)
	require.Equal(t, "sk-keep", secretValue(t, api, ctx, "old"),
		"precondition: the key was never sealed, so surviving a rename proves nothing")

	_, err = api.RenameAIProvider(ctx, "old", "new")
	require.NoError(t, err)
	assert.Equal(t, "sk-keep", secretValue(t, api, ctx, "new"))
}

// Renaming to the token the provider already has is an idempotent SUCCESS, so the retry
// of a rename that half-failed is safe — and it must not fall into the collision check
// below and refuse the provider its own name.
func TestRenameAIProvider_SameTokenIsANoOpSuccess(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", strp("sk-keep")))
	require.NoError(t, err)

	same, err := api.RenameAIProvider(ctx, "prov-a", "prov-a")
	require.NoError(t, err, "renaming a provider to its own token must succeed")
	assert.Equal(t, "prov-a", same.Token)
	assert.Equal(t, "sk-keep", secretValue(t, api, ctx, "prov-a"),
		"the no-op rename disturbed the API key")
}

// A token another provider already holds is refused BY NAME rather than left to arrive
// as a unique-index violation the caller has to decode. The provider list is
// INSTANCE-global, so this uniqueness is instance-wide (uix_ai_providers_token), and
// newTestApi runs the REAL migrations — so the index the refusal stands in for actually
// exists here.
func TestRenameAIProvider_RefusesATokenAlreadyInUse(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, token := range []string{"prov-a", "taken"} {
		_, err := api.CreateAIProvider(ctx, claudeReq(token, nil))
		require.NoError(t, err)
	}

	_, err := api.RenameAIProvider(ctx, "prov-a", "taken")
	require.Error(t, err, "renaming onto an existing provider's token must be refused")
	assert.Contains(t, err.Error(), "already in use",
		"the refusal must name the collision rather than surface as a constraint violation")

	found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a", "taken"})
	require.NoError(t, ferr)
	assert.Len(t, found, 2, "the refused rename disturbed one of the two providers")
}

// An unknown token is a not-found, not a silent create and not "the only provider there
// is".
func TestRenameAIProvider_UnknownTokenIsNotFound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
	require.NoError(t, err)

	_, err = api.RenameAIProvider(ctx, "no-such-provider", "prov-b")
	require.Error(t, err, "renaming an unknown provider succeeded")

	found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.Equal(t, "prov-a", found[0].Token,
		"a rename addressed to an unknown token moved the seeded provider")
}
