// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// An MQTT_BASIC secret must be at least credential.MinDeviceSecretBytes, on create and on
// rotation, counted in bytes as sent. The boundary is pinned on both sides.
func TestAnMqttPasswordBelowTheMinimumIsRefusedOnCreateAndRotation(t *testing.T) {
	api, ctx := resolveFixture(t) // device "dev"; credential c-1 holds a long secret
	exact := strings.Repeat("a", credential.MinDeviceSecretBytes)
	below := exact[:len(exact)-1]
	create := func(token, secret string) error {
		_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{Token: token, DeviceToken: "dev",
			CredentialType: string(CredentialMqttBasic), CredentialId: "id-" + token, CredentialValue: &secret, Enabled: true})
		return err
	}

	require.ErrorIs(t, create("short", below), ErrCredentialSecretTooShort)
	require.NoError(t, create("exact", exact), "a secret of exactly the minimum is accepted")

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf(below)})
	require.ErrorIs(t, err, ErrCredentialSecretTooShort)
	// Refused before anything is written: the old secret still authenticates.
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret-0123456789"), time.Now())
	require.NoError(t, err)
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf(exact)})
	require.NoError(t, err)

	// The refusal is INVALID_VALUE and names no secret.
	ext := ErrCredentialSecretTooShort.Extensions()
	require.Equal(t, "INVALID_VALUE", ext["code"])
	require.NotContains(t, ErrCredentialSecretTooShort.Error(), below)
}

// The floor is the MQTT password's. A credential type that compares no secret is not held
// to it, and an update that does not set a secret keeps whatever is stored, short or not:
// a secret stored before the floor existed works until it is rotated.
func TestTheMinimumDoesNotReachOtherTypesOrSecretsAlreadyStored(t *testing.T) {
	api, ctx := resolveFixture(t)
	short := "tok"
	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{Token: "t-1", DeviceToken: "dev",
		CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", CredentialValue: &short, Enabled: true})
	require.NoError(t, err)

	// A stored short secret, as one written before the floor would be.
	key := api.DeviceSecretKey
	digest, err := key.Digest("acme", "oldpw")
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Exec("UPDATE device_credentials SET secret_digest = ? WHERE token = ?", digest, "c-1").Error)

	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err, "an edit that does not rotate must not be refused over the stored secret")
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "oldpw"), time.Now())
	require.NoError(t, err, "a stored short secret authenticates until it is rotated")
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("oldpw")})
	require.True(t, errors.Is(err, ErrCredentialSecretTooShort), "re-setting the short secret is a rotation and is refused")
}
