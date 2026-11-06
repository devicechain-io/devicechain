// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/revocationupload"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// revocationLimitsOf is the store's bounds from the loaded fixed configuration.
func revocationLimitsOf(c config.RevocationConfiguration) model.RevocationLimits {
	return model.RevocationLimits{
		MaxCrlBytes:               c.MaxCrlBytes,
		MaxCrlEntries:             c.MaxCrlEntries,
		MaxListsPerAnchor:         c.MaxListsPerAnchor,
		MaxManualEntriesPerTenant: c.MaxManualEntriesPerTenant,
		DefaultStaleGrace:         c.DefaultStaleGraceDuration(),
		MaxGraphqlCrlBytes:        c.MaxGraphqlCrlBytes,
		MaxConcurrentWrites:       c.MaxConcurrentWrites,
	}
}

// wireRevocation installs the revocation store's fixed bounds on the Api and mounts the streaming
// CRL upload on the service's own mux. The bounds go in first: the handler sizes its slots from
// them.
func wireRevocation(api *model.Api, cfg *config.DeviceManagementConfiguration, ms *core.Microservice,
	infra mscfg.InfrastructureConfiguration) error {
	if err := api.SetRevocationLimits(revocationLimitsOf(cfg.Callout.Revocation)); err != nil {
		return err
	}
	h := revocationupload.New(api, ms.Readiness.Validator, ms.Readiness.AudiencePolicy, provisioningTenantGate(infra))
	revocationupload.Register(ms.Mux(), h)
	lim := api.RevocationLimits()
	log.Info().Str("path", revocationupload.Path).Int("maxCrlBytes", lim.MaxCrlBytes).Int("maxCrlEntries", lim.MaxCrlEntries).
		Int("maxGraphqlCrlBytes", lim.MaxGraphqlCrlBytes).Int("maxConcurrentWrites", lim.MaxConcurrentWrites).
		Msg("CRL upload endpoint mounted.")
	return nil
}
