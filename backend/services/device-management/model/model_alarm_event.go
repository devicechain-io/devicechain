// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// AlarmEventPublisher acknowledges alarm snapshots from durable outboxes. Ordinary
// transitions enqueue atomically; the relay retries broker failures with stable IDs.
// Agent effects and deletion cascades retain their own outboxes. Implementations
// must be safe for concurrent use and return ErrAlarmEventUnpublishable for encoding
// failures so the relay can quarantine the payload without blocking later events.
type AlarmEventPublisher interface {
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
