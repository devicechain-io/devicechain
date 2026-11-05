// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

// installAudiencePolicy builds the token-audience policy (rule 2) from the loaded instance
// document and installs it on the readiness gate, which is where every token-verifying
// handler reads it from. The load is the first step of ExecuteInitialize and the gate opens
// only after initialize, so the configured policy is in place before any request can be
// authenticated.
//
// Three guards, each one a way this wiring has to survive a Microservice that is not a
// production one:
//
//   - It runs only when Readiness is non-nil. Break-glass builds a struct-literal
//     Microservice with no gate and loads the instance configuration; there is nothing to
//     install on, and a dereference here would panic it.
//   - The counter is registered ONCE per Microservice, on the first install, through
//     MetricsRegisterer. A second load re-installs the policy and reuses the counter.
//   - An area outside streams.PlatformAreas (including empty) gets a policy with no own-area
//     resource and one Warn, not a startup failure: tests name made-up areas.
func (ms *Microservice) installAudiencePolicy(cfg config.TokenAudienceConfiguration) error {
	if ms.Readiness == nil {
		return nil
	}
	if ms.audienceRec == nil {
		ms.audienceRec = auth.NewPromAudienceRecorder(ms.MetricsRegisterer(), ms.MetricsSubsystem())
	}
	policy, err := auth.NewPlanePolicy(cfg, ms.FunctionalArea, ms.audienceRec)
	if err != nil {
		return fmt.Errorf("infrastructure.tokenAudience: %w", err)
	}
	ms.Readiness.SetAudiencePolicy(policy)
	if !streams.IsPlatformArea(ms.FunctionalArea) {
		log.Warn().Str("area", ms.FunctionalArea).
			Msg("This functional area is not a platform area, so tokens bound to an area are never this one's")
	}
	areas := 0
	if cfg.AreaBaseUrl != "" {
		areas = len(config.AreaAudiences())
	}
	log.Info().
		Str("mode", string(cfg.EffectiveEnforcement())).
		Int("areaResources", areas).
		Int("mcpResources", len(cfg.McpResources)).
		Int("externalResources", len(cfg.ExternalResources)).
		Msg("Token audience policy installed")
	return nil
}
