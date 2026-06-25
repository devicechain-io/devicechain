// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"
)

type EventType int64

// Enumeration of event types.
//
//go:generate stringer -type=EventType
const (
	NewRelationship EventType = iota
	Location
	Measurement
	Alert
	StateChange
	CommandInvocation
	CommandResponse
)

var EventTypesByName map[string]EventType

// Unresolved event details.
type UnresolvedEvent struct {
	Source        string
	AltId         *string
	Device        string
	Relationship  *string
	OccurredTime  time.Time
	ProcessedTime time.Time
	EventType     EventType
	Payload       interface{}

	// Credential presented by the connecting device (ADR-014). When set, the
	// downstream resolver authenticates the device against the credential store
	// rather than trusting the self-asserted Device token. CredentialSecret
	// carries the bearer secret (e.g. an MQTT password) when the credential type
	// requires one; it is nil when possession of the id is itself the proof.
	CredentialType   *string
	CredentialId     *string
	CredentialSecret *string
}

// Payload for creating a new relationship. The target is a uniform (type, token)
// reference (ADR-013): TargetType names an entity class and Target is its token.
type UnresolvedNewRelationshipPayload struct {
	RelationshipType string
	TargetType       string
	Target           string
}

// Information for a location entry.
type UnresolvedLocationEntry struct {
	Latitude     *string
	Longitude    *string
	Elevation    *string
	OccurredTime *string
}

// Payload creating new locations.
type UnresolvedLocationsPayload struct {
	Entries []UnresolvedLocationEntry
}

// Information for a measurements entry.
type UnresolvedMeasurementsEntry struct {
	Measurements map[string]string
	OccurredTime *string
}

// Payload creating new measurements.
type UnresolvedMeasurementsPayload struct {
	Entries []UnresolvedMeasurementsEntry
}

// Information for an alert entry.
type UnresolvedAlertEntry struct {
	Type         string
	Level        uint32
	Message      string
	Source       string
	OccurredTime *string
}

// Payload creating new alerts.
type UnresolvedAlertsPayload struct {
	Entries []UnresolvedAlertEntry
}

// Initializer.
func init() {
	EventTypesByName = make(map[string]EventType)
	EventTypesByName[NewRelationship.String()] = NewRelationship
	EventTypesByName[Location.String()] = Location
	EventTypesByName[Measurement.String()] = Measurement
	EventTypesByName[Alert.String()] = Alert
	EventTypesByName[StateChange.String()] = StateChange
	EventTypesByName[CommandInvocation.String()] = CommandInvocation
	EventTypesByName[CommandResponse.String()] = CommandResponse
}
