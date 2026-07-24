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

	// AuthenticatedTransport marks an event whose device was authenticated at the
	// TRANSPORT by a trusted internal ingest source (LwM2M DTLS-PSK / Sparkplug
	// broker), so it presents no per-event credential. The resolver then trusts the
	// self-asserted Device token under deviceAuthMode=required — the same trust the
	// 'disabled'/'optional' transports already grant, but confined to these sources.
	//
	// SAFE only because it is NOT device-forgeable: ADR-025 confines a device's NATS
	// publish to its own devices.{token}.events subject, and the device->inbound-events
	// gateway (event-sources JsonDecoder) copies only named payload fields — it has NO
	// field for this flag. It MUST therefore never be settable from device-controlled
	// input (see the decoder guard test). For LwM2M the Device token is bound to the
	// authenticated PSK identity (per-device); for Sparkplug it is topic-derived
	// (broker-level, NOT per-device — required no longer closes intra-tenant spoofing
	// for Sparkplug; a known, tracked gap).
	AuthenticatedTransport bool
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

// Presence state carried by a StateChange event (ADR-067). A closed enum: a
// connectivity transition, not a free-form status.
type PresenceState string

const (
	PresenceConnected    PresenceState = "CONNECTED"
	PresenceDisconnected PresenceState = "DISCONNECTED"
)

// Payload for a transport-level device presence transition (ADR-067). SessionId
// is a producer-supplied monotonic session id (a host-observed connect epoch, not
// e.g. a raw Sparkplug bdSeq). It rides the wire as a string so an epoch-sized
// value (UnixNano) survives a JSON decode without float64 precision loss; the
// resolver parses it to a uint64. Reason is descriptive metadata only, never an
// ordering or authorization input.
type UnresolvedStateChangePayload struct {
	State        PresenceState
	Reason       string
	SessionId    string
	OccurredTime *string
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
