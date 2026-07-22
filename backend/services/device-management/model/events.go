// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// Payload with resolved relationship info. The target is a uniform
// (type, id) reference (ADR-013).
type ResolvedNewRelationshipPayload struct {
	RelationshipTypeId uint64
	TargetType         *string
	TargetId           *uint64
}

// Entry with resolved location information.
type ResolvedLocationEntry struct {
	Latitude     *string
	Longitude    *string
	Elevation    *string
	OccurredTime *string
}

// Payload with resolved location entries.
type ResolvedLocationsPayload struct {
	Entries []ResolvedLocationEntry
}

// Entry with resolved info for a single measurement. Unit and DataType are
// denormalized from the bound metric definition (ADR-016) as of the profile's
// active published version — alongside Classifier — so the persisted measurement
// is self-describing on read without a cross-service hop back into
// device-management. Both are nil for an undeclared (unbound) measurement.
type ResolvedMeasurementEntry struct {
	Name       string
	Value      string
	Classifier *uint64
	Unit       *string
	DataType   *string
}

// Information for a measurements entry.
type ResolvedMeasurementsEntry struct {
	Entries      []ResolvedMeasurementEntry
	OccurredTime *string
}

// Payload with resolved measurement entries.
type ResolvedMeasurementsPayload struct {
	Entries []ResolvedMeasurementsEntry
}

// Information for an alert entry.
type ResolvedAlertEntry struct {
	Type         string
	Level        uint32
	Message      string
	Source       string
	OccurredTime *string
}

// Payload with resolved alert entries.
type ResolvedAlertsPayload struct {
	Entries []ResolvedAlertEntry
}

// Payload with a resolved device presence transition (ADR-067). State is a
// validated PresenceState (CONNECTED|DISCONNECTED); SessionId is the parsed
// producer-supplied monotonic session id (a host-observed connect epoch). The
// device-state projection applies the transition under a monotonic
// (SessionId, OccurredTime) guard (ADR-067 decision 4).
type ResolvedStateChangePayload struct {
	State        string
	Reason       string
	SessionId    uint64
	OccurredTime *string
}

// Event with token references resolved and the originating device's tracked
// relationships merged onto it as a set of anchors (ADR-013). The set is empty
// when the device has no tracked relationship (it still resolves and persists).
// The source device is carried as its stable per-tenant token (ADR-044): the
// numeric row id is internal to device-management and never crosses the seam to
// event-management / device-state.
type ResolvedEvent struct {
	Source            string
	AltId             *string
	SourceDeviceToken string
	Anchors           []ResolvedAnchor
	// DeviceTypeToken and ProfileVersionToken denormalize the device's rule-scoping
	// identity at resolve time (ADR-051): the device-type token and a
	// "{profileToken}@{version}" token naming the active published profile version
	// (ADR-045) whose rules apply. event-processing's DETECT engine selects the
	// applicable rules from these without a graph read back into device-management.
	// ProfileVersionToken is empty when the type has no profile or the profile is
	// unpublished (no active version) — the device has no resolvable rules.
	DeviceTypeToken     string
	ProfileVersionToken string
	// ExternalId denormalizes the reporting device's external id (ADR-049) at resolve
	// time, mirroring the ADR-016 unit/dataType denormalization onto measurements: the
	// device-state presence projection stores it so a consumer can key a device by its
	// transport-native identity (the Sparkplug "{group}/{node}[/{device}]" external id)
	// without a hop back into device-management. Empty when the device has no external id.
	ExternalId string
	// ScopeMemberships denormalizes, at resolve time, the rule-scoped dynamic-group
	// versions the reporting device AND its anchors currently belong to (ADR-062): the
	// union of MembershipsForEntity over the device and each anchor. event-processing's
	// DETECT engine treats a group-scoped rule as in-scope for this event iff its
	// {group}@{version} is present here — a set test on the replayed bytes, so scope is
	// replay-correct BY CONSTRUCTION (the membership is frozen into the immutable event
	// at the instant it happened, never re-queried on replay). Empty when no rule
	// references any group the device or its anchors belong to (the pay-nothing case).
	ScopeMemberships []GroupRef
	OccurredTime     time.Time
	ProcessedTime    time.Time
	EventType        esmodel.EventType
	Payload          interface{}
}

// GroupRef is one rule-scoped dynamic-group version an event is stamped as belonging
// to (ADR-062): the group's stable per-tenant token and the frozen selector version
// the membership was computed against. The engine's scope check is
// `group@v ∈ event.ScopeMemberships`.
type GroupRef struct {
	GroupToken string
	Version    int32
}

// ResolvedAnchor is one of a resolved event's anchors — a tracked relationship's
// target as a uniform (type, token) reference (ADR-013/044). The target is named
// by its stable per-tenant token so the reference survives across the service seam.
type ResolvedAnchor struct {
	AnchorType     string
	AnchorToken    string
	RelationshipId uint
}

// Captures failure information for events that could not be processed.
type FailedEvent struct {
	Reason  uint
	Service string
	Message string
	Error   string
	Payload []byte
}

// Create a new FailedEvent.
func NewFailedEvent(reason uint, service string, message string, err error, payload []byte) *FailedEvent {
	return &FailedEvent{
		Reason:  reason,
		Service: service,
		Message: message,
		Error:   err.Error(),
		Payload: payload,
	}
}
