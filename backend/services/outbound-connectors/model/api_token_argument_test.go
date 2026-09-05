// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHICH CONNECTOR AN UPDATE WRITES, AND WHERE THE RENAME WENT.
//
// updateConnector used to take the RENAME rule rather than the reconcile: a differing
// payload token was the connector's new name, which the credential's id-keying makes
// safe (TestSecretSurvivesTokenRename). That is why this family could not be converted
// mechanically — dropping the payload token would have deleted a capability.
//
// The capability moved to renameConnector, where `newToken` can mean only one thing,
// and the update input then lost its token entirely. So the disagreement the old rule
// refused — a `token` argument naming one connector and a payload token naming another
// — is now UNREPRESENTABLE rather than merely refused, and the tests that drove it are
// re-pointed here at the rename's own rules.

// 🔴 A BLANK NEW TOKEN IS REFUSED, WHITESPACE INCLUDED. `token: String!` admits "", and
// it used to be written straight onto the row — leaving a connector REACT still
// dispatches to addressable by nothing, with the mutation returning success.
func TestRenameConnector_ABlankNewTokenIsRefused(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
			})
			require.NoError(t, err)

			_, err = api.RenameConnector(ctx, "conn-a", blank)
			require.Error(t, err, "a blank new token %q was accepted", blank)

			found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
			require.NoError(t, ferr)
			require.Len(t, found, 1, "the connector is no longer findable by its own token")
			assert.Equal(t, "conn-a", found[0].Token)
		})
	}
}

// THE COUNTERWEIGHT: the refusal above has not been bought by removing the rename.
func TestRenameConnector_RenamesAConnector(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	renamed, err := api.RenameConnector(ctx, "conn-a", "conn-b")
	require.NoError(t, err, "a rename was refused")
	assert.Equal(t, "conn-b", renamed.Token)

	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a", "conn-b"})
	require.NoError(t, ferr)
	require.Len(t, found, 1, "the connector answers to both tokens, so the rename copied rather than moved")
	assert.Equal(t, "conn-b", found[0].Token)
}

// Renaming to the token the connector already has is an idempotent SUCCESS, so the
// retry of a rename that half-failed is safe — and it must not fall into the collision
// check below and refuse the connector its own name.
func TestRenameConnector_SameTokenIsANoOpSuccess(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig, Secret: strp("keep"),
	})
	require.NoError(t, err)

	same, err := api.RenameConnector(ctx, "conn-a", "conn-a")
	require.NoError(t, err, "renaming a connector to its own token must succeed")
	assert.Equal(t, "conn-a", same.Token)
	assert.Equal(t, "keep", secretValue(t, api, ctx, "conn-a"),
		"the no-op rename disturbed the credential")
}

// A token another connector in the tenant already holds is refused BY NAME rather than
// left to arrive as a unique-index violation the caller has to decode. The full-replace
// update had no such check at all: it wrote the token and let the index answer.
func TestRenameConnector_RefusesATokenAlreadyInUse(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, token := range []string{"conn-a", "taken"} {
		_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
			Token: token, Type: string(ConnectorTypeMQTT), Config: mqttConfig,
		})
		require.NoError(t, err)
	}

	_, err := api.RenameConnector(ctx, "conn-a", "taken")
	require.Error(t, err, "renaming onto an existing connector's token must be refused")
	assert.Contains(t, err.Error(), "already in use",
		"the refusal must name the collision rather than surface as a constraint violation")

	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a", "taken"})
	require.NoError(t, ferr)
	assert.Len(t, found, 2, "the refused rename disturbed one of the two connectors")
}

// 🔴 THE TOKEN GRAMMAR STILL GOVERNS THE NEW TOKEN, and that is worth its own test
// because of HOW the rename writes. It issues a one-column update rather than saving the
// loaded row — a map destination rather than a struct — and the grammar callback handles
// those through a different branch. A rename that bypassed it would let a token nothing
// can address onto a live connector, which is the same defect the blank check above
// refuses, arriving through the write instead of through the argument.
//
// The blank cases are NOT reused here: those are refused before the database is touched,
// so they say nothing about whether the write path is still guarded.
func TestRenameConnector_TheTokenGrammarStillApplies(t *testing.T) {
	for _, bad := range []string{"not a token", "-leading-hyphen", "trailing space "} {
		t.Run(bad, func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
			})
			require.NoError(t, err)

			_, err = api.RenameConnector(ctx, "conn-a", bad)
			require.Error(t, err, "the grammar-violating token %q reached the row", bad)
			// 🔴 The error has to be the GRAMMAR's, not merely some error. Every other
			// way this write could fail — a collision, a missing row — would satisfy a
			// bare require.Error while the grammar had been bypassed entirely, which is
			// the only thing this test is about.
			assert.Contains(t, err.Error(), "is invalid",
				"the rename failed for a reason other than the token grammar, so this test "+
					"says nothing about whether the grammar still guards the write")

			found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
			require.NoError(t, ferr)
			require.Len(t, found, 1, "the connector is no longer findable by its own token")
		})
	}
}

// 🔴 NAMING ONE SIDE OF THE type/config PAIR RE-VALIDATES THE STORED OTHER SIDE.
//
// This is the input class every other update test in this file misses, and the miss is
// structural rather than accidental: `connectorEdit` always sends BOTH, and the harness
// declares them as PARTNERS so it moves them together too. So nothing drives the request
// the guarantee is actually about — one side sent, with the stored other side invalid for
// it — and a fold that validated `config` only when the request CARRIED one would pass the
// whole suite.
//
// What that mutant would let through is not a validation nicety. An mqtt connector
// re-pointed at kafka with its mqtt config left in place is STORED, reported as a success,
// and then dead-letters at send time — which is the failure mode the write-time check
// exists to convert into a refusal the operator sees on the screen that caused it.
func TestUpdateConnector_ChangingOnlyTheTypeRevalidatesTheStoredConfig(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	// `type` alone. The stored config is an MQTT one, which kafka cannot take.
	_, err = api.UpdateConnector(ctx, "conn-a", &ConnectorUpdateRequest{
		Type: dcgraphql.OptionalStringOf(string(ConnectorTypeKafka)),
	}, nil)
	require.Error(t, err, "a re-point onto a kind the STORED config cannot satisfy was accepted, "+
		"so the connector is now stored unusable and fails at send time instead")

	// Nothing was written: the refusal is total, so a caller who retries has not
	// half-applied the first attempt.
	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.Equal(t, string(ConnectorTypeMQTT), found[0].Type, "the refused update still moved the type")
	assert.JSONEq(t, mqttConfig, string(found[0].Config))

	// THE COUNTERWEIGHT, and it is what stops the refusal above being bought by refusing
	// every type change: the same re-point WITH a config the new kind accepts succeeds.
	_, err = api.UpdateConnector(ctx, "conn-a",
		connectorEdit(ConnectorTypeKafka, `{"addresses":["k:9092"],"topic":"t"}`), nil)
	require.NoError(t, err, "a re-point that names a valid config for the new kind was refused")
}

// The mirror: naming only `config` validates it against the STORED type. A fold that
// checked the pair only when the request carried the TYPE would pass the test above and
// fail here.
func TestUpdateConnector_ChangingOnlyTheConfigRevalidatesAgainstTheStoredType(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	// A well-formed KAFKA config sent to a connector still stored as mqtt.
	_, err = api.UpdateConnector(ctx, "conn-a", &ConnectorUpdateRequest{
		Config: dcgraphql.OptionalStringOf(`{"addresses":["k:9092"],"topic":"t"}`),
	}, nil)
	require.Error(t, err, "a config the STORED kind cannot take was accepted")

	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.JSONEq(t, mqttConfig, string(found[0].Config), "the refused update still wrote the config")

	// The counterweight: a different, VALID mqtt config on its own is accepted.
	_, err = api.UpdateConnector(ctx, "conn-a", &ConnectorUpdateRequest{
		Config: dcgraphql.OptionalStringOf(`{"urls":["tcp://other:1883"],"topic":"alerts"}`),
	}, nil)
	require.NoError(t, err, "a valid config for the stored kind was refused")
}

// An unknown token is a not-found, not a silent create and not "the only connector
// there is".
func TestRenameConnector_UnknownTokenIsNotFound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	})
	require.NoError(t, err)

	_, err = api.RenameConnector(ctx, "no-such-connector", "conn-b")
	require.Error(t, err, "renaming an unknown connector succeeded")

	found, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.Equal(t, "conn-a", found[0].Token,
		"a rename addressed to an unknown token moved the seeded connector")
}
