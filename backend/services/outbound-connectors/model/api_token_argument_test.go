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

// WHICH CONNECTOR AN UPDATE WRITES.
//
// updateConnector takes the RENAME rule, not the reconcile: a differing payload token
// is the connector's new name, which TestSecretSurvivesTokenRename pins and the
// credential's id-keying makes safe.
//
// What the rename rule refuses is a BLANK new token. `token: String!` admits "", and
// that used to be written straight onto the row — leaving a connector that REACT
// still dispatches to addressable by nothing, with the mutation returning success.

func TestUpdateConnector_ABlankPayloadTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
			})
			require.NoError(t, err)

			_, err = api.UpdateConnector(ctx, "conn-a", &ConnectorCreateRequest{
				Token: blank, Type: string(ConnectorTypeMQTT), Config: mqttConfig,
			}, nil)
			require.Error(t, err, "a blank payload token %q was accepted", blank)

			found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
			require.NoError(t, ferr)
			require.Len(t, found, 1, "the connector is no longer findable by its own token")
			assert.Equal(t, "conn-a", found[0].Token)
		})
	}
}

// The guarded-write path refuses it too. The two paths write through different code,
// so a check on one says nothing about the other — and it is the guarded path the
// console uses, since it is the one that tracks a version.
func TestUpdateConnector_TheGuardedPathAlsoRefusesABlankToken(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	created, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)
	stamp := created.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")

	_, err = api.UpdateConnector(ctx, "conn-a", &ConnectorCreateRequest{
		Token: "", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	}, &stamp)
	require.Error(t, err, "the guarded path accepted a blank payload token")

	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.Equal(t, "conn-a", found[0].Token)
}
