// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/credbinding"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

func TestAIProviderCredentialBinding(t *testing.T) {
	for _, mode := range []string{"endpoint", "kind", "replace", "clear", "neutral", "default", "default-port", "anonymous"} {
		t.Run(mode, func(t *testing.T) {
			api := newTestApi(t)
			ctx := context.Background()
			secret := strp("old")
			if mode == "anonymous" {
				secret = nil
			}
			create := &AIProviderCreateRequest{Token: "model", Kind: "anthropic", Model: "model", Secret: secret, Enabled: true}
			if mode == "kind" {
				create.Endpoint = strp("https://model.example")
			}
			row, err := api.CreateAIProvider(ctx, create)
			require.NoError(t, err)
			req := &AIProviderUpdateRequest{Endpoint: dcgraphql.OptionalStringOf("https://other.example")}
			want := "old"
			switch mode {
			case "kind":
				req = &AIProviderUpdateRequest{Kind: dcgraphql.OptionalStringOf("openai-compatible")}
			case "replace":
				req.Secret = dcgraphql.OptionalStringOf("new")
				want = "new"
			case "clear":
				req.Secret = dcgraphql.OptionalString{Set: true}
				want = ""
			case "neutral":
				req = &AIProviderUpdateRequest{Name: dcgraphql.OptionalStringOf("new"), Model: dcgraphql.OptionalStringOf("other-model"), Enabled: dcgraphql.OptionalBoolOf(false)}
			case "default":
				req.Endpoint = dcgraphql.OptionalStringOf("https://api.anthropic.com/")
			case "default-port":
				req.Endpoint = dcgraphql.OptionalStringOf("https://API.ANTHROPIC.COM:443/")
			case "anonymous":
				want = ""
			}
			_, err = api.UpdateAIProvider(ctx, "model", req, nil)
			if mode == "endpoint" || mode == "kind" {
				var refusal *credbinding.ResupplyRequiredError
				require.True(t, errors.As(err, &refusal), "%v", err)
				require.Equal(t, credbinding.Code, refusal.Extensions()["code"])
				rows, e := api.AIProvidersByToken(ctx, []string{"model"})
				require.NoError(t, e)
				require.Equal(t, row.Endpoint, rows[0].Endpoint)
				require.Equal(t, row.Kind, rows[0].Kind)
				require.True(t, row.UpdatedAt.Equal(rows[0].UpdatedAt))
			} else {
				require.NoError(t, err)
			}
			if mode == "neutral" {
				rows, e := api.AIProvidersByToken(ctx, []string{"model"})
				require.NoError(t, e)
				require.Equal(t, "new", rows[0].Name.String)
				require.Equal(t, "other-model", rows[0].ModelID)
				require.False(t, rows[0].Enabled)
			}
			require.Equal(t, want, secretValue(t, api, ctx, "model"))
		})
	}
}
