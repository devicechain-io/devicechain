// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
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

func TestConnectorBindingMoveSecretFailureKeepsOldDestination(t *testing.T) {
	for _, optimistic := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconditional", true: "precondition"}[optimistic], func(t *testing.T) {
			for _, mode := range []string{"null", "empty", "replace", "neutral"} {
				t.Run(mode, func(t *testing.T) {
					api := newTestApi(t)
					ctx := core.WithTenant(context.Background(), "acme")
					before := mqttConfig
					row, err := api.CreateConnector(ctx, &ConnectorCreateRequest{Token: "sink", Type: "mqtt", Config: before, Secret: strp("old")})
					require.NoError(t, err)
					api.Secrets = bindingFailingSecretStore{api.Secrets}
					req := &ConnectorUpdateRequest{Config: dcgraphql.OptionalStringOf(`{"urls":["tcp://other:1883"],"topic":"alerts"}`)}
					switch mode {
					case "null":
						req.Secret = dcgraphql.OptionalString{Set: true}
					case "empty":
						req.Secret = dcgraphql.OptionalStringOf("")
					case "replace":
						req.Secret = dcgraphql.OptionalStringOf("new")
					case "neutral":
						req = &ConnectorUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")}
					}
					var expected *string
					if optimistic {
						expected = dcgraphql.FormatTime(row.UpdatedAt)
					}
					_, updateErr := api.UpdateConnector(ctx, "sink", req, expected)
					if mode == "neutral" {
						require.NoError(t, updateErr)
					} else {
						require.ErrorIs(t, updateErr, errBindingSecretWrite)
					}
					rows, err := api.ConnectorsByToken(ctx, []string{"sink"})
					require.NoError(t, err)
					require.Len(t, rows, 1)
					after := rows[0]
					require.Equal(t, before, string(after.Config))
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
