// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// pullFixture seeds a tenant with a default-channel type "plain" and an HTTP_PULL type "pulled",
// a device on each, and an access-token credential for the pulled one.
func pullFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &DeviceCredential{})...)
	ctx := partialUpdateCtx()
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "plain"})
	require.NoError(t, err)
	_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "pulled", CommandChannel: strp("HTTP_PULL")})
	require.NoError(t, err)
	_, err = api.CreateDevice(ctx, &DeviceCreateRequest{Token: "plain-dev", DeviceTypeToken: "plain"})
	require.NoError(t, err)
	_, err = api.CreateDevice(ctx, &DeviceCreateRequest{Token: "pull-dev", DeviceTypeToken: "pulled"})
	require.NoError(t, err)
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "cred-pull", DeviceToken: "pull-dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "pull-access-token-0001", Enabled: true,
	})
	require.NoError(t, err)
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "cred-plain", DeviceToken: "plain-dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "plain-access-token-0002", Enabled: true,
	})
	require.NoError(t, err)
	return api, ctx
}

func TestCommandChannelIsStoredAndDefaultsToNull(t *testing.T) {
	api, ctx := pullFixture(t)
	plain, err := api.DeviceTypesByToken(ctx, []string{"plain"})
	require.NoError(t, err)
	require.False(t, requireOne(t, "plain type", plain, err).CommandChannel.Valid,
		"a type that did not opt in must read NULL, which means today's behaviour")
	pulled, err := api.DeviceTypesByToken(ctx, []string{"pulled"})
	require.Equal(t, "HTTP_PULL", requireOne(t, "pulled type", pulled, err).CommandChannel.String)
}

func TestCommandChannelRefusesAnUnknownValueOnCreateAndUpdate(t *testing.T) {
	api, ctx := pullFixture(t)
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "bad", CommandChannel: strp("SMOKE_SIGNALS")})
	require.ErrorIs(t, err, ErrInvalidCommandChannel)
	_, err = api.UpdateDeviceType(ctx, "plain", &DeviceTypeUpdateRequest{CommandChannel: dcgraphql.OptionalStringOf("http_pull")})
	require.ErrorIs(t, err, ErrInvalidCommandChannel, "the value is case sensitive: an unrecognised channel must not read as the default")

	// Nothing was written by either refusal.
	got, err := api.DeviceTypesByToken(ctx, []string{"plain"})
	require.False(t, requireOne(t, "plain type", got, err).CommandChannel.Valid)
	missing, err := api.DeviceTypesByToken(ctx, []string{"bad"})
	require.NoError(t, err)
	require.Empty(t, missing)
}

func TestCommandChannelBlankMeansTheDefault(t *testing.T) {
	api, ctx := pullFixture(t)
	created, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "blank", CommandChannel: strp("  ")})
	require.NoError(t, err)
	require.False(t, created.CommandChannel.Valid)
}

// TestCommandChannelFollowsThePartialUpdateRules: omitted keeps, a value sets, null and blank clear.
func TestCommandChannelFollowsThePartialUpdateRules(t *testing.T) {
	api, ctx := pullFixture(t)
	read := func() string {
		got, err := api.DeviceTypesByToken(ctx, []string{"pulled"})
		return requireOne(t, "pulled type", got, err).CommandChannel.String
	}
	_, err := api.UpdateDeviceType(ctx, "pulled", &DeviceTypeUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	require.Equal(t, "HTTP_PULL", read(), "a rename must not touch the channel")

	_, err = api.UpdateDeviceType(ctx, "pulled", &DeviceTypeUpdateRequest{CommandChannel: dcgraphql.OptionalStringOf("")})
	require.NoError(t, err)
	require.Equal(t, "", read(), "a blank value returns the type to the default")

	_, err = api.UpdateDeviceType(ctx, "pulled", &DeviceTypeUpdateRequest{CommandChannel: dcgraphql.OptionalStringOf("HTTP_PULL")})
	require.NoError(t, err)
	require.Equal(t, "HTTP_PULL", read())

	_, err = api.UpdateDeviceType(ctx, "pulled", &DeviceTypeUpdateRequest{CommandChannel: dcgraphql.OptionalString{Set: true}})
	require.NoError(t, err)
	require.Equal(t, "", read(), "an explicit null clears")
}

func TestPullDevicesNamesOnlyEnabledDevicesOfPullTypes(t *testing.T) {
	api, ctx := pullFixture(t)
	got, err := api.PullDevices(ctx, []string{"plain-dev", "pull-dev", "ghost"})
	require.NoError(t, err)
	require.Equal(t, []string{"pull-dev"}, got)

	// A disabled device is absent: its commands are gated elsewhere, and a pull must not serve it.
	_, err = api.UpdateDevice(ctx, "pull-dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	got, err = api.PullDevices(ctx, []string{"pull-dev"})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestPullDevicesRefusesAnOversizedQuestion(t *testing.T) {
	api, ctx := pullFixture(t)
	tokens := make([]string, maxPullDeviceTokens+1)
	for i := range tokens {
		tokens[i] = "d" + time.Duration(i).String()
	}
	_, err := api.PullDevices(ctx, tokens)
	require.ErrorIs(t, err, ErrTooManyPullDevices)
}

func TestAuthenticateCommandChannelProvesTheTokenAndNamesTheChannel(t *testing.T) {
	api, ctx := pullFixture(t)
	now := time.Now()

	access, err := api.AuthenticateCommandChannel(ctx, "pull-access-token-0001", now)
	require.NoError(t, err)
	require.NotNil(t, access)
	require.Equal(t, "pull-dev", access.DeviceToken)
	require.Equal(t, "pulled", access.DeviceTypeToken)
	require.Equal(t, "HTTP_PULL", access.CommandChannel)

	// A device whose type has not opted in still authenticates; the caller refuses it by channel.
	plain, err := api.AuthenticateCommandChannel(ctx, "plain-access-token-0002", now)
	require.NoError(t, err)
	require.NotNil(t, plain)
	require.Equal(t, "plain-dev", plain.DeviceToken)
	require.Empty(t, plain.CommandChannel)
}

func TestAuthenticateCommandChannelAnswersNilForEveryWayToFail(t *testing.T) {
	api, ctx := pullFixture(t)
	now := time.Now()

	for name, token := range map[string]string{"unknown": "no-such-token", "empty": "", "the digest-shaped guess": "dctk1$00000000$AAAA"} {
		access, err := api.AuthenticateCommandChannel(ctx, token, now)
		require.NoError(t, err, name)
		require.Nil(t, access, name)
	}

	// Revoked (credential disabled): the very next request is refused.
	_, err := api.UpdateDeviceCredential(ctx, "cred-pull", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	access, err := api.AuthenticateCommandChannel(ctx, "pull-access-token-0001", now)
	require.NoError(t, err)
	require.Nil(t, access, "a revoked token must stop receiving commands on the next request")
}

func TestAuthenticateCommandChannelRefusesADisabledDevice(t *testing.T) {
	api, ctx := pullFixture(t)
	_, err := api.UpdateDevice(ctx, "pull-dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	access, err := api.AuthenticateCommandChannel(ctx, "pull-access-token-0001", time.Now())
	require.NoError(t, err)
	require.Nil(t, access, "a disabled device is cut off from the platform, commands included")
}

func TestAuthenticateCommandChannelIsTenantScoped(t *testing.T) {
	api, _ := pullFixture(t)
	other := core.WithTenant(context.Background(), "other")
	access, err := api.AuthenticateCommandChannel(other, "pull-access-token-0001", time.Now())
	require.NoError(t, err)
	require.Nil(t, access, "a token must not authenticate in another tenant")
}
