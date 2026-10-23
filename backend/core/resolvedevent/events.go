// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package resolvedevent is the wire contract of the resolved-event stream: the envelope
// device-management publishes once per resolved event, its per-event-type payloads, and the
// failed-event envelope, with the protobuf codec for each (marshal.go, and resolved_wire.go,
// the hand-written fast path for the envelope). It lives in core so the consumers of the
// stream (event-management, device-state, event-processing, notification-management, ...)
// read it without importing the producer, and so the producer cannot drift from them: both
// sides compile against this one definition.
//
// The generated code (dc-resolved-events.pb.go) shares its protobuf package with
// device-management's own events file, so no message name, and no byte on the wire,
// differs from when both lived there. main_wire_golden_test.go holds the bytes of that
// time against today's marshaller.
package resolvedevent

import (
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/presence"
)

// Payload with resolved relationship info. The target is a uniform
// (type, id) reference (ADR-013).
type ResolvedNewRelationshipPayload struct {
	RelationshipTypeId uint64
	TargetType         *string
	TargetId           *uint64
}

// Entry with resolved location information. The coordinate contract is fixed
// platform-wide and never per-device (ADR-078 d.4b): WGS84 / EPSG:4326 decimal
// degrees, elevation and accuracy in metres (elevation above the ELLIPSOID, not
// mean sea level), speed in metres per second, heading in degrees clockwise from
// true north in [0, 360). Values arrive as strings and have already been
// range-checked at decode, so nothing downstream re-validates them.
//
// OccurredTime is THIS fix's instant and is ALWAYS SET — see the note on
// ResolvedMeasurementsEntry.OccurredTime, which is the same field for the same
// reason.
type ResolvedLocationEntry struct {
	Latitude     *string
	Longitude    *string
	Elevation    *string
	Accuracy     *string
	Speed        *string
	Heading      *string
	OccurredTime time.Time
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
	// Derived marks a CALCULATED metric's value (ADR-082): computed by the resolver from
	// the other entries of the same sample under the event's profile version, never sent
	// by the device. Every consumer stores, projects and detects on it like any other
	// entry; only event identity treats it differently (IdentityView).
	//
	// 🔴 omitempty IS LOAD-BEARING. This struct has no JSON tags, and event-management
	// derives the event id from json.Marshal of the payload, so a field that always
	// marshalled would move the id of every event ever stored. With omitempty a raw entry
	// marshals exactly as it did before the field existed, and IdentityView drops derived
	// entries before the payload is hashed, so the flag never reaches an id either way.
	Derived bool `json:",omitempty"`
}

// Information for a measurements entry — ONE sample, a coherent set of named
// readings taken at one instant.
//
// 🔴 OccurredTime IS ALWAYS SET, AND IT IS ALREADY BOUNDED. It is not a pointer and
// there is no fallback to interpret: the resolver resolved the entry-versus-envelope
// rule and applied the future-skew bound ONCE, in eventtime.ForEntry, so this is the
// instant every surface must report for this sample. A consumer READS it; nothing
// downstream re-derives it, re-clamps it, or falls back to the envelope.
//
// That absoluteness is the fix. When each consumer decided for itself, five of them
// gave three different answers — a dashboard's "latest" card could disagree with its
// own history chart about the same reading, and a replay preview clamped differently
// from live detection, which is the replay-correctness property the authoring canvas
// rests on.
type ResolvedMeasurementsEntry struct {
	Entries      []ResolvedMeasurementEntry
	OccurredTime time.Time
}

// Payload with resolved measurement entries.
type ResolvedMeasurementsPayload struct {
	Entries []ResolvedMeasurementsEntry
}

// IdentityView is the payload as event identity sees it: every Derived entry removed
// (ADR-082 decision 5). A calculated value is a function of the raw values and of the
// profile version that computed it, so leaving it in the event id would give one device
// reading two ids whenever a formula is published between two copies of it — a device-level
// duplicate (a QoS 1 resend, an edge spool retransmit, every LwM2M and Sparkplug event, none
// of which set an alternate id) would then store twice. Excluded, the id is a function of
// what the device sent alone.
//
// A payload with no derived entry — every payload of a profile without formulas — is
// returned AS IS, without allocating, so it hashes byte-identically to before calculated
// metrics existed. Otherwise a copy is returned and the receiver is not modified; a sample
// keeps its place (and its time) even if every entry in it was derived, so the remaining
// structure is exactly what the same reading would have resolved to with no formulas.
func (p *ResolvedMeasurementsPayload) IdentityView() *ResolvedMeasurementsPayload {
	if p == nil || !p.hasDerived() {
		return p
	}
	view := &ResolvedMeasurementsPayload{Entries: make([]ResolvedMeasurementsEntry, len(p.Entries))}
	for i, sample := range p.Entries {
		raw := make([]ResolvedMeasurementEntry, 0, len(sample.Entries))
		for _, mx := range sample.Entries {
			if !mx.Derived {
				raw = append(raw, mx)
			}
		}
		view.Entries[i] = ResolvedMeasurementsEntry{Entries: raw, OccurredTime: sample.OccurredTime}
	}
	return view
}

// IdentityPayload makes the payload an eventidentity.Viewer, so event-management can hash its
// identity view without importing this package. It is IdentityView, by value: a value and a
// pointer both reach it, which is how callers hand the payload over.
func (p ResolvedMeasurementsPayload) IdentityPayload() any { return p.IdentityView() }

var _ eventidentity.Viewer = ResolvedMeasurementsPayload{}

// hasDerived reports whether any entry of the payload is Derived.
func (p *ResolvedMeasurementsPayload) hasDerived() bool {
	for i := range p.Entries {
		for j := range p.Entries[i].Entries {
			if p.Entries[i].Entries[j].Derived {
				return true
			}
		}
	}
	return false
}

// Information for an alert entry. OccurredTime is this alert's instant and is
// ALWAYS SET — see the note on ResolvedMeasurementsEntry.OccurredTime.
type ResolvedAlertEntry struct {
	Type         string
	Level        uint32
	Message      string
	Source       string
	OccurredTime time.Time
}

// Payload with resolved alert entries.
type ResolvedAlertsPayload struct {
	Entries []ResolvedAlertEntry
}

// Payload with a resolved device presence transition (ADR-067). State is a validated
// presence.State (CONNECTED|DISCONNECTED|DEMOTED) — read it through Claim() below and
// never by comparing it to one value, because DEMOTED is not a connectivity claim at all
// and a comparison answers "is this device up?" for it anyway. SessionId is the parsed
// producer-supplied monotonic session id (a host-observed connect epoch). The
// device-state projection applies the transition under a monotonic
// (SessionId, OccurredTime) guard (ADR-067 decision 4).
type ResolvedStateChangePayload struct {
	State  string
	Reason string
	// ExpectedSessionId is the parsed compare-and-set precondition, zero when the
	// producer made no claim. Both presence consumers hand it to presence.Decide, which
	// admits a LOWER SessionId only when this matches the session they currently hold.
	ExpectedSessionId uint64
	SessionId         uint64
	OccurredTime      *string
}

// Claim maps the wire state onto the presence claim that presence.Decide consumes. It
// is the ONE implementation for BOTH consumers — the device-state projection and the
// DETECT engine — which is the whole point: they are the two halves presence.Decide
// exists to keep in lockstep, and each was deriving the claim from its own hand-written
// copy of the same string compare.
//
// 🔑 IT REPORTS ITS OWN FAILURE, and that is why it replaced the compare. `state ==
// "CONNECTED"` is a total function onto a bool, so it answers "is this device up?" for
// every input including the inputs that are not about the device being up. It read an
// unrecognised state as a DISCONNECT — a death — and it had no way to express DEMOTED,
// which asserts nothing about connectivity at all. A false ok is a malformed payload
// and the caller must dispose of it as one, never guess.
func (p *ResolvedStateChangePayload) Claim() (presence.Claim, bool) {
	switch p.State {
	case string(presence.StateConnected):
		return presence.ClaimConnected, true
	case string(presence.StateDisconnected):
		return presence.ClaimDisconnected, true
	case string(presence.StateDemoted):
		return presence.ClaimDemoted, true
	default:
		return presence.ClaimUnset, false
	}
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
	// FenceSetVersion denormalizes, at resolve time, the tenant's ACTIVE geofence-set
	// version (ADR-078) — the version whose frozen snapshot names the fences that were
	// live at the instant this event happened. Geofence containment is therefore
	// replay-correct BY CONSTRUCTION, the same way ScopeMemberships makes group scope
	// replay-correct: the fence set is frozen into the immutable event, never re-queried
	// against whatever fences exist at replay time.
	//
	// 🔴 STAMPED ON LOCATION EVENTS ONLY, and that restriction is the point rather than an
	// optimization. Nothing but a position can enter or leave a fence, so stamping a
	// measurement or a presence transition would put a version on rows that can never use
	// it — and once such a row exists, the next reader has to guess whether the field is
	// meaningful there. 0 on a location event means the tenant had never created a fence.
	FenceSetVersion int32
	// SourceDeviceDisabled is true when the device was disabled at the instant the event
	// was resolved. Only a presence transition is ever resolved for a disabled device (see
	// EventResolver.ResolveEvent): a CONNECTED, DISCONNECTED or DEMOTED claim is made by
	// the platform's own transport about a connection the platform itself holds, so it
	// still moves the device's presence and is kept in its connection history. Every other
	// event from a disabled device is refused before it is resolved.
	//
	// 🔴 A consumer that acts ON THE DEVICE'S BEHALF must skip an event carrying it.
	// Detection is the one today: a connectivity rule fed a quarantined device's
	// disconnect would raise an alarm for a device the operator has cut off, which is what
	// disabling it promises will not happen. It is frozen into the event like
	// ScopeMemberships, so a replay skips exactly what the live path skipped.
	SourceDeviceDisabled bool
	// GatewayToken is the token of the gateway device the event arrived through, when it did:
	// the broker authenticated that gateway, the subject named SourceDeviceToken as its child,
	// and the device's gateway assignment named that gateway when the event was resolved.
	// SourceDeviceToken remains the device that produced the event (attribution is to the
	// child); this records who carried it, so a stored event can be traced to the gateway that
	// vouched for it. Empty for an event that did not come through a gateway, and then absent
	// from the wire.
	GatewayToken  string
	OccurredTime  time.Time
	ProcessedTime time.Time
	EventType     eventtype.EventType
	Payload       interface{}
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
