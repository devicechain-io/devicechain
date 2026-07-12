// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// This file defines the alarm state-change event (ADR-041): the envelope re-emitted
// whenever a raised Alarm transitions, plus the small publisher interface the model
// layer emits through. The DETECT edge integrator and the operator API both mutate alarms
// via the same *Api, so emitting here — through an injected interface — gives one uniform
// event stream for every transition (integrator raise/escalate/clear and operator
// ack/clear). It is the substrate for graphql-ws subscriptions (2.E, ADR-037) and
// notifications (ADR-017).
//
// The model layer depends only on the AlarmEventPublisher interface, never on the
// messaging/proto packages: the concrete NATS-backed publisher lives in the processor
// layer and is injected at wiring time (dependency inversion). A nil publisher (tests,
// or before wiring) disables emission.

// AlarmEventType names the kind of transition an alarm underwent. It is deliberately
// coarser than the internal state moves: a subscriber (a notification rule, a UI)
// cares that a problem appeared, worsened, improved, resolved, or was acknowledged —
// not whether a raise was a first raise or a re-raise of a previously cleared row.
type AlarmEventType string

const (
	// AlarmEventRaised is emitted when an alarm goes ACTIVE — either newly created or
	// reactivated from CLEARED. Both are "a problem is now present" to a subscriber.
	AlarmEventRaised AlarmEventType = "RAISED"
	// AlarmEventEscalated is emitted when an ACTIVE alarm's severity increases in
	// place (a higher tier now fires).
	AlarmEventEscalated AlarmEventType = "ESCALATED"
	// AlarmEventDeescalated is emitted when an ACTIVE alarm's severity decreases in
	// place (it still fires, but at a lower tier).
	AlarmEventDeescalated AlarmEventType = "DEESCALATED"
	// AlarmEventCleared is emitted when an alarm goes CLEARED — whether cleared by the
	// DETECT edge integrator (its contributor set emptied) or manually by an operator.
	AlarmEventCleared AlarmEventType = "CLEARED"
	// AlarmEventAcknowledged is emitted when an operator acknowledges an alarm.
	AlarmEventAcknowledged AlarmEventType = "ACKNOWLEDGED"
)

// Valid reports whether the value names one of the known event types.
func (t AlarmEventType) Valid() bool {
	switch t {
	case AlarmEventRaised, AlarmEventEscalated, AlarmEventDeescalated,
		AlarmEventCleared, AlarmEventAcknowledged:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t AlarmEventType) String() string { return string(t) }

// AlarmStateChangeEvent is the envelope emitted on each alarm transition. It is a
// snapshot of the alarm after the transition plus the transition kind and, for a
// severity change, the prior severity — enough for a subscriber to render the change
// or drive a notification without re-reading the alarm. The alarm is addressed by its
// token so a subscriber can resolve or re-query it; the originator is carried as the
// uniform (type, id) reference (ADR-013).
type AlarmStateChangeEvent struct {
	EventType      AlarmEventType
	AlarmToken     string
	OriginatorType string
	OriginatorId   uint
	AlarmKey       string
	MetricKey      string

	State        string // ACTIVE | CLEARED after the transition
	Severity     string // severity after the transition
	Acknowledged bool

	// PreviousSeverity is set only for ESCALATED/DEESCALATED, naming the severity the
	// alarm held before the change; empty otherwise.
	PreviousSeverity string

	AcknowledgedBy *string
	LastValue      *float64
	Message        *string

	// RaisedTime is when the current alarm cycle began (the row's RaisedTime). Paired
	// with OccurredTime it lets a subscriber show how long an alarm was active without
	// re-querying — e.g. a CLEARED notification "active 42m" — the standard clear/ack
	// line.
	RaisedTime time.Time

	// OccurredTime is the event time that drove the transition — the measurement's
	// occurred time for an evaluator transition, or the operation time for an operator
	// ack/clear — so the emitted timeline matches the alarm's own timestamps.
	OccurredTime time.Time
}

// AlarmEventPublisher publishes alarm state-change events (ADR-041). Emission is
// best-effort and side-band to the alarm write: the alarm row is the source of truth
// and a subscriber can always re-query, so a failed publish is logged by the
// implementation, never surfaced to the caller (a NATS hiccup must not fail or retry
// the DB transition). Implementations must be safe for concurrent use.
type AlarmEventPublisher interface {
	PublishAlarmEvent(ctx context.Context, event *AlarmStateChangeEvent)
}

// severityTransition classifies an in-place severity change on an ACTIVE alarm.
// Severity ranks run 0 (CRITICAL, most severe) .. 4 (INDETERMINATE, least): a
// numerically lower rank is an escalation. It reports changed=false when the severity
// is unchanged — a value-only update emits no event — or when either severity is
// unknown (rank < 0), which the evaluator never produces but is guarded against so a
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
	}
	if a.AcknowledgedBy.Valid {
		by := a.AcknowledgedBy.String
		ev.AcknowledgedBy = &by
	}
	if a.LastValue.Valid {
		v := a.LastValue.Float64
		ev.LastValue = &v
	}
	if a.Message.Valid {
		m := a.Message.String
		ev.Message = &m
	}
	return ev
}
