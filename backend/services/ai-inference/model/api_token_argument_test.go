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

// WHICH PROVIDER AN UPDATE WRITES.
//
// updateAiProvider takes the RENAME rule rather than the reconcile, for the reason
// reloadWithSecret states in its own comment: the write-only key handle is keyed by
// the provider's immutable id "so a token rename in the same update keeps the key
// bound". That is a design intent, not an accident, so the rename stays.
//
// What the rule refuses is a BLANK new token. `token: String!` admits "", and it used
// to be written straight onto the row — leaving a provider that tenants may still be
// assigned to addressable by nothing, with the mutation returning success. Worse here
// than elsewhere: the guarded path then reloads BY that token, so the caller's own
// success response was already reading a row it could no longer name.

func TestUpdateAIProvider_ABlankPayloadTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
			require.NoError(t, err)

			_, err = api.UpdateAIProvider(ctx, "prov-a", claudeReq(blank, nil), nil)
			require.Error(t, err, "a blank payload token %q was accepted", blank)

			found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a"})
			require.NoError(t, ferr)
			require.Len(t, found, 1, "the provider is no longer findable by its own token")
			assert.Equal(t, "prov-a", found[0].Token)
		})
	}
}

// THE COUNTERWEIGHT: the refusal has not been bought by removing the rename the
// service's own design depends on.
func TestUpdateAIProvider_ADifferingTokenStillRenames(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
	require.NoError(t, err)

	_, err = api.UpdateAIProvider(ctx, "prov-a", claudeReq("prov-b", nil), nil)
	require.NoError(t, err, "a rename was refused")

	found, ferr := api.AIProvidersByToken(ctx, []string{"prov-b"})
	require.NoError(t, ferr)
	require.Len(t, found, 1, "the renamed provider is not findable")
}
