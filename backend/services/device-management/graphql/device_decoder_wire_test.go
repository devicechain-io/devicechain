// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deviceDecoder served end to end: the service-plane question the device-uplink route asks about
// the decoder of a device's type.

const wireDeviceDecoder = `query ($d: String!, $t: String!, $i: String!, $s: String) {
  deviceDecoder(deviceToken: $d, credentialType: $t, credentialId: $i, credentialSecret: $s) {
    present input maxReadings composedCost parsesJson profileVersionToken } }`

// The credentials the fixture's devices hold.
const (
	pumpToken  = "pump-access-token-0123456789abcdef"
	pumpMqttID = "pump-user"
	pumpMqttPw = "pump-password-0123456789"
)

func (f scopeFixture) credentialFor(t *testing.T, device string) {
	t.Helper()
	ctx := f.operator()
	_, err := f.api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{
		Token: device + "-cred", DeviceToken: device, CredentialType: string(model.CredentialAccessToken),
		CredentialId: pumpToken + device, Enabled: true})
	require.NoError(t, err)
}

func (f scopeFixture) mqttCredentialFor(t *testing.T, device string) {
	t.Helper()
	pw := pumpMqttPw
	_, err := f.api.CreateDeviceCredential(f.operator(), &model.DeviceCredentialCreateRequest{
		Token: device + "-mqtt", DeviceToken: device, CredentialType: string(model.CredentialMqttBasic),
		CredentialId: pumpMqttID + device, CredentialValue: &pw, Enabled: true})
	require.NoError(t, err)
}

func askDecoder(t *testing.T, svc context.Context, device, ctype, id string, secret *string) served {
	t.Helper()
	vars := map[string]any{"d": device, "t": ctype, "i": id, "s": nil}
	if secret != nil {
		vars["s"] = *secret
	}
	return serve(t, svc, wireDeviceDecoder, vars)
}

func deviceDecoderPlanOf(t *testing.T, res served) map[string]any {
	t.Helper()
	require.Empty(t, res.errs)
	plan, _ := res.data["deviceDecoder"].(map[string]any)
	require.NotNil(t, plan, "deviceDecoder is non-null")
	return plan
}

func TestDeviceDecoderAnswersTheDevicesTypeDecoder(t *testing.T) {
	f, _ := newIntegrationDecoderFixture(t)
	f.credentialFor(t, "acme-pump")
	svc := service(f)
	ask := func() map[string]any {
		return deviceDecoderPlanOf(t, askDecoder(t, svc, "acme-pump", "ACCESS_TOKEN", pumpToken+"acme-pump", nil))
	}

	// No published decoder: an answer of "none".
	none := ask()
	assert.Equal(t, false, none["present"])
	assert.Nil(t, none["input"])

	f.publishDecoder(t, wireFrameDecoder)
	got := ask()
	assert.Equal(t, true, got["present"])
	assert.Equal(t, "FRAME", got["input"])
	assert.EqualValues(t, 1, got["maxReadings"])
	assert.Greater(t, got["composedCost"], float64(0))
	assert.Equal(t, false, got["parsesJson"])
	assert.Equal(t, "meter@1", got["profileVersionToken"])

	// A publish without it withdraws it.
	f.publishDecoder(t, "")
	assert.Equal(t, false, ask()["present"])
}

// 🔴 NOTHING IS SAID BEFORE THE CREDENTIAL IS PROVEN. Every way of failing to authenticate is the
// same null, whether or not the device exists and whether or not a decoder is attached, so an
// anonymous caller cannot tell a device with a decoder from one without, or from none.
func TestDeviceDecoderIsNullForEveryFailureToAuthenticate(t *testing.T) {
	f, _ := newIntegrationDecoderFixture(t)
	f.credentialFor(t, "acme-pump")
	f.credentialFor(t, "globex-pump")
	f.mqttCredentialFor(t, "acme-pump")
	svc := service(f)
	wrongPw := "not-the-password-at-all"
	goodPw := pumpMqttPw

	cases := map[string]func() served{
		"an unknown access token": func() served {
			return askDecoder(t, svc, "acme-pump", "ACCESS_TOKEN", "no-such-token-0123456789abcdef", nil)
		},
		"another device's valid token": func() served {
			return askDecoder(t, svc, "acme-pump", "ACCESS_TOKEN", pumpToken+"globex-pump", nil)
		},
		"a device that does not exist, with a valid token": func() served {
			return askDecoder(t, svc, "no-such-device", "ACCESS_TOKEN", pumpToken+"acme-pump", nil)
		},
		"an unknown credential type": func() served {
			return askDecoder(t, svc, "acme-pump", "BOGUS", pumpToken+"acme-pump", nil)
		},
		"a wrong password": func() served {
			return askDecoder(t, svc, "acme-pump", "MQTT_BASIC", pumpMqttID+"acme-pump", &wrongPw)
		},
		"no password for a type that needs one": func() served {
			return askDecoder(t, svc, "acme-pump", "MQTT_BASIC", pumpMqttID+"acme-pump", nil)
		},
	}
	// With a decoder attached and then without: the nulls must not differ.
	for _, decoder := range []string{wireFrameDecoder, ""} {
		f.publishDecoder(t, decoder)
		// The paired positive: the right password authenticates, so the nulls above are the credential's.
		ok := askDecoder(t, svc, "acme-pump", "MQTT_BASIC", pumpMqttID+"acme-pump", &goodPw)
		require.NotNil(t, deviceDecoderPlanOf(t, ok), "positive control")
		for name, ask := range cases {
			res := ask()
			assert.Empty(t, res.errs, name)
			assert.Nil(t, res.data["deviceDecoder"], "%s must be null (decoder %q)", name, decoder)
		}
	}
}

func TestDeviceDecoderIsServiceOnly(t *testing.T) {
	f, _ := newIntegrationDecoderFixture(t)
	f.credentialFor(t, "acme-pump")
	f.publishDecoder(t, wireFrameDecoder)
	args := func(ctx context.Context) served {
		return askDecoder(t, ctx, "acme-pump", "ACCESS_TOKEN", pumpToken+"acme-pump", nil)
	}

	got := args(service(f))
	require.Empty(t, got.errs, "the paired positive")
	require.NotNil(t, got.data["deviceDecoder"])

	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator": f.operator(),
		"a service token without device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService,
			Tenant: scopeTenant, Authorities: []string{string(auth.EventRead)}}),
	}
	for who, ctx := range refused {
		res := args(ctx)
		if len(res.errs) == 0 || res.data["deviceDecoder"] != nil {
			t.Errorf("deviceDecoder was admitted for %s: %v %v", who, res.data, res.errs)
		}
	}
}
