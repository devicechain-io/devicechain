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

// 🔴 A TYPE CHANGE CANNOT CARRY A SECRET PAST THE MINIMUM. An ACCESS_TOKEN stores no secret,
// so a value sent with one is dropped; a change TO MQTT_BASIC must set the password in the same
// request, and that password meets the minimum like any other.
func TestChangingATypeToMqttBasicNeedsAPasswordThatMeetsTheMinimum(t *testing.T) {
	api, ctx := resolveFixture(t)
	short := "tok"
	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{Token: "t-1", DeviceToken: "dev",
		CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", CredentialValue: &short, Enabled: true})
	require.NoError(t, err)
	toBasic := func(value dcgraphql.OptionalString) error {
		// The id is set too: an access token's stored id is a digest, not a username.
		_, err := api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
			CredentialType: dcgraphql.OptionalStringOf(string(CredentialMqttBasic)),
			CredentialId:   dcgraphql.OptionalStringOf("tok-1"), CredentialValue: value})
		return err
	}

	// The reviewer's scenario: no value. Without the rule, "tok" would now be a 3-byte password.
	require.ErrorIs(t, toBasic(dcgraphql.OptionalString{}), ErrCredentialSecretRequiredForType)
	require.Equal(t, "INVALID_VALUE", ErrCredentialSecretRequiredForType.Extensions()["code"])
	require.ErrorIs(t, toBasic(dcgraphql.OptionalStringOf("")), ErrCredentialSecretRequiredForType)
	require.ErrorIs(t, toBasic(dcgraphql.ClearedString()), ErrCredentialSecretRequiredForType)
	require.ErrorIs(t, toBasic(dcgraphql.OptionalStringOf(short)), ErrCredentialSecretTooShort)
	_, err = api.AuthenticateDevice(ctx, basic("tok-1", short), time.Now())
	require.Error(t, err, "a refused change leaves the credential as it was, and 'tok' never authenticates as a password")

	require.NoError(t, toBasic(dcgraphql.OptionalStringOf("a-long-enough-password")))
	_, err = api.AuthenticateDevice(ctx, basic("tok-1", "a-long-enough-password"), time.Now())
	require.NoError(t, err)

	// A value sent with a type that compares no secret is not kept.
	_, err = api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		CredentialType: dcgraphql.OptionalStringOf(string(CredentialAccessToken)),
		CredentialId:   dcgraphql.OptionalStringOf("tok-1")})
	require.NoError(t, err)
	var digest *string
	require.NoError(t, api.RDB.Database.Raw("SELECT secret_digest FROM device_credentials WHERE token = ?", "t-1").Scan(&digest).Error)
	require.Nil(t, digest, "a type that compares no secret stores none")
	require.ErrorIs(t, toBasic(dcgraphql.OptionalString{}), ErrCredentialSecretRequiredForType,
		"nothing carried across the type change")
}

// A creation of a type that compares no secret drops a value sent with it.
func TestACreateOfATypeWithNoSecretStoresNone(t *testing.T) {
	api, ctx := resolveFixture(t)
	v := "ignored-value-0123456789"
	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{Token: "t-2", DeviceToken: "dev",
		CredentialType: string(CredentialAccessToken), CredentialId: "tok-2", CredentialValue: &v, Enabled: true})
	require.NoError(t, err)
	var digest *string
	require.NoError(t, api.RDB.Database.Raw("SELECT secret_digest FROM device_credentials WHERE token = ?", "t-2").Scan(&digest).Error)
	require.Nil(t, digest)
}

// A short plaintext left by an old pod during a rolling upgrade is ADOPTED by an edit that
// sets no password, and the minimum does not apply to it: it is carried over, not chosen.
func TestAdoptingAShortLegacyPlaintextSecretIsNotRefusedAndAuthenticates(t *testing.T) {
	api, ctx := resolveFixture(t)
	require.NoError(t, api.RDB.DB(ctx).Exec("UPDATE device_credentials SET credential_value = ? WHERE token = ?", "legacy", "c-1").Error)

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "legacy"), time.Now())
	require.NoError(t, err, "the adopted short secret authenticates")
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret-0123456789"), time.Now())
	require.Error(t, err, "the older digest was replaced by the later plaintext")
}
