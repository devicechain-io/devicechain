// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// X.509 device credentials are not accepted until certificate verification ships:
// creating one is refused with the typed UNSUPPORTED answer, and nothing is written.
func TestCreateDeviceCredentialRefusesX509(t *testing.T) {
	f := newSQLiteCredentialFixture(t)

	_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-x", DeviceToken: "dev", CredentialType: string(CredentialX509Certificate),
		CredentialId: "AA:BB", Enabled: true,
	})
	var unsupported *UnsupportedCredentialTypeError
	require.True(t, errors.As(err, &unsupported), "got %v, want UnsupportedCredentialTypeError", err)
	require.Equal(t, "UNSUPPORTED", unsupported.Extensions()["code"])
	require.Same(t, unsupported, UnsupportedCredentialTypeOf(err))

	_, err = f.api.DeviceCredentialByCredentialId(f.ctx, string(CredentialX509Certificate), "AA:BB")
	require.Error(t, err, "a refused create still stored the credential")
}

// An update cannot move an existing credential onto the retired type either.
func TestUpdateDeviceCredentialRefusesX509(t *testing.T) {
	f := newSQLiteCredentialFixture(t)

	x509 := string(CredentialX509Certificate)
	var req DeviceCredentialUpdateRequest
	req.CredentialType.Set, req.CredentialType.Value = true, &x509
	_, err := f.api.UpdateDeviceCredential(f.ctx, "c-2", &req)
	var unsupported *UnsupportedCredentialTypeError
	require.True(t, errors.As(err, &unsupported), "got %v, want UnsupportedCredentialTypeError", err)

	got, err := f.api.DeviceCredentialByCredentialId(f.ctx, string(CredentialAccessToken), "tok-1")
	require.NoError(t, err)
	require.Equal(t, string(CredentialAccessToken), got.CredentialType, "a refused update changed the stored type")
}

// A row of the retired type that is already stored (created before the type was
// retired) authenticates nothing: the per-event path refuses it, and so does the cached
// decorator. The row is written straight to the table, past the create guard.
func TestAuthenticateRefusesAStoredX509Credential(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	require.NoError(t, f.api.RDB.Database.WithContext(f.ctx).Create(&DeviceCredential{
		TokenReference: rdb.TokenReference{Token: "c-x"}, DeviceId: f.devId,
		CredentialType: string(CredentialX509Certificate), CredentialId: "AA:BB", Enabled: true,
	}).Error)

	presented := &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: "AA:BB"}
	d, err := f.api.AuthenticateDevice(f.ctx, presented, time.Unix(1_800_000_000, 0))
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, d, "a stored X.509 credential authenticated a device")
	require.True(t, IsCredentialRefusal(err))

	d, err = f.capi.AuthenticateDevice(f.ctx, presented, time.Unix(1_800_000_000, 0))
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, d)
}
