// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/auth"
)

const wireCheckDeviceCredential = `query ($d: String!, $t: String!, $i: String!, $s: String) {
  checkDeviceCredential(device: $d, credentialType: $t, credentialId: $i, secret: $s) }`

// checkDeviceCredential is the service plane: a user's access token is refused whatever it holds,
// because the verdict is a credential-guessing oracle in the wrong hands.
func TestCheckDeviceCredentialIsServiceOnly(t *testing.T) {
	f := newIntegrationWireFixture(t)
	vars := map[string]any{"d": "sensor-001", "t": "", "i": "", "s": nil}
	withMode := func(ctx context.Context, mode string) context.Context {
		return context.WithValue(ctx, ContextDeviceAuthModeKey, mode)
	}

	// The paired positive: the service token with device:read is answered.
	got := serve(t, withMode(service(f), config.AuthModeDisabled), wireCheckDeviceCredential, vars)
	if len(got.errs) > 0 || got.data["checkDeviceCredential"] != true {
		t.Fatalf("a service token with device:read: %v %v", got.data, got.errs)
	}

	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator": f.operator(),
		"a service token without device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService,
			Tenant: scopeTenant, Authorities: []string{string(auth.EventRead)}}),
	}
	for who, ctx := range refused {
		res := serve(t, withMode(ctx, config.AuthModeDisabled), wireCheckDeviceCredential, vars)
		if len(res.errs) == 0 || res.data["checkDeviceCredential"] == true {
			t.Errorf("checkDeviceCredential was admitted for %s: %v %v", who, res.data, res.errs)
		}
	}
}

// The policy the verdict applies is the configured one, and an unset policy is the hardened one.
func TestCheckDeviceCredentialAppliesTheConfiguredPolicy(t *testing.T) {
	f := newIntegrationWireFixture(t)
	vars := map[string]any{"d": "sensor-001", "t": "", "i": "", "s": nil}
	for mode, want := range map[string]bool{
		config.AuthModeDisabled: true,
		config.AuthModeOptional: true,
		config.AuthModeRequired: false,
		"":                      false,
	} {
		ctx := context.WithValue(service(f), ContextDeviceAuthModeKey, mode)
		res := serve(t, ctx, wireCheckDeviceCredential, vars)
		if len(res.errs) > 0 || res.data["checkDeviceCredential"] != want {
			t.Errorf("no credential under mode %q: want %v, got %v %v", mode, want, res.data, res.errs)
		}
	}
}

// The credential handed to the policy is what the event presented, secret included, and a
// credential missing its type or its id is no credential at all.
func TestPresentedBy(t *testing.T) {
	secret := "s3cret-value"
	got := presentedBy("MQTT_BASIC", "user-1", &secret)
	if got == nil || got.CredentialType != "MQTT_BASIC" || got.CredentialId != "user-1" || got.Secret == nil || *got.Secret != secret {
		t.Fatalf("the presented credential was altered on the way: %+v", got)
	}
	if presentedBy("", "user-1", &secret) != nil || presentedBy("MQTT_BASIC", "", &secret) != nil {
		t.Error("a credential with no type or no id must be treated as none")
	}
	if got := presentedBy("ACCESS_TOKEN", "tok", nil); got == nil || got.Secret != nil {
		t.Errorf("a credential with no secret keeps none: %+v", got)
	}
}
