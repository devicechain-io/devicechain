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

// DerivedEvent is DETECT's emitted signal on the wire — a first-class, subscribe-able
// product (ADR-037). Its fields are the stable, deterministic identity of a detection, so
// an at-least-once re-emission across a replay collapses downstream via
// (RuleID, Series, Kind, OccurredTime) as the idempotency key (ADR-051 §8). It is a JSON
// envelope for slice 4a; the wire may harden to protobuf when the client subscription lands
// (slice 7) — pre-GA, changeable.
type DerivedEvent struct {
	// RuleID is the tenant-prefixed id of the rule that fired.
	RuleID string `json:"ruleId"`
	// Tenant is the owning tenant (redundant with the subject, carried for convenience).
	Tenant string `json:"tenant"`
	// Kind is the user-facing rule type (rules.RuleType: "threshold", "absence", …).
	Kind string `json:"kind"`
	// Series is the keyed series the detection is for — the device token, or the anchor
	// token for a correlation rule.
	Series string `json:"series"`
	// OccurredTime is the logical (event) time the detection is stamped at — the same value
	// that anchors its dedup identity.
	OccurredTime time.Time `json:"occurredTime"`
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

	de := DerivedEvent{
		RuleID:       det.RuleID,
		Tenant:       sr.Tenant,
		Kind:         string(sr.Compiled.Type),
		Series:       det.Series,
		OccurredTime: det.At,
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
