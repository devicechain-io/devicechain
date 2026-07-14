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
	// GroupToken / GroupVersion are the rule's OPTIONAL group scope (ADR-062 S4): the published
	// dynamic entity-group version whose members the rule fires for. Empty token ⇒ an unscoped
	// (profile-wide) rule. When set, Plan feeds this rule only for events whose ScopeMemberships
	// include {GroupToken}@{GroupVersion}, and feeds a DESCOPE for events that lack it (dropping
	// the series' state + resolving). The scope is constant per rule id (the id embeds the
	// immutable profile-version token, so a scope change always mints a new id), so it needs no
	// change-detection beyond Definition.
	GroupToken   string
	GroupVersion int32
	// Definition is the raw opaque rule JSON the Compiled came from. It is retained purely as
	// a change-detector: a reused profile token can re-mint an existing rule id with a
	// DIFFERENT body whose difference lives only in the predicate (not the lowered core.Rule),
	// so comparing definitions is the reliable "did the rule change" test used to GC stale
	// engine state on such a replacement (applyRuleUpdate). Empty in tests that build a
	// ScopedRule directly.
	Definition string
}

// scopeKey indexes rules by the (tenant, profile-version) pair a resolved event selects on.
type scopeKey struct {
	tenant         string
	profileVersion string
}

// RuleRegistry is the rule set the engine runs. It is built from a RuleSource at startup
// and then mutated LIVE by published-rule facts (ADR-051 slice 4b-3): Upsert/Remove are
// marshaled onto the processor's single-writer loop, the same goroutine that reads it on
// the hot path (RulesFor, Lookup), so it needs no lock — all mutation and all reads share
// one goroutine. It answers two questions on the hot path: which rules a resolved event
// feeds (RulesFor, by scope) and which rule owns a drained detection (Lookup, by id).
type RuleRegistry struct {
	byID    map[string]*ScopedRule
	byScope map[scopeKey][]*ScopedRule
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
	}
	for i := range scoped {
		if _, dup := reg.byID[scoped[i].id()]; dup && scoped[i].Compiled != nil {
			// The startup rule set is rebuilt from the durable projection, whose primary key is
			// the rule id, so it is already deduped (a re-published id overwrote in place,
			// last-wins) — a duplicate here is not the normal restart/live divergence (that is
			// gone: both paths see the projection's single row per id) but a genuine generator
			// bug, dropped loudly so Lookup-based tenant attribution can never bind to the wrong
			// rule.
			log.Error().Str("rule", scoped[i].Compiled.ID).
				Msg("Dropping rule with a duplicate id (generator bug; id uniqueness is required for tenant attribution).")
			continue
		}
		reg.Upsert(scoped[i])
	}
	return reg
}

// id is the rule's compiled id, or "" when there is nothing to file (a nil compiled rule).
func (sr ScopedRule) id() string {
	if sr.Compiled == nil {
		return ""
	}
	return sr.Compiled.ID
}

// Upsert adds or replaces a rule, filing it under its (tenant, profile-version) scope, and
// reports whether it was admitted. It is the live mutation the published-rule fact consumer
// applies on the single-writer loop (ADR-051 slice 4b-3). It enforces the invariants the
// tenant backstop's Lookup-based attribution depends on, refusing (fail-closed, and for the
// admission-time cases loudly) anything that would break them: a nil compiled rule (nothing
// to run) and an empty owning Tenant (a rule no tenant subject could attribute). A repeated
// id is NOT a bug here (unlike NewRuleRegistry's batch): a rule id embeds its immutable
// profile-version token, so a re-published/redelivered fact carries a byte-identical rule —
// it replaces the entry in place (byID and byScope share one *ScopedRule, so the update is
// seen through both) while the engine's UpsertRule preserves the running rule's state.
func (reg *RuleRegistry) Upsert(sr ScopedRule) bool {
	if sr.Compiled == nil {
		return false
	}
	if sr.Tenant == "" {
		log.Warn().Str("rule", sr.Compiled.ID).Msg("Dropping rule with no owning tenant (fail-closed).")
		return false
	}
	k := scopeKey{tenant: sr.Tenant, profileVersion: sr.ProfileVersionToken}
	if existing, ok := reg.byID[sr.Compiled.ID]; ok {
		oldK := scopeKey{tenant: existing.Tenant, profileVersion: existing.ProfileVersionToken}
		*existing = sr // mutate through the shared pointer so byScope sees the update too
		if oldK != k {
			// Defensive: an id's scope is immutable in practice (the version token is part of
			// the id), but re-file if it ever diverges rather than leave a stale scope entry.
			reg.detachFromScope(oldK, existing)
			reg.byScope[k] = append(reg.byScope[k], existing)
		}
		return true
	}
	p := &sr
	reg.byID[sr.Compiled.ID] = p
	reg.byScope[k] = append(reg.byScope[k], p)
	return true
}

// Remove evicts a rule from the registry — dropping it from byID and its scope bucket — and
// reports whether it was present. It is the registry half of a rule removal (ADR-051 slice
// 4b-3); the engine's RemoveRule GCs the matching keyed state. The publish path never
// removes (versions are immutable and superseded ones are retained so in-flight events still
// match, and a rollback re-selects an already-loaded version); removal is a governance/
// teardown path (ADR-023/052), reachable on the single-writer loop so that path is a wiring
// change, not an engine change.
func (reg *RuleRegistry) Remove(id string) bool {
	sr, ok := reg.byID[id]
	if !ok {
		return false
	}
	delete(reg.byID, id)
	reg.detachFromScope(scopeKey{tenant: sr.Tenant, profileVersion: sr.ProfileVersionToken}, sr)
	return true
}

// detachFromScope removes one rule pointer from its scope bucket, deleting the bucket when it
// empties so RulesFor never returns an empty non-nil slice and stale scopes do not accumulate.
func (reg *RuleRegistry) detachFromScope(k scopeKey, sr *ScopedRule) {
	bucket := reg.byScope[k]
	for i, p := range bucket {
		if p == sr {
			bucket = append(bucket[:i], bucket[i+1:]...)
			break
		}
	}
	if len(bucket) == 0 {
		delete(reg.byScope, k)
	} else {
		reg.byScope[k] = bucket
	}
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

// AbsenceRulesFor returns the ABSENCE rules a resolved event in (tenant, profileVersion) would
// feed — the subset of RulesFor whose core kind is Absence. The dead-man armer (ADR-051 slice
// 4c-2b-2b) uses it to resolve a rostered device's active profile version to the absence rules
// it must arm a never-seen device against; only absence has a dead-man timer, so the other kinds
// are irrelevant to arming. Returns nil (never an empty non-nil slice) when the scope has none.
func (reg *RuleRegistry) AbsenceRulesFor(tenant, profileVersion string) []*ScopedRule {
	var out []*ScopedRule
	for _, sr := range reg.byScope[scopeKey{tenant: tenant, profileVersion: profileVersion}] {
		if sr.Compiled != nil && sr.Compiled.Core.Kind == core.Absence {
			out = append(out, sr)
		}
	}
	return out
}

// Cores is the full core.Rule set the engine is constructed with at startup — every
// registered rule's keyed-streaming config. It is computed on demand (rather than cached)
// because it is read once, at engine build/restore; live mutation goes straight to the
// engine via UpsertRule/RemoveRule, so there is no cache to keep in sync. Order is
// unspecified (NewEngine files them into an id-keyed map), so it need not be deterministic.
func (reg *RuleRegistry) Cores() []core.Rule {
	cores := make([]core.Rule, 0, len(reg.byID))
	for _, sr := range reg.byID {
		cores = append(cores, sr.Compiled.Core)
	}
	return cores
}

// IDs returns a snapshot of every admitted rule's compiled id. It exists for the post-replay
// registry reconcile (ResolvedEventsProcessor.reconcileRegistry), which diffs the live registry
// against a freshly-reloaded projection to remove rules the projection no longer holds — the caller
// ranges this snapshot rather than byID directly so it can Remove during the walk without mutating
// the map it is iterating.
func (reg *RuleRegistry) IDs() []string {
	ids := make([]string, 0, len(reg.byID))
	for id := range reg.byID {
		ids = append(ids, id)
	}
	return ids
}

// Count is the number of admitted rules (bounded, tenant-label-free — the aggregate the
// Slice-4 rule-count gauge reports; a per-tenant breakdown is an ADR-023 governance concern
// where the cardinality is budgeted, not a hot-path metric).
func (reg *RuleRegistry) Count() int { return len(reg.byID) }

// RuleCountsByTenant returns the number of admitted rules per owning tenant — the per-tenant rule
// dimension of the ADR-023 runtime state budget (ADR-051 slice 6c). It is computed on demand from
// byID on the single-writer loop (a governance read, not a hot-path metric), so it does not itself
// become a per-tenant labelled series; the caller reduces it to bounded aggregates (a count of
// over-budget tenants) before emitting any metric.
func (reg *RuleRegistry) RuleCountsByTenant() map[string]int {
	counts := make(map[string]int)
	for _, sr := range reg.byID {
		counts[sr.Tenant]++
	}
	return counts
}

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
