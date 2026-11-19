// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// This file is the model side of the alarm state-change event (ADR-041), the envelope
// re-emitted whenever a raised Alarm transitions: its shape is contract.AlarmStateChangeEvent,
// and this file holds the small publisher interface the model layer emits through and the
// code that builds the event from an alarm row. The DETECT edge integrator and the operator API both mutate alarms
// via the same *Api, so emitting here — through an injected interface — gives one uniform
// event stream for every transition (integrator raise/escalate/clear and operator
// ack/clear). It is the substrate for graphql-ws subscriptions (2.E, ADR-037) and
// notifications (ADR-017).
//
// The model layer depends only on the AlarmEventPublisher interface, never on the
// messaging/proto packages: the concrete NATS-backed publisher lives in the processor
// layer and is injected at wiring time (dependency inversion). A nil publisher (tests,
// or before wiring) disables emission.

// AlarmEventPublisher publishes alarm state-change events (ADR-041). Emission is
// side-band to the alarm write and returns nothing: a broker fault must not fail or
// retry the DB transition that produced it.
//
// 🔴 THAT IS NOT THE SAME AS BEST-EFFORT, AND THE IMPLEMENTATION MUST NOT TREAT IT AS
// SUCH. The transition is already committed, so an event that never reaches the stream
// is an alarm nobody is paged about; the subscriber that matters reads a durable whose
// cursor only advances over messages that were actually published, and no reconciler
// walks alarm rows for transitions the stream never carried. Swallowing a failure here
// therefore loses the page silently. An implementation owes the caller no error, but it
// owes an operator a RECORD — see processor.AlarmEventWriter, which retries and then
// dead-letters with a counter. Implementations must be safe for concurrent use.
type AlarmEventPublisher interface {
	PublishAlarmEvent(ctx context.Context, event *AlarmStateChangeEvent)
	PublishAlarmEventDedup(ctx context.Context, event *AlarmStateChangeEvent, dedupID string) error
}

// severityTransition classifies an in-place severity change on an ACTIVE alarm.
// Severity ranks run 0 (CRITICAL, most severe) .. 4 (INDETERMINATE, least): a
// numerically lower rank is an escalation. It reports changed=false when the severity
// is unchanged — a value-only update emits no event — or when either severity is
// unknown (rank < 0), which the integrator never produces but is guarded against so a
// bogus value can't manufacture a spurious escalation event.
func severityTransition(prev, next string) (AlarmEventType, bool) {
	pr, nr := AlarmSeverity(prev).Rank(), AlarmSeverity(next).Rank()
	if pr < 0 || nr < 0 || pr == nr {
		return "", false
	}
	if nr < pr {
		return AlarmEventEscalated, true
	}
	return AlarmEventDeescalated, true
}

// newAlarmStateChangeEvent builds an event from an alarm's post-transition state. The
// caller passes the transition kind, the prior severity (empty except for a severity
// change), and the event time driving the transition. It reads the alarm's fields, so
// the caller must update the in-memory row to its new state before calling — the
// column-limited Updates used on the write paths do not write back into the struct.
func newAlarmStateChangeEvent(a *Alarm, etype AlarmEventType, prevSeverity string, occurredTime time.Time) *AlarmStateChangeEvent {
	ev := &AlarmStateChangeEvent{
		EventType:        etype,
		AlarmToken:       a.Token,
		OriginatorType:   a.OriginatorType,
		OriginatorId:     a.OriginatorId,
		AlarmKey:         a.AlarmKey,
		MetricKey:        a.MetricKey,
		State:            a.State,
		Severity:         a.Severity,
		Acknowledged:     a.Acknowledged,
		PreviousSeverity: prevSeverity,
		RaisedTime:       a.RaisedTime,
		OccurredTime:     occurredTime,
		ScopeAnchors:     append([]string(nil), a.ScopeAnchors...),
	}
	if a.PositionDerived != nil {
		// A copy: the event outlives this call and must not alias the row's field.
		verdict := *a.PositionDerived
		ev.PositionDerived = &verdict
	}
	if a.AcknowledgedBy.Valid {
		by := a.AcknowledgedBy.String
		ev.AcknowledgedBy = &by
	}
	if a.LastValue.Valid {
		v := a.LastValue.Float64
		ev.LastValue = &v
	}
	return ev
}
