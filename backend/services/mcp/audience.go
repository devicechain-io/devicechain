// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// mcpRegisteredKey is the instance-document key an operator edits to register mcp's resource.
const mcpRegisteredKey = "infrastructure.tokenAudience.mcpResources"

// checkAudienceRegistration cross-checks mcp's own resourceUrl against the instance's
// registered MCP resources (the set every API plane accepts besides its own area).
//
// mcp forwards the caller's own token, whose `aud` is mcp's resourceUrl. If that value is not
// registered, every plane refuses every forwarded call (in enforce mode) while mcp itself
// reports healthy, so:
//
//   - enforce: return an error naming the key, so mcp refuses to start;
//   - observe: log an Error and report it on the gauge, but start (the planes only count the
//     mismatch in this mode, they do not refuse).
//
// The comparison is byte-exact, like the planes' own. The gauge is 1 when registered and 0
// when not. The chart cannot produce a mismatch, so this only catches a hand-written
// existingSecret document.
func checkAudienceRegistration(resourceURL string, cfg config.TokenAudienceConfiguration,
	reg prometheus.Registerer, subsystem string) error {
	registered := false
	for _, r := range cfg.McpResources {
		if r == resourceURL {
			registered = true
			break
		}
	}
	setRegisteredGauge(reg, subsystem, registered)
	if registered {
		return nil
	}
	mode := cfg.EffectiveEnforcement()
	if mode == config.TokenAudienceEnforce {
		return fmt.Errorf("%s does not contain this service's resourceUrl %q: the API planes would "+
			"refuse every call mcp forwards; add it to infrastructure.tokenAudience.mcpResources",
			mcpRegisteredKey, resourceURL)
	}
	log.Error().Str("resourceUrl", resourceURL).Str("key", mcpRegisteredKey).Str("mode", string(mode)).
		Msg("mcp's resourceUrl is not a registered MCP resource; in enforce mode the API planes " +
			"would refuse every call it forwards, and this service would refuse to start")
	return nil
}

// setRegisteredGauge publishes devicechain_<subsystem>_token_audience_registered. It tolerates
// a second call on one registry (a retried initialize) by reusing the registered collector.
func setRegisteredGauge(reg prometheus.Registerer, subsystem string, registered bool) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "devicechain",
		Subsystem: subsystem,
		Name:      "token_audience_registered",
		Help: "1 when this service's resourceUrl is one of the instance's registered MCP resources " +
			"(infrastructure.tokenAudience.mcpResources), 0 when it is not and the API planes would " +
			"refuse the calls mcp forwards.",
	})
	if err := reg.Register(g); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			log.Warn().Err(err).Msg("could not register the token-audience registration gauge")
			return
		}
		existing, ok := are.ExistingCollector.(prometheus.Gauge)
		if !ok {
			return
		}
		g = existing
	}
	if registered {
		g.Set(1)
	} else {
		g.Set(0)
	}
}
