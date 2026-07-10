// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/rs/zerolog/log"
)

// ScopedRule is a compiled rule tagged with the two dimensions the fan-out selects on: the
// tenant that owns it and the profile version it applies to. A resolved event carries its
// device's profile-version token (denormalized at resolution, ADR-051 slice 1), so the
// runtime selects the applicable rules read-free — no graph read back into device-management.
type ScopedRule struct {
	// Tenant is the owning tenant. It MUST equal the tenant prefix of Compiled.ID
	// (RuleTenant): the backstop treats a divergence as a mis-filed rule and refuses to
	// publish its detections to any tenant subject.
	Tenant string
	// ProfileVersionToken is the "{profileToken}@{version}" the rule is homed on (ADR-045).
	// A resolved event feeds this rule only when its ProfileVersionToken matches.
	ProfileVersionToken string
	// Compiled is the slice-3 lowered rule (core config + leaf predicate + feed metrics).
	Compiled *rules.CompiledRule
}

// scopeKey indexes rules by the (tenant, profile-version) pair a resolved event selects on.
type scopeKey struct {
	tenant         string
	profileVersion string
}

// RuleRegistry is the immutable, read-only snapshot of the rule set the engine runs. It is
// built once from a RuleSource at startup (dynamic CRUD-driven updates arrive in slice 4b,
// where a rebuild is marshaled onto the single-writer loop) and is safe for concurrent
// reads. It answers two questions on the hot path: which rules a resolved event feeds
// (RulesFor, by scope) and which rule owns a drained detection (Lookup, by id).
type RuleRegistry struct {
	byID    map[string]*ScopedRule
	byScope map[scopeKey][]*ScopedRule
	cores   []core.Rule
}

// NewRuleRegistry builds the registry from a rule set, filing each rule under the scope its
// source assigned. It does NOT here re-validate that a rule's id-tenant matches its owning
// Tenant: that divergence is the runtime tenant backstop's job, refused at the publish
// boundary (derived.go) before any tenant subject is written — so a mis-minted rule may run
// but its detections are contained (DLQ'd, never published).
//
// It DOES enforce the invariants the backstop's Lookup-based attribution depends on, dropping
// (fail-closed, loudly) any rule that would break them:
//   - a nil compiled rule (nothing to run);
//   - a DUPLICATE Compiled.ID — id uniqueness is load-bearing: byID keys on the id, so a
//     collision would make Lookup (and thus the tenant backstop) resolve a detection to the
//     WRONG rule, and byScope would feed both, double-counting every event. A collision means
//     a generator bug (ids are globally unique by construction: {tenant}/{version}/{key});
//   - an empty owning Tenant (a rule that could never be attributed to a tenant subject).
func NewRuleRegistry(scoped []ScopedRule) *RuleRegistry {
	reg := &RuleRegistry{
		byID:    make(map[string]*ScopedRule, len(scoped)),
		byScope: make(map[scopeKey][]*ScopedRule),
		cores:   make([]core.Rule, 0, len(scoped)),
	}
	for i := range scoped {
		sr := scoped[i]
		if sr.Compiled == nil {
			continue
		}
		if sr.Tenant == "" {
			log.Warn().Str("rule", sr.Compiled.ID).Msg("Dropping rule with no owning tenant (fail-closed).")
			continue
		}
		if _, dup := reg.byID[sr.Compiled.ID]; dup {
			log.Error().Str("rule", sr.Compiled.ID).
				Msg("Dropping rule with a duplicate id (generator bug; id uniqueness is required for tenant attribution).")
			continue
		}
		p := &sr
		reg.byID[sr.Compiled.ID] = p
		k := scopeKey{tenant: sr.Tenant, profileVersion: sr.ProfileVersionToken}
		reg.byScope[k] = append(reg.byScope[k], p)
		reg.cores = append(reg.cores, sr.Compiled.Core)
	}
	return reg
}

// RulesFor returns the rules a resolved event in (tenant, profileVersion) feeds. The empty
// profile-version token (an unprofiled or unpublished device, ADR-045) matches only rules
// filed under that empty token — i.e. none in practice — so such a device has no rules,
// which is the intended read-free "no resolvable rules" outcome.
func (reg *RuleRegistry) RulesFor(tenant, profileVersion string) []*ScopedRule {
	return reg.byScope[scopeKey{tenant: tenant, profileVersion: profileVersion}]
}

// Lookup returns the rule that owns a detection's id, or (nil, false) if the rule is not in
// the registry (e.g. removed after a still-buffered detection fired — dropped, not leaked).
func (reg *RuleRegistry) Lookup(id string) (*ScopedRule, bool) {
	sr, ok := reg.byID[id]
	return sr, ok
}

// Cores is the full core.Rule set the engine is constructed with — every registered rule's
// keyed-streaming config, keyed by the same tenant-prefixed id the fan-out feeds it under.
func (reg *RuleRegistry) Cores() []core.Rule { return reg.cores }

// Count is the number of admitted rules (bounded, tenant-label-free — the aggregate the
// Slice-4 rule-count gauge reports; a per-tenant breakdown is an ADR-023 governance concern
// where the cardinality is budgeted, not a hot-path metric).
func (reg *RuleRegistry) Count() int { return len(reg.byID) }

// RuleSource yields the rule set the registry is built from. Slice 4a is backed by a static
// source (bootstrapped/tested set); slice 4b replaces it with one fed by device-management's
// published-rule fact events, scoped by profile-version token.
type RuleSource interface {
	Load(ctx context.Context) ([]ScopedRule, error)
}

// StaticRuleSource is a fixed rule set — the 4a seam a test or a bootstrap populates, and
// the placeholder the 4b fact-event source supersedes.
type StaticRuleSource struct {
	Rules []ScopedRule
}

// Load returns the fixed set.
func (s StaticRuleSource) Load(context.Context) ([]ScopedRule, error) { return s.Rules, nil }
