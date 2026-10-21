// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// recordingKicker is a SessionKicker that records every request and answers with err.
type recordingKicker struct {
	calls []kickCall
	err   error
}

type kickCall struct {
	tenant, reason string
	tokens         []string
}

func (k *recordingKicker) KickDeviceSessions(_ context.Context, tenant, reason string, tokens []string) error {
	k.calls = append(k.calls, kickCall{tenant, reason, append([]string(nil), tokens...)})
	return k.err
}

func kickFixture(t *testing.T) (*Api, context.Context, *recordingKicker) {
	t.Helper()
	api, ctx := resolveFixture(t)
	k := &recordingKicker{}
	api.SessionKicker = k
	return api, ctx, k
}

// REVOCATION KICKS. Disabling a credential ends the device's live sessions; the kick names the
// tenant and the device token the broker knows the session by.
//
// Negative control: with endDeviceSessions removed from UpdateDeviceCredential this has no call.
func TestDisablingACredentialKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickCredentialChanged, []string{"dev"}}}, k.calls)
}

func TestRotatingACredentialsSecretKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("r0tated-secret-0123")})
	require.NoError(t, err)
	require.Len(t, k.calls, 1)
}

func TestAnEditThatChangesNoAuthorityKicksNothing(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(`{"note":"renamed"}`)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "a metadata edit must not drop a healthy device's connection")
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "enabling is not a revocation")
}

func TestDeletingACredentialKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	deleted, err := api.DeleteDeviceCredential(ctx, "c-1")
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickCredentialDeleted, []string{"dev"}}}, k.calls)

	k.calls = nil
	deleted, err = api.DeleteDeviceCredential(ctx, "c-1")
	require.NoError(t, err)
	require.False(t, deleted)
	require.Empty(t, k.calls, "deleting what is not there ends no session")
}

func TestDisablingADeviceKicksItAndEditingOrEnablingItDoesNot(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	require.Empty(t, k.calls)

	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickDeviceDisabled, []string{"dev"}}}, k.calls)

	k.calls = nil
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "re-enabling a device ends nothing")
}

func TestDeletingADeviceKicksItsSessions(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	k := &recordingKicker{}
	api.SessionKicker = k
	dev := &Device{}
	dev.Token = "dev-1"
	require.NoError(t, api.RDB.DB(ctx).Create(dev).Error)

	deleted, err := api.DeleteDevice(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, []kickCall{{"acme", messaging.SessionKickDeviceDeleted, []string{"dev-1"}}}, k.calls)

	k.err = errors.New("no responder")
	require.NoError(t, api.RDB.DB(ctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-2"}}).Error)
	_, err = api.DeleteDevice(ctx, "dev-2")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
}

// FAIL CLOSED. A kick that cannot be confirmed is an ERROR from the operation that withdrew
// the authority, never a quiet success; the change itself is saved (the next CONNECT is
// refused), and repeating the operation asks again.
func TestAKickThatCannotBeConfirmedIsReportedAndRetriedByRepeating(t *testing.T) {
	api, ctx, k := kickFixture(t)
	k.err = errors.New("the broker cluster could not be consulted in full")

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Contains(t, err.Error(), "broker cluster could not be consulted", "the cause is surfaced")
	// Saved: the credential no longer authenticates a new connect.
	_, authErr := api.AuthenticateDeviceConnect(ctx, basic("cred-1", "s3cret-0123456789"), time.Now())
	require.ErrorIs(t, authErr, ErrCredentialNotResolved)

	// The retry: the same disable asks the broker again, and succeeds once it can.
	k.err = nil
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Len(t, k.calls, 2)

	// Same for a device disable and a credential delete.
	k.err = errors.New("no responder")
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	_, err = api.DeleteDeviceCredential(ctx, "c-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
}

func TestNoKickerWiredIsAnInstanceWithoutAnMqttCallout(t *testing.T) {
	api, ctx := resolveFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
}

// THE JWT CLAMP'S INPUT. The two connect lookups hand the callout the expiry of the credential
// they authenticated; the per-event lookup does not (it has no use for it and copies nothing).
func TestTheConnectLookupsCarryTheCredentialsExpiry(t *testing.T) {
	api, ctx := resolveFixture(t)
	exp := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		ExpiresAt: dcgraphql.OptionalStringOf(exp.Format(time.RFC3339))})
	require.NoError(t, err)

	d, _, err := api.ResolveDeviceCredential(ctx, basic("cred-1", "x"), time.Now())
	require.NoError(t, err)
	require.True(t, d.ConnectCredentialExpiresAt.Valid)
	require.True(t, exp.Equal(d.ConnectCredentialExpiresAt.Time), "got %s want %s", d.ConnectCredentialExpiresAt.Time, exp)

	// An access token takes the other connect path.
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(CredentialAccessToken), CredentialId: "tok-2", Enabled: true,
		ExpiresAt: ptr(exp.Format(time.RFC3339))})
	require.NoError(t, err)
	d, err = api.AuthenticateDeviceConnect(ctx, accessToken("tok-2"), time.Now())
	require.NoError(t, err)
	require.True(t, exp.Equal(d.ConnectCredentialExpiresAt.Time))

	d, err = api.AuthenticateDevice(ctx, accessToken("tok-2"), time.Now())
	require.NoError(t, err)
	require.False(t, d.ConnectCredentialExpiresAt.Valid, "the per-event path does not carry it")
}

func ptr[T any](v T) *T { return &v }
