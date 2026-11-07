// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deviceDecoder served end to end: the service-plane question the device-uplink route asks about
// the decoder of a device's type.

const wireDeviceDecoder = `query ($d: String!) {
  deviceDecoder(deviceToken: $d) { present input maxReadings composedCost parsesJson profileVersionToken } }`

func deviceDecoderPlanOf(t *testing.T, res served) map[string]any {
	t.Helper()
	require.Empty(t, res.errs)
	plan, _ := res.data["deviceDecoder"].(map[string]any)
	require.NotNil(t, plan, "deviceDecoder is non-null")
	return plan
}

func TestDeviceDecoderAnswersTheDevicesTypeDecoder(t *testing.T) {
	f, _ := newIntegrationDecoderFixture(t)
	svc := service(f)
	ask := func(device string) map[string]any {
		return deviceDecoderPlanOf(t, serve(t, svc, wireDeviceDecoder, map[string]any{"d": device}))
	}

	// No published decoder: an answer of "none".
	none := ask("acme-pump")
	assert.Equal(t, false, none["present"])
	assert.Nil(t, none["input"])

	f.publishDecoder(t, wireFrameDecoder)
	got := ask("acme-pump")
	assert.Equal(t, true, got["present"])
	assert.Equal(t, "FRAME", got["input"])
	assert.EqualValues(t, 1, got["maxReadings"])
	assert.Greater(t, got["composedCost"], float64(0))
	assert.Equal(t, false, got["parsesJson"])
	assert.Equal(t, "meter@1", got["profileVersionToken"])

	// An unknown device is the same answer as a type with no decoder: no existence oracle here.
	ghost := ask("no-such-device")
	assert.Equal(t, false, ghost["present"])
	assert.Equal(t, "", ghost["profileVersionToken"])

	// A publish without it withdraws it.
	f.publishDecoder(t, "")
	assert.Equal(t, false, ask("acme-pump")["present"])
}

func TestDeviceDecoderIsServiceOnly(t *testing.T) {
	f, _ := newIntegrationDecoderFixture(t)
	f.publishDecoder(t, wireFrameDecoder)
	vars := map[string]any{"d": "acme-pump"}

	got := serve(t, service(f), wireDeviceDecoder, vars)
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
		res := serve(t, ctx, wireDeviceDecoder, vars)
		if len(res.errs) == 0 || res.data["deviceDecoder"] != nil {
			t.Errorf("deviceDecoder was admitted for %s: %v %v", who, res.data, res.errs)
		}
	}
}
