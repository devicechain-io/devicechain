// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// 🔴 THE CALLOUT'S REGISTRATION IS ASSEMBLED HERE, NOT IN THE PROCESSOR. The processor tests hand
// it a fake limiter and a fake bound; what makes it the service's own is that the tenant ceiling
// is the SAME limiter the self-registration endpoint charges, that the bound is the configured
// one, and that the per-source bucket exists only when the operator turned it on.
func TestCalloutRegistrationIsAssembledFromTheServicesOwnParts(t *testing.T) {
	prevMs, prevCfg, prevTenants := Microservice, Configuration, ProvisioningTenants
	t.Cleanup(func() { Microservice, Configuration, ProvisioningTenants = prevMs, prevCfg, prevTenants })
	Microservice = &core.Microservice{InstanceId: "jitwiring", FunctionalArea: "device-management"}
	infra := Microservice.InstanceConfiguration.Infrastructure
	cfg := config.NewDeviceManagementConfiguration()
	api := &model.Api{}

	t.Run("the defaults", func(t *testing.T) {
		ProvisioningUnresolvedAdmissions = func(core.CeilingSource) {}
		ProvisioningTenants = nil
		jc, err := calloutJitConfig(api, cfg, infra)
		require.NoError(t, err)
		require.Same(t, api, jc.Registrar)
		require.Equal(t, config.DefaultCalloutJitMaxInFlight, jc.MaxInFlight)
		require.Nil(t, jc.Sources, "the per-source bucket must be off unless the operator says the MQTT Service preserves client addresses")
		require.NotNil(t, ProvisioningTenants, "the shared limiter is built on first use")
		require.Same(t, ProvisioningTenants, jc.Tenants)
	})
	t.Run("the endpoint's limiter is the one used", func(t *testing.T) {
		shared := core.NewTenantRateLimiter(core.StaticCeiling(1, 1))
		ProvisioningTenants = shared
		jc, err := calloutJitConfig(api, cfg, infra)
		require.NoError(t, err)
		require.Same(t, shared, jc.Tenants, "registration by certificate charged a limiter of its own")
		// And it is one allowance: a charge here is felt there.
		require.True(t, shared.Allow("acme"))
		require.False(t, jc.Tenants.Allow("acme"), "the callout's ceiling is not the endpoint's")
	})
	t.Run("configured", func(t *testing.T) {
		c := config.NewDeviceManagementConfiguration()
		c.Callout.JitMaxInFlight = 3
		c.Callout.JitSourceTrusted = true
		jc, err := calloutJitConfig(api, c, infra)
		require.NoError(t, err)
		require.Equal(t, 3, jc.MaxInFlight)
		require.NotNil(t, jc.Sources, "jitSourceTrusted did not turn the per-source bucket on")
	})
}
