// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/rs/zerolog/log"
)

// maxJitSourceKeys bounds the per-source bucket map, as the self-registration endpoint's is.
const maxJitSourceKeys = 100_000

// calloutJitConfig is what registration of devices by certificate on first connect runs with,
// assembled from the loaded configuration and the service's Api.
//
// 🔴 THE PER-TENANT CEILING IS THE SELF-REGISTRATION ENDPOINT'S OWN LIMITER (ProvisioningTenants),
// not a second one: a tenant's registrations by secret and by certificate draw on one allowance.
// The per-source bucket is OFF unless callout.jitSourceTrusted says the MQTT Service preserves
// client addresses; keyed on a translated address it would put every device in one bucket.
func calloutJitConfig(api *model.Api, cfg *config.DeviceManagementConfiguration, infra mscfg.InfrastructureConfiguration) (processor.JitConfig, error) {
	jc := processor.JitConfig{
		Registrar:   api,
		Tenants:     provisioningTenantLimiter(infra),
		MaxInFlight: cfg.Callout.JitMaxInFlight,
	}
	if cfg.Callout.JitSourceTrusted {
		sources, err := sourcelimit.New(sourcelimit.Limits{
			Burst: cfg.Provisioning.SourceBurst, Every: cfg.Provisioning.SourceEveryDuration(), MaxKeys: maxJitSourceKeys})
		if err != nil {
			return jc, err
		}
		jc.Sources = sources
	}
	return jc, nil
}

// wireCalloutCertificateRegistration turns registration on first connect on for the callout.
func wireCalloutCertificateRegistration(resp *processor.CalloutResponder, api *model.Api,
	cfg *config.DeviceManagementConfiguration, infra mscfg.InfrastructureConfiguration) error {
	jc, err := calloutJitConfig(api, cfg, infra)
	if err != nil {
		return err
	}
	if _, err := resp.WithCertificateRegistration(jc); err != nil {
		return err
	}
	log.Info().Int("jitMaxInFlight", jc.MaxInFlight).Bool("jitSourceTrusted", jc.Sources != nil).
		Msg("Device auth-callout registers devices by certificate on their first connect.")
	return nil
}
