// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

import "time"

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

// AlarmClearReasonOriginatorDeleted identifies a clear caused by entity deletion.
const AlarmClearReasonOriginatorDeleted = "ORIGINATOR_DELETED"

// AlarmStateChangeEvent is the envelope emitted on each alarm transition. It is a
// snapshot of the alarm after the transition plus the transition kind and, for a
// severity change, the prior severity — enough for a subscriber to render the change
// or drive a notification without re-reading the alarm. The alarm is addressed by its
// token so a subscriber can resolve or re-query it; the originator is carried as the
// uniform (type, id) reference (ADR-013).
type AlarmStateChangeEvent struct {
	ClearReason    string
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

	// RaisedTime is when the current alarm cycle began (the row's RaisedTime). Paired
	// with OccurredTime it lets a subscriber show how long an alarm was active without
	// re-querying — e.g. a CLEARED notification "active 42m" — the standard clear/ack
	// line.
	RaisedTime time.Time

	// OccurredTime is the event time that drove the transition — the detection's
	// occurred time for a rule-driven (integrator) transition, or the operation time for an
	// operator ack/clear — so the emitted timeline matches the alarm's own timestamps.
	OccurredTime time.Time

	// ScopeAnchors is the alarm's scope stamp (Alarm.ScopeAnchors): what the live alarm
	// stream matches a member whose access is limited to some customers against, so the
	// stream and the alarms query agree on what such a member sees (ADR-081).
	ScopeAnchors []string

	// DeviceToken, DeviceName and DeviceTypeToken describe the originating device as it was
	// when the event was emitted, so a notification can name what raised the alarm instead of
	// quoting a numeric id. Each is empty when the originator is not a device, the device could
	// not be read, or it has no such value (a device's name is optional); a subscriber omits
	// what is empty rather than inventing it. They are read at emission, not carried by the
	// alarm row, so they never make a transition fail.
	DeviceToken     string
	DeviceName      string
	DeviceTypeToken string
	// PositionDerived carries the alarm row's verdict; nil means unknown.
	PositionDerived *bool

	// SuppressedUntil and SuppressionWindowToken are LIVE suppression, evaluated at emission
	// wall time: the alarm matched the named alarm suppression window, which suppresses it
	// until SuppressedUntil. A notification consumer holds its page until then and re-checks.
	// Nil means not suppressed, or that the producer could not tell (evaluation failed, or it
	// predates these fields), and the consumer then pages as it always did.
	SuppressedUntil        *time.Time `json:",omitempty"`
	SuppressionWindowToken string     `json:",omitempty"`
	// RaisedDuringSuppressionToken is HISTORY, carried on RAISED only: the window the cycle
	// began under, evaluated at the cycle's event time. It never changes how a page is sent.
	RaisedDuringSuppressionToken string `json:",omitempty"`
}
