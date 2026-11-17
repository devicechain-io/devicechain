// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
)

var errBindingSecretWrite = errors.New("simulated secret write failure")

type bindingFailingSecretStore struct{ secrets.SecretStore }

func (s bindingFailingSecretStore) Put(context.Context, secrets.SecretRef, []byte) error {
	return errBindingSecretWrite
}
func (s bindingFailingSecretStore) Delete(context.Context, secrets.SecretRef) error {
	return errBindingSecretWrite
}

func TestProviderBindingMoveSecretFailureKeepsOldDestination(t *testing.T) {
	for _, optimistic := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconditional", true: "precondition"}[optimistic], func(t *testing.T) {
			for _, mode := range []string{"null", "empty", "replace", "neutral"} {
				t.Run(mode, func(t *testing.T) {
					api := newTestApi(t)
					ctx := context.Background()
					row, err := api.CreateAIProvider(ctx, &AIProviderCreateRequest{Token: "sink", Kind: "anthropic", Model: "model", Endpoint: strp("https://old.example"), Secret: strp("old"), Enabled: true})
					require.NoError(t, err)
					api.Secrets = bindingFailingSecretStore{api.Secrets}
					req := &AIProviderUpdateRequest{Endpoint: dcgraphql.OptionalStringOf("https://other.example")}
					switch mode {
					case "null":
						req.Secret = dcgraphql.OptionalString{Set: true}
					case "empty":
						req.Secret = dcgraphql.OptionalStringOf("")
					case "replace":
						req.Secret = dcgraphql.OptionalStringOf("new")
					case "neutral":
						req = &AIProviderUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")}
					}
					var expected *string
					if optimistic {
						expected = dcgraphql.FormatTime(row.UpdatedAt)
					}
					_, updateErr := api.UpdateAIProvider(ctx, "sink", req, expected)
					if mode == "neutral" {
						require.NoError(t, updateErr)
					} else {
						require.ErrorIs(t, updateErr, errBindingSecretWrite)
					}
					rows, err := api.AIProvidersByToken(ctx, []string{"sink"})
					require.NoError(t, err)
					require.Len(t, rows, 1)
					after := rows[0]
					require.Equal(t, row.Endpoint, after.Endpoint)
					if mode == "neutral" {
						require.Equal(t, "renamed", after.Name.String)
					} else {
						require.True(t, row.UpdatedAt.Equal(after.UpdatedAt))
					}
					require.Equal(t, "old", secretValue(t, api, ctx, "sink"))
				})
			}
		})
	}
}
