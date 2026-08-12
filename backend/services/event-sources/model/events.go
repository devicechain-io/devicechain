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

// Information for a location entry. A fix is the full GPS vocabulary, not the
// minimum: position plus the quality and motion a device already knows.
//
// The coordinate contract is fixed platform-wide and is NEVER per-device
// (ADR-078 d.4b): Latitude/Longitude are WGS84 / EPSG:4326 decimal degrees,
// Elevation is metres above the WGS84 ELLIPSOID (not above mean sea level),
// Accuracy is horizontal metres, Speed is metres per second, and Heading is
// degrees clockwise from true north in [0, 360). A device whose sensor reports
// MSL converts before sending. Letting the profile declare a datum instead was
// rejected because getting it wrong is SILENT spatial error — a position drawn
// confidently in the wrong place — and ellipsoid-vs-geoid is tens of metres,
// comfortably enough to put a machine on the wrong side of a geofence.
//
// Every field is a JSON STRING, including the numeric ones, matching the
// measurement convention: a bare `"latitude": 33.749` fails the whole decode.
// Latitude and Longitude are required; the rest are optional. All of it is
// range-checked at decode (see validateLocationEntry) rather than at the
// database, so a units bug is rejected as BAD DATA on first delivery instead of
// surfacing as an unclassified Postgres overflow that retries to MaxDeliver.
//
// Speed and Heading are stored as REPORTED and are never recomputed or
// reconciled against consecutive fixes. The two can legitimately disagree — a
// stationary device with a noisy compass still reports a heading, and a device
// that batches or drops fixes reports a speed no pair of stored positions would
// reproduce. A consumer needing a value it can defend derives it itself.
// OccurredTime is THIS fix's own instant, nil when the device timed only the
// message. It is a parsed time rather than the raw string because this struct is
// only ever built past the untrusted edge: the JSON decoder rejects a malformed
// value outright (a device gets a 400 / dead-letter naming the offending entry),
// and the proto seam fails loudly rather than re-validating. Everything downstream
// therefore has a time or a documented absence, never a string that might not be
// one.
type UnresolvedLocationEntry struct {
	Latitude     *string
	Longitude    *string
	Elevation    *string
	Accuracy     *string
	Speed        *string
	Heading      *string
	OccurredTime *time.Time
}

// Payload creating new locations.
type UnresolvedLocationsPayload struct {
	Entries []UnresolvedLocationEntry
}

// Information for a measurements entry — ONE sample, a coherent set of named
// readings taken at one instant. OccurredTime is that instant, nil when the device
// timed only the message; see UnresolvedLocationEntry for why it is already parsed.
//
// A batch of these is how a store-and-forward device (and every Sparkplug/LwM2M
// upload) reports buffered history, so the per-entry time is the difference between
// a minute of readings stored as a minute of readings and all of them stored at one
// instant.
type UnresolvedMeasurementsEntry struct {
	Measurements map[string]string
	OccurredTime *time.Time
}

// Payload creating new measurements.
type UnresolvedMeasurementsPayload struct {
	Entries []UnresolvedMeasurementsEntry
}

// Information for an alert entry. OccurredTime is this alert's own instant, nil
// when the device timed only the message; see UnresolvedLocationEntry for why it is
// already parsed.
type UnresolvedAlertEntry struct {
	Type         string
	Level        uint32
	Message      string
	Source       string
	OccurredTime *time.Time
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
