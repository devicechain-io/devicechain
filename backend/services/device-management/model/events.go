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
	OccurredTime        time.Time
	ProcessedTime       time.Time
	EventType           esmodel.EventType
	Payload             interface{}
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
