// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

import "time"

// PublishedDetectionRule is one detection rule carried in a
// DetectionRulesPublishedEvent: its authoring token (unique per profile, ADR-042)
// and the opaque rules.Rule JSON definition (ADR-051 slice 4b-1). device-management
// never parses the definition — event-processing's DETECT compiler does, on consume.
//
// EntityGroupToken / EntityGroupVersion carry the rule's OPTIONAL group scope (ADR-062
// S4): the published dynamic entity-group version whose members the rule fires for. An
// unscoped (profile-wide) rule carries an empty token and version 0 — the engine treats
// a scoped rule (non-empty token) as firing only for events whose ScopeMemberships
// include {EntityGroupToken}@{EntityGroupVersion}. device-management does not interpret
// the scope beyond freezing and propagating it; the membership set-test is the engine's.
type PublishedDetectionRule struct {
	Token              string
	Definition         string
	EntityGroupToken   string
	EntityGroupVersion int32
}

// DetectionRulesPublishedEvent is the envelope emitted post-commit when a device
// profile is published or rolled back (ADR-051 slice 4b-3 / 4c-2), carrying the
// ENABLED detection rules frozen into the now-active version so event-processing's
// DETECT engine can run them. The rules are keyed on the immutable ProfileVersionToken
// ("{profileToken}@{version}", ADR-045) — the same token a resolved event
// denormalizes — so the engine scopes them read-free; a rollback re-emits the target
// version's fact so the engine learns which version is active. Disabled rules are omitted: they
// ride the frozen snapshot but are inert until a later publish enables them. The
// tenant is not a field: it travels on the per-tenant NATS subject.
type DetectionRulesPublishedEvent struct {
	ProfileVersionToken string
	Rules               []PublishedDetectionRule
	// PublishedAt is the profile's STORED activation instant, active_since (ADR-051 slice
	// 4c-2): the moment this rule set became active — by publish, or by rollback to it. It is
	// stored rather than read off the clock at send time so event-processing's reconcile
	// against this service reproduces exactly the value this fact carries. event-processing
	// uses it as the rule-activation half of
	// the dead-man grace-period base — a never-reported device's absence deadline is
	// max(device-created, rule-published) + timeout, so publishing an absence rule
	// gives an already-existing quiet device one timeout of grace before firing rather
	// than an instant fleet-wide burst. Zero (absent) means "treat as immemorial": the
	// grace base falls back to the device's creation time alone.
	PublishedAt time.Time
}
