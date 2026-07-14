// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

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
// profile is published (ADR-051 slice 4b-3), carrying the ENABLED detection rules
// frozen into the new immutable version so event-processing's DETECT engine can run
// them. The rules are keyed on the immutable ProfileVersionToken
// ("{profileToken}@{version}", ADR-045) — the same token a resolved event
// denormalizes — so the engine scopes them read-free and a rollback needs no new
// fact (the target version's rules stay loaded). Disabled rules are omitted: they
// ride the frozen snapshot but are inert until a later publish enables them. The
// tenant is not a field: it travels on the per-tenant NATS subject.
type DetectionRulesPublishedEvent struct {
	ProfileVersionToken string
	Rules               []PublishedDetectionRule
	// PublishedAt is the version's commit time (ADR-051 slice 4c-2): the moment this
	// rule set became active. event-processing uses it as the rule-activation half of
	// the dead-man grace-period base — a never-reported device's absence deadline is
	// max(device-created, rule-published) + timeout, so publishing an absence rule
	// gives an already-existing quiet device one timeout of grace before firing rather
	// than an instant fleet-wide burst. Zero (absent) means "treat as immemorial": the
	// grace base falls back to the device's creation time alone.
	PublishedAt time.Time
}

// DetectionRulesPublishedPublisher publishes detection-rules-published events
// (ADR-051 slice 4b-3). Like the alarm and entity-deleted publishers it is best-effort
// and side-band to the publish: a marshal/publish failure is logged by the implementation,
// never surfaced to the caller — a NATS hiccup must not fail or retry the profile publish.
// The emit is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is durably
// persisted by event-processing's consumer (persist-before-ack) and so survives a restart,
// but a fact that never reaches the stream is NOT recovered by replay — it relies on a
// subsequent publish or the planned reconciliation sweep, exactly like a missed
// entity-deleted event. Implementations must be safe for concurrent use.
type DetectionRulesPublishedPublisher interface {
	PublishDetectionRulesPublished(ctx context.Context, event *DetectionRulesPublishedEvent)
}
