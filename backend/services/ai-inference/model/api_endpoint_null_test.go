// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// 🔴 A PROVIDER WITH NO ENDPOINT OVERRIDE STORES NULL.
//
// The column has always been nullable, but the model held a bare string, so "use the
// kind's built-in default" was stored as the empty string — on create with no endpoint, and on an update
// clearing one. The model now spells the NULL the column allows. The column is read with
// raw SQL so the assertion is about what it holds, not how a Go type renders it.
func TestAProviderWithNoEndpointStoresNull(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()
	endpoint := func() sql.NullString {
		var out sql.NullString
		require.NoError(t, api.RDB.Database.Raw("SELECT endpoint FROM ai_providers WHERE token = ?", "p").
			Scan(&out).Error)
		return out
	}

	req := claudeReq("p", nil)
	_, err := api.CreateAIProvider(ctx, req)
	require.NoError(t, err)
	require.Equal(t, sql.NullString{}, endpoint(), "a provider created with no endpoint must store NULL")

	_, err = api.UpdateAIProvider(ctx, "p", &AIProviderUpdateRequest{
		Endpoint: dcgraphql.OptionalStringOf(" https://proxy.example.invalid "),
	}, nil)
	require.NoError(t, err)
	require.Equal(t, sql.NullString{String: "https://proxy.example.invalid", Valid: true}, endpoint(),
		"an endpoint is a URL, whose grammar forbids surrounding whitespace, so it is stored trimmed")

	for name, clear := range map[string]dcgraphql.OptionalString{
		"null":  dcgraphql.ClearedString(),
		"empty": dcgraphql.OptionalStringOf(""),
	} {
		_, err = api.UpdateAIProvider(ctx, "p", &AIProviderUpdateRequest{
			Endpoint: dcgraphql.OptionalStringOf("https://proxy.example.invalid"),
		}, nil)
		require.NoError(t, err)
		_, err = api.UpdateAIProvider(ctx, "p", &AIProviderUpdateRequest{Endpoint: clear}, nil)
		require.NoError(t, err)
		require.Equalf(t, sql.NullString{}, endpoint(), "clearing the endpoint with %s must store NULL", name)
	}
}
