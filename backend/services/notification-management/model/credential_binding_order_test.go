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

func TestChannelBindingMoveSecretFailureKeepsOldDestination(t *testing.T) {
	for _, mode := range []string{"null", "empty", "replace", "neutral"} {
		t.Run(mode, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			before := `{"host":"mail.example","from":"a@example"}`
			createChannel(t, api, ctx, "sink", ChannelTypeSMTP, strPtr(before), strPtr("old"))
			row := channelRow(t, api, ctx, "sink")
			api.Secrets = bindingFailingSecretStore{api.Secrets}
			req := &NotificationChannelUpdateRequest{Config: dcgraphql.OptionalStringOf(`{"host":"other.example","from":"a@example"}`)}
			switch mode {
			case "null":
				req.Secret = dcgraphql.OptionalString{Set: true}
			case "empty":
				req.Secret = dcgraphql.OptionalStringOf("")
			case "replace":
				req.Secret = dcgraphql.OptionalStringOf("new")
			case "neutral":
				req = &NotificationChannelUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")}
			}
			_, updateErr := api.UpdateNotificationChannel(ctx, "sink", req)
			if mode == "neutral" {
				require.NoError(t, updateErr)
			} else {
				require.ErrorIs(t, updateErr, errBindingSecretWrite)
			}
			after := channelRow(t, api, ctx, "sink")
			require.Equal(t, before, string(*after.Config))
			if mode == "neutral" {
				require.Equal(t, "renamed", after.Name.String)
			} else {
				require.True(t, row.UpdatedAt.Equal(after.UpdatedAt))
			}
			require.Equal(t, "old", channelSecretValue(t, api, ctx, "sink"))
		})
	}
}
