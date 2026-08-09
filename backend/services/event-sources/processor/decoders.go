// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
)

const (
	DECODER_TYPE_JSON = "json"
)

// Payload expected for events passed in json format.
type JsonEvent struct {
	AltId        *string                `json:"altId,omitempty"`
	Device       string                 `json:"device"`
	Relationship *string                `json:"relationship,omitempty"`
	OccurredTime *string                `json:"occurredTime,omitempty"`
	EventType    string                 `json:"eventType"`
	Payload      map[string]interface{} `json:"payload"`

	// Credential presented by the connecting device (ADR-014), carried so the
	// resolver can authenticate the device rather than trusting the Device token.
	CredentialType   *string `json:"credentialType,omitempty"`
	CredentialId     *string `json:"credentialId,omitempty"`
	CredentialSecret *string `json:"credentialSecret,omitempty"`
}

// Interface implemented by all decoders.
type Decoder interface {
	// Decodes a binary payload into an event.
	Decode(payload []byte) (*model.UnresolvedEvent, interface{}, error)
}

// Create a new decoder based on the given type indicator.
func NewDecoderForType(decodetype string, config map[string]string) (Decoder, error) {
	switch decodetype {
	case DECODER_TYPE_JSON:
		return NewJsonDecoder(config), nil
	default:
		return nil, fmt.Errorf("Unknown decoder type: %s", decodetype)
	}
}

// Decodes payloads that use json format.
type JsonDecoder struct {
	Configuration map[string]string
}

// Create a new json decoder.
func NewJsonDecoder(config map[string]string) *JsonDecoder {
	return &JsonDecoder{
		Configuration: config,
	}
}

// Builds a new relationship payload from the json content. The target is a
// uniform (type, token) reference (ADR-013).
func (jd *JsonDecoder) BuildNewRelationshipPayload(source *JsonEvent) (*model.UnresolvedNewRelationshipPayload, error) {
	payload := &model.UnresolvedNewRelationshipPayload{}
	if rt, ok := source.Payload["relationshipType"]; ok {
		payload.RelationshipType = fmt.Sprintf("%v", rt)
	}
	if ttype, ok := source.Payload["targetType"]; ok {
		payload.TargetType = fmt.Sprintf("%v", ttype)
	}
	if target, ok := source.Payload["target"]; ok {
		payload.Target = fmt.Sprintf("%v", target)
	}

	return payload, nil
}

// Bounds for the coordinate contract (ADR-078 d.3/d.4b). Latitude and longitude
// are the WGS84 degree ranges. The remaining three are the widths of the columns
// they land in, expressed as a magnitude ceiling: exceeding one is a units bug,
// and the value must be refused HERE rather than at the INSERT.
//
// The reason is a measured failure, not tidiness. A device sending degrees x 1e7
// (the NMEA/LwM2M convention) overflows decimal(10,8) with SQLSTATE 22003, which
// the persistence layer returns unwrapped — so the dispatch switch falls through
// to its retry default, the event burns its whole MaxDeliver budget, and it is
// then misfiled as a downstream API failure rather than as invalid data. A decode
// error, by contrast, is TERMINAL on every transport by construction: HTTP answers
// 400 and the broker path routes to dead-letter and settles. Rejecting at the door
// is what makes this deterministic without touching the classifier at all.
//
// Widening the columns so the value fits was rejected: it would let 337490000 be
// STORED as a latitude, converting a loud retry loop into silent corruption.
const (
	maxAbsLatitude  = 90.0
	maxAbsLongitude = 180.0
	// elevation, accuracy and speed are all decimal(12,4) — precision 12, scale 4,
	// so EIGHT digits before the point and a true ceiling of 99999999.9999.
	//
	// The bound is deliberately a whole metre inside that rather than at it. Excess
	// SCALE rounds silently where excess integer digits raise, so a value like
	// 99999999.99999 would pass a check written at the exact ceiling and then round
	// UP to 100000000.0000 at the column — nine integer digits, and the overflow
	// this whole check exists to prevent, reintroduced by the check itself.
	maxAbsMetres = 99_999_999.0
	// heading is decimal(7,4) and is a compass bearing, so its bound is semantic —
	// [0, 360), because 360 is the same bearing as 0 and storing both spellings of
	// north is how a consumer ends up with two headings that compare unequal.
	//
	// 🔴 But the bound has to be checked one rounding-quantum INSIDE 360, not at it,
	// and this is the same trap as the metre ceiling above rather than a second one.
	// The check runs on the parsed float; the column stores 4 decimal places and
	// ROUNDS to get there. So "359.99999" is below 360 as a float, passes an
	// at-the-boundary check, and is then stored as exactly 360.0000 — the value the
	// exclusive bound exists to refuse, reintroduced by the check itself. Measured
	// on PostgreSQL 17: 359.99995 and above round to 360.0000, and decimal(7,4) has
	// room to 999.9999 so nothing downstream refuses it either.
	//
	// The bound is therefore 360 minus half a quantum. Anything that would round to
	// 360.0000 is refused; 359.9999 and below still store as themselves.
	headingQuantum      = 0.0001 // decimal(7,4)
	maxHeadingExclusive = 360.0 - headingQuantum/2
)

// Range-checks one decoded location entry against the coordinate contract.
//
// Latitude and longitude are REQUIRED. This is not pedantry about a missing key:
// Go's json decoding leaves unmatched fields nil, so an entry that misspells
// "latitude" as "lat" unmarshals cleanly into an entry holding NOTHING, and
// without this check it would resolve, persist a row with no position, and be
// acked to the device as success — the same fail-open the entries wrapper has,
// one level further in.
func validateLocationEntry(index int, entry *model.UnresolvedLocationEntry) error {
	type bound struct {
		name     string
		value    *string
		required bool
		min, max float64
		// maxExclusive treats max as an open bound, for heading's [0, 360).
		maxExclusive bool
	}
	for _, b := range []bound{
		{name: "latitude", value: entry.Latitude, required: true, min: -maxAbsLatitude, max: maxAbsLatitude},
		{name: "longitude", value: entry.Longitude, required: true, min: -maxAbsLongitude, max: maxAbsLongitude},
		{name: "elevation", value: entry.Elevation, min: -maxAbsMetres, max: maxAbsMetres},
		{name: "accuracy", value: entry.Accuracy, min: 0, max: maxAbsMetres},
		{name: "speed", value: entry.Speed, min: 0, max: maxAbsMetres},
		{name: "heading", value: entry.Heading, min: 0, max: maxHeadingExclusive, maxExclusive: true},
	} {
		if b.value == nil {
			if b.required {
				return fmt.Errorf("location entry %d is missing required field %q", index, b.name)
			}
			continue
		}
		parsed, err := strconv.ParseFloat(*b.value, 64)
		if err != nil {
			return fmt.Errorf("location entry %d field %q is not a number: %q", index, b.name, *b.value)
		}
		// NaN fails every comparison below, so it would pass a naive range check.
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("location entry %d field %q is not a finite number: %q", index, b.name, *b.value)
		}
		tooBig := parsed > b.max
		if b.maxExclusive {
			tooBig = parsed >= b.max
		}
		if parsed < b.min || tooBig {
			return fmt.Errorf("location entry %d field %q is out of range: %q", index, b.name, *b.value)
		}
	}
	return nil
}

// errNoEntries is the fail-closed rejection shared by all three entry-carrying
// payloads.
//
// 🔴 All three decoders have the SAME fail-open shape and only location was found
// by investigation. Each unmarshals the nested payload object into a struct whose
// only field is Entries, so a body that puts its content directly under "payload"
// with no entries wrapper decodes SUCCESSFULLY with zero entries, resolves,
// persists nothing, and is acked to the device as 202. Measured on all three.
//
// Location was the one that had never been used, so its wrong shape survived in
// fixtures. Measurement is worse: it is the path every SDK, simulator and adapter
// actually takes, and the flat form was sitting in this package's own tests —
// including the one that calls itself "a well-formed device Measurement event" and
// the one named DecodeSuccess, which asserted 202 for a body that stored nothing.
// That is how a wrong shape teaches itself to the next reader.
//
// The published wire contract has always shown the nested form for measurements,
// so nothing outside this repository is promised the flat one, and no in-repo
// producer emits it. Fixing all three together is the point: leaving the identical
// defect one function away, on the more heavily used path, is how it comes back.
//
// This helper covers only the WRAPPER. Each caller also rejects an entry that is
// present but empty, because that is the same silent success one level in — see the
// per-entry checks in the three Build*Payload functions below. The wrapper check
// alone would still ack `{"entries":[{}]}` as 202.
func errNoEntries(kind, example string) error {
	return fmt.Errorf("%s payload carries no entries: content belongs in payload.entries[], as %s", kind, example)
}

// Parses a locations event.
//
// 🔴 This FAILS CLOSED, and that is the whole point of the function. The nested
// payload object unmarshals into a struct whose only field is Entries, so a body
// that puts coordinates at the top of "payload" with no entries wrapper decodes
// SUCCESSFULLY with zero entries, resolves, persists nothing, and is acked to the
// device as 202. Measured before the fix, and both gateway fixtures in this
// package used that wrong shape — which is how the next person would have learned
// it. The canonical shape is:
//
//	"payload": {"entries": [{"latitude": "33.749", "longitude": "-84.388"}]}
//
// A location payload yielding zero entries is therefore a decode error. Nothing in
// production emits location today, so there is no traffic to break, and this is
// the last moment the fix is free.
func (jd *JsonDecoder) BuildLocationsPayload(source *JsonEvent) (*model.UnresolvedLocationsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedLocationsPayload{}
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
	if len(payload.Entries) == 0 {
		return nil, errNoEntries("location", `{"entries":[{"latitude":"...","longitude":"..."}]}`)
	}
	for i := range payload.Entries {
		if err := validateLocationEntry(i, &payload.Entries[i]); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// Parses a measurements event.
func (jd *JsonDecoder) BuildMeasurementsPayload(source *JsonEvent) (*model.UnresolvedMeasurementsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedMeasurementsPayload{}
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
	if len(payload.Entries) == 0 {
		return nil, errNoEntries("measurement", `{"entries":[{"measurements":{"temperature":"21.5"}}]}`)
	}
	// The entries wrapper alone is only half the check. An entry carrying no
	// measurements is the SAME silent success one level in: it persists nothing and
	// is acked 202, so a device that sends `{"entries":[{}]}` — which is exactly what
	// an SDK emitting an empty reading set produces — is told its data landed.
	for i := range payload.Entries {
		if len(payload.Entries[i].Measurements) == 0 {
			return nil, fmt.Errorf("measurement entry %d carries no measurements", i)
		}
	}
	return payload, nil
}

// Parses an alerts event.
func (jd *JsonDecoder) BuildAlertsPayload(source *JsonEvent) (*model.UnresolvedAlertsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedAlertsPayload{}
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
	if len(payload.Entries) == 0 {
		return nil, errNoEntries("alert", `{"entries":[{"type":"...","level":1,"message":"...","source":"..."}]}`)
	}
	// An empty alert entry is worse than an empty measurement one: it does not merely
	// store nothing, it STORES A ROW with an empty type, message and source at level
	// 0. Type is the classifier every consumer keys on — a notification policy, a
	// rule, a console filter — so an untyped alert is a row nothing can route and
	// nobody can search for. Message and source stay optional: a blank message is
	// unhelpful, not corrupt.
	for i := range payload.Entries {
		if payload.Entries[i].Type == "" {
			return nil, fmt.Errorf("alert entry %d has no type: an alert must carry the classifier consumers route on", i)
		}
	}
	return payload, nil
}

// Parse json event payload.
func (jd *JsonDecoder) ParseEvent(payload []byte) (*JsonEvent, error) {
	jevent := &JsonEvent{}
	err := json.Unmarshal(payload, jevent)
	if err != nil {
		return nil, err
	}
	return jevent, nil
}

// Assemble an event based on json event data.
func (jd *JsonDecoder) AssembleEvent(jevent *JsonEvent) (*model.UnresolvedEvent, error) {
	event := &model.UnresolvedEvent{
		AltId:            jevent.AltId,
		Device:           jevent.Device,
		Relationship:     jevent.Relationship,
		CredentialType:   jevent.CredentialType,
		CredentialId:     jevent.CredentialId,
		CredentialSecret: jevent.CredentialSecret,
	}
	if etype, ok := model.EventTypesByName[jevent.EventType]; ok {
		event.EventType = etype
	} else {
		return nil, fmt.Errorf("unknown event type in json payload: %s", jevent.EventType)
	}
	if jevent.OccurredTime != nil {
		otime, err := time.Parse(time.RFC3339, *jevent.OccurredTime)
		if err != nil {
			return nil, err
		}
		event.OccurredTime = otime
	} else {
		event.OccurredTime = time.Now()
	}
	event.ProcessedTime = time.Now()
	return event, nil
}

// Decode a json payload into an event.
func (jd *JsonDecoder) Decode(payload []byte) (*model.UnresolvedEvent, interface{}, error) {
	// Parse json payload.
	jevent, err := jd.ParseEvent(payload)
	if err != nil {
		return nil, nil, err
	}
	// Assemble event from json data.
	event, err := jd.AssembleEvent(jevent)
	if err != nil {
		return nil, nil, err
	}

	// Create payload based on event type.
	switch event.EventType {
	case model.NewRelationship:
		payload, err := jd.BuildNewRelationshipPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Location:
		payload, err := jd.BuildLocationsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Measurement:
		payload, err := jd.BuildMeasurementsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Alert:
		payload, err := jd.BuildAlertsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.StateChange:
		// StateChange (presence, ADR-067) is a PLATFORM-PRODUCER event: a presence-
		// asserting adapter (the Sparkplug host) emits it directly over the wire
		// contract (proto), never through this device-facing JSON decoder. Accepting it
		// here would let any device credential forge its own presence — assert itself
		// permanently CONNECTED with an unbeatable session id, which the projection can
		// never supersede (the sweep skips ASSERTED and no data event flips it), hiding
		// the device's own death from monitoring. Reject it as an unsupported device event.
		return nil, nil, fmt.Errorf("state-change (presence) events are platform-produced and not accepted from device ingest")
	}

	return nil, nil, fmt.Errorf("unhandled event type: %s", jevent.EventType)
}
