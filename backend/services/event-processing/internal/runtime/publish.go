// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/rs/zerolog/log"
)

// PublishedRuleID composes the runtime id "{tenant}/{profileVersionToken}/{ruleToken}" of a
// published detection rule. It is the single source of the id format, shared by the compile
// path (CompilePublishedRules) and the durable projection's row key, so the id the engine
// runs under and the id persisted for rebuild can never drift.
func PublishedRuleID(tenant, profileVersionToken, ruleToken string) string {
	return ComposeRuleID(tenant, profileVersionToken+tenantSep+ruleToken)
}

// CompilePublishedRules lowers the ENABLED detection rules carried in one published-rule
// fact into ScopedRules ready for the registry and engine (ADR-051 slice 4b-3). It is the
// single compile path both the startup rebuild (the store-backed RuleSource) and the live
// fact consumer run, so a rule loads identically however it arrives.
//
// Each rule's runtime id is composed here — "{tenant}/{profileVersionToken}/{ruleToken}" —
// from the tenant (which rode the fact's subject) and the fact's version token; the stored
// definition carries no id (the token is assigned at this fact-emit boundary, ADR-051 slice
// 4b-1). Rules compile under rules.DefaultLimits, the SAME budget the ADR-044 publish gate
// enforced (slice 4b-2), so a rule that passed the gate compiles here too. The raw definition
// rides on the ScopedRule so a change under a reused id is detectable (applyRuleUpdate) even
// when the lowered core.Rule is unchanged (the difference is in the predicate).
//
// A rule that fails to Decode/Compile is logged LOUDLY and skipped, not fatal: the publish
// gate should have rejected it before the version was frozen, so a failure here is a
// gate/consumer contract violation (or a hand-edited stream) — the offending rule does not
// run (fail-closed) while every other rule in the fact still loads. The returned count of
// such failures lets the caller surface the anomaly.
func CompilePublishedRules(tenant, profileVersionToken string, published []dmmodel.PublishedDetectionRule) ([]ScopedRule, int) {
	limits := rules.DefaultLimits()
	scoped := make([]ScopedRule, 0, len(published))
	failed := 0
	for _, p := range published {
		if profileVersionToken == "" || p.Token == "" {
			// An empty version token or rule token would compose an id whose tenant segment
			// still parses but whose scope/attribution is meaningless (and two empty tokens
			// would collide). The authoring grammar (ADR-042) forbids empties, so this only
			// guards a malformed/forged fact — drop it, symmetric with the publish gate.
			failed++
			log.Error().Str("tenant", tenant).Str("profileVersion", profileVersionToken).
				Str("rule", p.Token).Msg("Published detection rule has an empty version or rule token; skipping.")
			continue
		}
		id := PublishedRuleID(tenant, profileVersionToken, p.Token)
		rule, err := rules.Decode([]byte(p.Definition))
		if err != nil {
			failed++
			log.Error().Err(err).Str("tenant", tenant).Str("profileVersion", profileVersionToken).
				Str("rule", p.Token).Msg("Published detection rule failed to decode; skipping (publish gate should have rejected it).")
			continue
		}
		rule.ID = id
		compiled, err := rules.Compile(rule, limits)
		if err != nil {
			failed++
			log.Error().Err(err).Str("tenant", tenant).Str("profileVersion", profileVersionToken).
				Str("rule", p.Token).Msg("Published detection rule failed to compile; skipping (publish gate should have rejected it).")
			continue
		}
		scoped = append(scoped, ScopedRule{Tenant: tenant, ProfileVersionToken: profileVersionToken, Compiled: compiled, Definition: p.Definition})
	}
	return scoped, failed
}
