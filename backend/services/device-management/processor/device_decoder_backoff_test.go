// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

type decoderAuthAPI struct {
	fakeAuthApi
	checker *credential.Checker
}

func (a decoderAuthAPI) GetDeviceCredentialChecker() *credential.Checker { return a.checker }
func (a decoderAuthAPI) DevicesByToken(context.Context, []string) ([]*model.Device, error) {
	return []*model.Device{{TokenReference: rdb.TokenReference{Token: "dev"}, DeviceTypeId: 42}}, nil
}
func (a decoderAuthAPI) ProfileResolutionByDeviceType(_ context.Context, id uint) (model.ProfileResolution, error) {
	if id != 42 {
		panic("decoder used the connect lookup's incomplete device")
	}
	return model.ProfileResolution{}, nil
}

func TestDeviceDecoderAndMqttPasswordConnectShareBackoff(t *testing.T) {
	now := time.Now()
	store := credentialtest.NewStore()
	checker := testChecker(t, store, credential.WithClock(func() time.Time { return now }))
	api := decoderAuthAPI{checker: checker, fakeAuthApi: fakeAuthApi{
		secret: "right", authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) {
			return &model.Device{TokenReference: rdb.TokenReference{Token: "dev"}}, nil
		},
	}}
	callout := &CalloutResponder{api: api, creds: checker, now: func() time.Time { return now }}
	ctx := core.WithTenant(context.Background(), "acme")
	presented := func(secret string) *model.PresentedCredential {
		return &model.PresentedCredential{CredentialType: "MQTT_BASIC", CredentialId: "username", Secret: &secret}
	}
	for i := 0; i < DeviceCredentialPolicy.Free; i++ {
		if i%2 == 0 {
			answer, err := model.DeviceDecoder(ctx, api, "dev", presented("wrong"))
			require.NoError(t, err)
			require.Nil(t, answer)
		} else {
			_, err := callout.checkPassword(ctx, "acme", presented("wrong"))
			require.ErrorIs(t, err, model.ErrCredentialSecretMismatch)
		}
	}
	_, err := model.DeviceDecoder(ctx, api, "dev", presented("right"))
	var held *credential.ThrottledError
	require.ErrorAs(t, err, &held)
	_, err = callout.checkPassword(ctx, "acme", presented("right"))
	require.ErrorAs(t, err, &held)
	now = now.Add(DeviceCredentialPolicy.Base)
	answer, err := model.DeviceDecoder(ctx, api, "dev", presented("right"))
	require.NoError(t, err)
	require.NotNil(t, answer)
	device, err := callout.checkPassword(ctx, "acme", presented("right"))
	require.NoError(t, err)
	require.Equal(t, "dev", device.Token)
}

func TestDeviceDecoderChargesRefusalsLikeWrongPasswords(t *testing.T) {
	for _, refusal := range []error{model.ErrCredentialNotResolved, model.ErrCredentialMisconfigured} {
		t.Run(refusal.Error(), func(t *testing.T) {
			checker := testChecker(t, credentialtest.NewStore())
			api := decoderAuthAPI{checker: checker, fakeAuthApi: fakeAuthApi{
				authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) { return nil, refusal },
			}}
			secret := "wrong"
			p := &model.PresentedCredential{CredentialType: "MQTT_BASIC", CredentialId: "u", Secret: &secret}
			ctx := core.WithTenant(context.Background(), "acme")
			for i := 0; i < DeviceCredentialPolicy.Free; i++ {
				answer, err := model.DeviceDecoder(ctx, api, "dev", p)
				require.NoError(t, err)
				require.Nil(t, answer)
			}
			_, err := model.DeviceDecoder(ctx, api, "dev", p)
			var held *credential.ThrottledError
			require.True(t, errors.As(err, &held))
		})
	}
}
