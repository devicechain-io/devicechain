// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// Edge discriminates the two edges of an alarm-bearing detection ON THE WIRE (ADR-057): a rising
// edge (the condition became true) and a falling edge (it ceased). It is the string projection of
// core.EdgeKind so an ADR-037 subscriber and the REACT dispatcher read it without importing the core
// enum. EdgeRaised is the DEFAULT (a bare/legacy event with no edge field, or one carrying "raised",
// is a raise) so only an explicit "resolved" is a falling edge — see wireEdge / DerivedEvent.Edge.
const (
	EdgeRaised   = "raised"
	EdgeResolved = "resolved"
)

// wireEdge projects a core edge onto its wire token.
func wireEdge(e core.EdgeKind) string {
	if e == core.EdgeResolved {
		return EdgeResolved
	}
	return EdgeRaised
}

// DerivedEvent is DETECT's emitted signal on the wire — a first-class, subscribe-able
// product (ADR-037). Its fields are the stable, deterministic identity of a detection, so
// an at-least-once re-emission across a replay collapses downstream via
// (RuleID, Series, Kind, OccurredTime, Edge) as the idempotency key (ADR-051 §8 / ADR-057). It is a
// JSON envelope for slice 4a; the wire may harden to protobuf when the client subscription lands
// (slice 7) — pre-GA, changeable.
type DerivedEvent struct {
	// RuleID is the tenant-prefixed id of the rule that fired.
	RuleID string `json:"ruleId"`
	// Tenant is the owning tenant (redundant with the subject, carried for convenience).
	Tenant string `json:"tenant"`
	// Kind is the user-facing rule type (rules.RuleType: "threshold", "absence", …).
	Kind string `json:"kind"`
	// Edge is the transition this detection reports (ADR-057): "raised" on the rising edge, "resolved"
	// on the falling edge. It IS part of the dedup identity — a Raised and a Resolved that share an
	// OccurredTime (a gap-driven falling edge coinciding with a fresh rising edge on adjacent events)
	// must not collapse into one downstream. omitempty keeps a raise lean on the wire; an absent value
	// decodes as the EdgeRaised default, so a pre-edge event still reads as a raise (the REACT dispatcher
	// keys off an explicit "resolved", not off empty).
	Edge string `json:"edge,omitempty"`
	// Series is the keyed series the detection is for — the device token, or the anchor
	// token for a correlation rule.
	Series string `json:"series"`
	// OccurredTime is the logical (event) time the detection is stamped at — the same value
	// that anchors its dedup identity.
	OccurredTime time.Time `json:"occurredTime"`
	// Severity is the detection's significance tier (ADR-041 vocabulary, lowercase) — an
	// INFORMATIONAL fire-time snapshot for ADR-037 subscribers, empty when the rule declares none.
	// It is NOT authoritative for what a raiseAlarm action raises at: the REACT dispatcher resolves
	// the tier from the rule as read from the durable projection at dispatch (RaiseAlarmAction), so
	// a reused-id def change between fire and dispatch raises at the projection's current tier, not
	// this snapshot. Like Kind, it is stamped here from the CURRENT registry rule at publish (the
	// same vanishingly-rare straggler skew documented for Kind), and it is excluded from the dedup
	// identity below.
	Severity string `json:"severity,omitempty"`
	// Value is the scalar the detection is about (the crossing sample, computed rate, or window
	// aggregate — see core.Detection.Value), carried so a raiseAlarm REACT action stamps the alarm
	// with the real triggering value instead of a zero (the slice-5c blocker for enabling raise-alarm
	// dispatch). It is a pointer so "no value" (silence-driven Absence/Duration, Correlation) is
	// distinct from "value is 0.0", and omitted from the wire when absent. Like Severity it is an
	// informational payload, NOT part of the dedup identity below.
	Value *float64 `json:"value,omitempty"`
	// CAVEAT (dynamic thresholds): the dedup identity does NOT include the resolved threshold. For a
	// rule whose bound comes from a device attribute (slice 4c-3), replay resolves against the CURRENT
	// attribute value, not the value at original event time (see startAttributeView's non-determinism
	// note), so replay is not detection-identical: a detection that fired originally but was still
	// buffered at a crash can be SILENTLY LOST (replay's new value no longer matches), and conversely a
	// detection that did NOT fire originally can be re-derived and PHANTOM-published at the old event
	// time — its (RuleID, Series, Kind, OccurredTime) has no prior counterpart downstream, so it is NOT
	// collapsed. This is the accepted, device/rule-scoped divergence the upstream-embedded-bound fix
	// (deferred) closes; static-threshold rules are unaffected.
}

// RejectReason is a bounded label for a dropped detection (bounded cardinality per the
// ADR-023 G.3 per-tenant-label DoS lesson — a fixed, small enum, never a tenant/rule value).
type RejectReason string

const (
	// RejectBackstop: the rule's id-tenant prefix disagrees with its owning tenant — a
	// mis-minted/mis-filed rule the tenant backstop refuses to publish (fail-closed).
	RejectBackstop RejectReason = "backstop"
	// RejectOrphan: the rule that produced the detection is no longer in the registry (e.g.
	// removed after it fired but before its buffered detection flushed). Dropped — a tenant
	// cannot be safely attributed to an unknown rule.
	RejectOrphan RejectReason = "orphan"
	// RejectMarshal: the derived-event envelope failed to serialize (practically unreachable
	// for a fixed scalar struct). Dropped — it can never succeed on retry.
	RejectMarshal RejectReason = "marshal"
)

// Metrics is the fan-out/publish observability sink, implemented by the processor (which
// owns the prometheus registration). Bounded cardinality: no per-tenant labels.
type Metrics interface {
	RecordFanout(events, evalErrors int)
	RecordDerivedPublished()
	RecordDerivedRejected(reason RejectReason)
}

// dlqMax bounds the in-memory dead-letter ring. The backstop should never fire in correct
// operation (it guards against a generator bug), so a bounded, restart-volatile record plus
// the counter and a loud error log is proportionate; a durable DLQ stream is a Slice-8 ops
// follow-up.
const dlqMax = 128

// DeadLetter records one backstopped detection for operator inspection.
type DeadLetter struct {
	RuleID        string
	OwningTenant  string
	ClaimedTenant string
	Series        string
	At            time.Time
	Reason        RejectReason
}

// Publisher turns drained detections into per-tenant derived events, enforcing the runtime
// tenant backstop at the publish boundary: it derives the sink subject's tenant from the
// rule (via the tenant-scoped writer's context) and refuses to publish when the rule's
// id-tenant prefix disagrees with the tenant it is registered under. A refused detection is
// dead-lettered and counted — never written to a tenant subject — so a mis-generated rule
// cannot leak one tenant's detection onto another's feed.
type Publisher struct {
	writer  messaging.MessageWriter
	reg     *RuleRegistry
	metrics Metrics

	mu  sync.Mutex
	dlq []DeadLetter
}

// NewPublisher builds a derived-event publisher over a tenant-scoped writer.
func NewPublisher(writer messaging.MessageWriter, reg *RuleRegistry, metrics Metrics) *Publisher {
	return &Publisher{writer: writer, reg: reg, metrics: metrics}
}

// Publish emits one detection as a derived event on its owning tenant's subject. It returns
// a non-nil error ONLY on a retryable broker failure — the caller must then NOT advance the
// checkpoint past the producing message (deliver-before-checkpoint), so a replay re-derives
// and re-emits the detection. A terminal drop (backstop reject, orphan rule, marshal
// failure) returns nil: it is intentional and must not wedge the checkpoint loop.
func (p *Publisher) Publish(ctx context.Context, det core.Detection) error {
	// ADR-057 two-edge model (6d-pre-2b): BOTH edges are now published — a Raised on the rising edge
	// and a Resolved on the falling edge, discriminated by DerivedEvent.Edge and dedup-distinct even at
	// a shared OccurredTime. The REACT dispatcher routes a Resolved to a clear (skipping send-command,
	// which has no falling-edge twin), and the ADR-037 subscriber sees the full lifecycle. A Resolved
	// carries no value (the condition ceased, not a reading).
	sr, ok := p.reg.Lookup(det.RuleID)
	if !ok {
		log.Warn().Str("rule", det.RuleID).Str("series", det.Series).
			Msg("Dropping detection for a rule no longer in the registry (orphan).")
		p.metrics.RecordDerivedRejected(RejectOrphan)
		p.record(DeadLetter{RuleID: det.RuleID, Series: det.Series, At: det.At, Reason: RejectOrphan})
		return nil
	}

	// THE TENANT BACKSTOP. The sink subject a detection is published to is scoped to the
	// rule's tenant; that tenant must be exactly the one the rule's id declares. A divergence
	// means a mis-minted/mis-filed rule — publishing it would write onto the wrong tenant's
	// feed. Fail closed: dead-letter, count, drop.
	idTenant, ok := RuleTenant(det.RuleID)
	if !ok || idTenant != sr.Tenant {
		log.Error().Str("rule", det.RuleID).Str("owningTenant", sr.Tenant).Str("claimedTenant", idTenant).
			Msg("Tenant backstop: refusing to publish a detection whose rule-id tenant disagrees with its owning tenant.")
		p.metrics.RecordDerivedRejected(RejectBackstop)
		p.record(DeadLetter{RuleID: det.RuleID, OwningTenant: sr.Tenant, ClaimedTenant: idTenant,
			Series: det.Series, At: det.At, Reason: RejectBackstop})
		return nil
	}

	// A tenant token that violates the ADR-042 grammar can never be published: the writer
	// fail-closes on it (core.ValidateToken, the same check WriteMessages runs before splicing
	// a tenant into a subject). Reject it HERE as a TERMINAL backstop drop rather than let the
	// writer's deterministic error surface as a retryable one — otherwise one mis-minted rule
	// whose tenant is internally consistent but malformed would fail every publish attempt,
	// wedge the checkpoint loop, and stall detection for EVERY tenant on the singleton.
	if err := dccore.ValidateToken(sr.Tenant); err != nil {
		log.Error().Err(err).Str("rule", det.RuleID).Str("owningTenant", sr.Tenant).
			Msg("Tenant backstop: refusing to publish a detection for a rule whose tenant token is invalid (ADR-042).")
		p.metrics.RecordDerivedRejected(RejectBackstop)
		p.record(DeadLetter{RuleID: det.RuleID, OwningTenant: sr.Tenant, ClaimedTenant: idTenant,
			Series: det.Series, At: det.At, Reason: RejectBackstop})
		return nil
	}

	// Kind is stamped from the CURRENT registry rule's Type, not re-derived from det.Kind (the
	// core enum at fire time). On the healthy path they agree. A reused-id def change between fire
	// and publish is the one skew case: a detection already buffered (pendingDets) when the rule's
	// body changed is stamped with the NEW type here. It is a pre-GA, vanishingly-rare edge (RemoveRule
	// drops the engine's UNdrained e.out on a def change, so only detections already drained into the
	// processor's buffer are exposed), and downstream dedups on (rule, series, kind, time) — so the
	// skew is a mis-typed straggler, not a correctness break. Accepted; noted for honesty.
	de := DerivedEvent{
		RuleID:       det.RuleID,
		Tenant:       sr.Tenant,
		Kind:         string(sr.Compiled.Type),
		Series:       det.Series,
		OccurredTime: det.At,
		Severity:     string(sr.Compiled.Severity),
		Edge:         wireEdge(det.Edge),
	}
	if det.HasValue {
		v := det.Value // copy the loop-local before taking its address
		de.Value = &v
	}
	payload, err := json.Marshal(de)
	if err != nil {
		// A fixed-shape struct of scalars does not fail to marshal; if it somehow does, drop
		// (terminal — it can never succeed on retry) and account for it like any other drop.
		log.Error().Err(err).Str("rule", det.RuleID).Msg("Dropping detection that failed to marshal.")
		p.metrics.RecordDerivedRejected(RejectMarshal)
		p.record(DeadLetter{RuleID: det.RuleID, OwningTenant: sr.Tenant, Series: det.Series, At: det.At, Reason: RejectMarshal})
		return nil
	}

	// The writer derives the subject from the tenant in context (fail-closed on none), so the
	// sink is exactly "{instance}.{sr.Tenant}.derived-events" — the backstop-validated tenant.
	tctx := dccore.WithTenant(ctx, sr.Tenant)
	if err := p.writer.WriteMessages(tctx, messaging.Message{Value: payload}); err != nil {
		return fmt.Errorf("publish derived event for rule %q: %w", det.RuleID, err)
	}
	p.metrics.RecordDerivedPublished()
	return nil
}

// record appends to the bounded dead-letter ring, evicting the oldest when full.
func (p *Publisher) record(dl DeadLetter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.dlq) >= dlqMax {
		copy(p.dlq, p.dlq[1:])
		p.dlq[len(p.dlq)-1] = dl
		return
	}
	p.dlq = append(p.dlq, dl)
}

// DeadLetters returns a copy of the current dead-letter ring (operator inspection).
func (p *Publisher) DeadLetters() []DeadLetter {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]DeadLetter, len(p.dlq))
	copy(out, p.dlq)
	return out
}
