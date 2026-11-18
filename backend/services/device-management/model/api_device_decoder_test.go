// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

type decoderLegacyAuth struct {
	DeviceManagementApi
	presented *PresentedCredential
}

func (a *decoderLegacyAuth) AuthenticateDevice(_ context.Context, p *PresentedCredential, _ time.Time) (*Device, error) {
	a.presented = p
	return &Device{TokenReference: rdb.TokenReference{Token: "dev"}, DeviceTypeId: 42}, nil
}

func (*decoderLegacyAuth) ProfileResolutionByDeviceType(_ context.Context, id uint) (ProfileResolution, error) {
	if id != 42 {
		panic("wrong device type")
	}
	return ProfileResolution{}, nil
}

func TestDeviceDecoderKeepsNonSecretAuthenticationPath(t *testing.T) {
	for _, ctype := range []CredentialType{CredentialAccessToken, CredentialX509Certificate} {
		t.Run(string(ctype), func(t *testing.T) {
			api := &decoderLegacyAuth{}
			p := &PresentedCredential{CredentialType: string(ctype), CredentialId: "credential"}
			answer, err := DeviceDecoder(context.Background(), api, "dev", p)
			require.NoError(t, err)
			require.NotNil(t, answer)
			require.Same(t, p, api.presented)
		})
	}
}

func TestDeviceDecoderMissingCheckerNeverUsesLegacyPasswordCompare(t *testing.T) {
	api := &decoderLegacyAuth{}
	secret := "correct"
	answer, err := DeviceDecoder(context.Background(), api, "dev", &PresentedCredential{
		CredentialType: string(CredentialMqttBasic), CredentialId: "credential", Secret: &secret,
	})
	require.Error(t, err)
	require.Nil(t, answer)
	require.Nil(t, api.presented)
}
