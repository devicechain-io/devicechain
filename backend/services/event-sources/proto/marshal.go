// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"google.golang.org/protobuf/proto"
)

// Every instant on this message is written in exactly one of two forms (eventtime.WireNanos):
// nanoseconds since the Unix epoch in an sfixed64 field, or, for an instant that count
// cannot hold, an RFC 3339 string in the field every instant was written in before. A
// reader takes the number when it is present and the string otherwise, which is also what
// reads a message written before the number existed.

// wireTime is an envelope instant in whichever form it takes: the count, or the string.
func wireTime(at time.Time) (*int64, string) {
	if nanos, ok := eventtime.WireNanos(at); ok {
		return &nanos, ""
	}
	return nil, eventtime.WireString(at)
}

// readWireTime reads an envelope instant back: the count when present, else the string.
// A message with neither fails, as a message with an unparseable string always has.
func readWireTime(nanos *int64, text string) (time.Time, error) {
	if nanos != nil {
		return eventtime.FromWireNanos(*nanos), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// marshalEntryTime renders one sample's own instant onto the wire, in one form or the
// other, or in neither when the device timed only the message. Absence is meaningful
// BEFORE resolution — it is what tells the resolver to fall back to the envelope — so it
// survives the round trip rather than being defaulted here.
func marshalEntryTime(occurred *time.Time) (*int64, *string) {
	if occurred == nil {
		return nil, nil
	}
	nanos, text := wireTime(*occurred)
	if nanos != nil {
		return nanos, nil
	}
	return nil, &text
}

// unmarshalEntryTime reads one sample's own instant back off the wire.
//
// 🔴 It FAILS rather than re-validating. This seam is internal: every producer on it
// went through the device-facing decoder, which already refused a malformed value, or
// is a platform adapter formatting a time it holds. So a value that will not parse here
// is a defect in this repository, not bad device input, and swallowing it would convert
// that defect into a batch silently collapsed onto one instant — the exact failure this
// whole change removes. The envelope's own time has always been handled this way
// (UnmarshalUnresolvedEvent below); this makes the entries match.
func unmarshalEntryTime(nanos *int64, raw *string) (*time.Time, error) {
	if nanos != nil {
		at := eventtime.FromWireNanos(*nanos)
		return &at, nil
	}
	if raw == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return nil, fmt.Errorf("entry occurredTime %q is not an RFC3339 timestamp: %w", *raw, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// readSeal reads a sealed credential value: the binary form, or the text form a message
// written before it carries, brought to the binary form (credential.SealedFromText). nil
// when the message carries neither.
func readSeal(binary []byte, text *string) []byte {
	if binary != nil {
		return binary
	}
	if text != nil {
		return credential.SealedFromText(*text)
	}
	return nil
}

// Marshal payload for a new relationship event.
func MarshalPayloadForNewRelationshipEvent(payload *model.UnresolvedNewRelationshipPayload) ([]byte, error) {
	pbna := &PUnresolvedNewRelationshipPayload{
		RelationshipType: payload.RelationshipType,
		TargetType:       payload.TargetType,
		Target:           payload.Target,
	}
	bytes, err := proto.Marshal(pbna)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for a locations event.
func MarshalPayloadForLocationsEvent(payload *model.UnresolvedLocationsPayload) ([]byte, error) {
	pbpayload := &PUnresolvedLocationsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime)
		pbentry := &PUnresolvedLocationEntry{
			Latitude:     entry.Latitude,
			Longitude:    entry.Longitude,
			Elevation:    entry.Elevation,
			Accuracy:     entry.Accuracy,
			Speed:        entry.Speed,
			Heading:      entry.Heading,
			Occurred:     nanos,
			OccurredTime: text,
		}
		pbpayload.Entries = append(pbpayload.Entries, pbentry)
	}
	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for a measurements event: the PUnresolvedMeasurementsPayload wire
// format, written directly rather than through proto.Marshal (see measurements_wire.go).
func MarshalPayloadForMeasurementsEvent(payload *model.UnresolvedMeasurementsPayload) ([]byte, error) {
	return marshalMeasurementsPayload(payload)
}

// Marshal payload for an alerts event.
func MarshalPayloadForAlertsEvent(payload *model.UnresolvedAlertsPayload) ([]byte, error) {
	pbpayload := &PUnresolvedAlertsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime)
		pbentry := &PUnresolvedAlertEntry{
			Type:         entry.Type,
			Level:        entry.Level,
			Message:      entry.Message,
			Source:       entry.Source,
			Occurred:     nanos,
			OccurredTime: text,
		}
		pbpayload.Entries = append(pbpayload.Entries, pbentry)
	}
	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for a state-change (presence) event.
func MarshalPayloadForStateChangeEvent(payload *model.UnresolvedStateChangePayload) ([]byte, error) {
	pbpayload := &PUnresolvedStateChangePayload{
		State:             string(payload.State),
		Reason:            payload.Reason,
		ExpectedSessionId: payload.ExpectedSessionId,
		SessionId:         payload.SessionId,
		OccurredTime:      payload.OccurredTime,
	}
	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Unmarshal a payload into a new relationship event.
func UnmarshalPayloadForNewRelationshipEvent(payload []byte) (*model.UnresolvedNewRelationshipPayload, error) {
	pbassn := &PUnresolvedNewRelationshipPayload{}
	err := proto.Unmarshal(payload, pbassn)
	if err != nil {
		return nil, err
	}
	return &model.UnresolvedNewRelationshipPayload{
		RelationshipType: pbassn.RelationshipType,
		TargetType:       pbassn.TargetType,
		Target:           pbassn.Target,
	}, nil
}

// Unmarshal a payload into a locations event.
func UnmarshalPayloadForLocationsEvent(encoded []byte) (*model.UnresolvedLocationsPayload, error) {
	pbpayload := &PUnresolvedLocationsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedLocationsPayload{}
	entries := make([]model.UnresolvedLocationEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime)
		if terr != nil {
			return nil, terr
		}
		entry := model.UnresolvedLocationEntry{
			Latitude:     pbentry.Latitude,
			Longitude:    pbentry.Longitude,
			Elevation:    pbentry.Elevation,
			Accuracy:     pbentry.Accuracy,
			Speed:        pbentry.Speed,
			Heading:      pbentry.Heading,
			OccurredTime: occurred,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into a measurements event.
func UnmarshalPayloadForMeasurementsEvent(encoded []byte) (*model.UnresolvedMeasurementsPayload, error) {
	pbpayload := &PUnresolvedMeasurementsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedMeasurementsPayload{}
	entries := make([]model.UnresolvedMeasurementsEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime)
		if terr != nil {
			return nil, terr
		}
		entry := model.UnresolvedMeasurementsEntry{
			Measurements: pbentry.Measurements,
			OccurredTime: occurred,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into an alerts event.
func UnmarshalPayloadForAlertsEvent(encoded []byte) (*model.UnresolvedAlertsPayload, error) {
	pbpayload := &PUnresolvedAlertsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedAlertsPayload{}
	entries := make([]model.UnresolvedAlertEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime)
		if terr != nil {
			return nil, terr
		}
		entry := model.UnresolvedAlertEntry{
			Type:         pbentry.Type,
			Level:        pbentry.Level,
			Message:      pbentry.Message,
			Source:       pbentry.Source,
			OccurredTime: occurred,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into a state-change (presence) event.
func UnmarshalPayloadForStateChangeEvent(encoded []byte) (*model.UnresolvedStateChangePayload, error) {
	pbpayload := &PUnresolvedStateChangePayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	return &model.UnresolvedStateChangePayload{
		State:             model.PresenceState(pbpayload.State),
		Reason:            pbpayload.Reason,
		ExpectedSessionId: pbpayload.ExpectedSessionId,
		SessionId:         pbpayload.SessionId,
		OccurredTime:      pbpayload.OccurredTime,
	}, nil
}

// Marshal unresolved payload based on event type.
func MarshalUnresolvedPayload(etype model.EventType, payload interface{}) ([]byte, error) {
	switch etype {
	case model.NewRelationship:
		if napayload, ok := payload.(*model.UnresolvedNewRelationshipPayload); ok {
			return MarshalPayloadForNewRelationshipEvent(napayload)
		}
		return nil, fmt.Errorf("invalid new-relationship payload: %+v", payload)
	case model.Location:
		if locpayload, ok := payload.(*model.UnresolvedLocationsPayload); ok {
			return MarshalPayloadForLocationsEvent(locpayload)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case model.Measurement:
		if mxpayload, ok := payload.(*model.UnresolvedMeasurementsPayload); ok {
			return MarshalPayloadForMeasurementsEvent(mxpayload)
		}
		return nil, fmt.Errorf("invalid measurement payload: %+v", payload)
	case model.Alert:
		if apayload, ok := payload.(*model.UnresolvedAlertsPayload); ok {
			return MarshalPayloadForAlertsEvent(apayload)
		}
		return nil, fmt.Errorf("invalid alert payload: %+v", payload)
	case model.StateChange:
		if scpayload, ok := payload.(*model.UnresolvedStateChangePayload); ok {
			return MarshalPayloadForStateChangeEvent(scpayload)
		}
		return nil, fmt.Errorf("invalid state-change payload: %+v", payload)
	default:
		return nil, fmt.Errorf("unable to marshal unresolved payload for event type: %s", etype.String())
	}
}

// Unmarshal unresolved payload based on event type.
func UnmarshalUnresolvedPayload(etype model.EventType, payload []byte) (interface{}, error) {
	switch etype {
	case model.NewRelationship:
		return UnmarshalPayloadForNewRelationshipEvent(payload)
	case model.Location:
		return UnmarshalPayloadForLocationsEvent(payload)
	case model.Measurement:
		return UnmarshalPayloadForMeasurementsEvent(payload)
	case model.Alert:
		return UnmarshalPayloadForAlertsEvent(payload)
	case model.StateChange:
		return UnmarshalPayloadForStateChangeEvent(payload)
	default:
		return nil, fmt.Errorf("unable to unmarshal unresolved payload for event type: %s", etype.String())
	}
}

// ErrUnsealedCredentialSecret is MarshalUnresolvedEvent's refusal of an event that still
// carries the credential secret as the device sent it.
var ErrUnsealedCredentialSecret = errors.New("refusing to marshal an event carrying an unsealed credential secret")

// ErrUnsealedCredentialId is MarshalUnresolvedEvent's refusal of an ACCESS_TOKEN event that
// still carries the token as the device sent it.
var ErrUnsealedCredentialId = errors.New("refusing to marshal an event carrying an unsealed access token")

// IsBearerCredentialType reports whether credentialType names a credential whose id is
// itself the bearer secret (ACCESS_TOKEN), which therefore travels sealed.
func IsBearerCredentialType(credentialType *string) bool {
	return credentialType != nil && strings.EqualFold(*credentialType, "ACCESS_TOKEN")
}

// Marshal an unresolved event to protobuf bytes.
//
// An event still carrying CredentialSecret, the secret as the device sent it, is refused:
// the stream keeps what is marshalled for its retention period, so the secret must be
// sealed into SealedCredentialSecret first (inboundEventMessage does). Dropping it
// silently would turn a missed seal into an event that fails to authenticate far from
// the cause.
func MarshalUnresolvedEvent(event *model.UnresolvedEvent) ([]byte, error) {
	if event.CredentialSecret != nil {
		return nil, ErrUnsealedCredentialSecret
	}
	// An access token is the credential itself: only its sealed form is marshalled.
	if IsBearerCredentialType(event.CredentialType) && event.CredentialId != nil {
		return nil, ErrUnsealedCredentialId
	}
	plbytes, err := MarshalUnresolvedPayload(event.EventType, event.Payload)
	if err != nil {
		return nil, err
	}

	// Encode protobuf event. Each instant in one form (wireTime), each seal in the binary
	// form only: the text fields of the seal are read, never written.
	occurred, occurredText := wireTime(event.OccurredTime)
	processed, processedText := wireTime(event.ProcessedTime)
	pbevent := &PUnresolvedEvent{
		SourceId:               event.Source,
		AltId:                  event.AltId,
		Device:                 event.Device,
		Relationship:           event.Relationship,
		Occurred:               occurred,
		OccurredTime:           occurredText,
		Processed:              processed,
		ProcessedTime:          processedText,
		EventType:              int64(event.EventType),
		Payload:                plbytes,
		CredentialType:         event.CredentialType,
		CredentialId:           event.CredentialId,
		SealedSecret:           event.SealedCredentialSecret,
		SealedToken:            event.SealedCredentialId,
		AuthenticatedTransport: event.AuthenticatedTransport,
	}

	// Marshal event to bytes.
	bytes, err := proto.Marshal(pbevent)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

// Unmarshal encoded unresolved event.
func UnmarshalUnresolvedEvent(encoded []byte) (*model.UnresolvedEvent, error) {
	// Unmarshal protobuf event.
	pbevent := &PUnresolvedEvent{}
	err := proto.Unmarshal(encoded, pbevent)
	if err != nil {
		return nil, err
	}

	// Decode event type.
	etype := model.EventType(pbevent.EventType)

	// Unmarshal payload.
	payload, err := UnmarshalUnresolvedPayload(etype, pbevent.Payload)
	if err != nil {
		return nil, err
	}

	occtime, err := readWireTime(pbevent.Occurred, pbevent.OccurredTime)
	if err != nil {
		return nil, err
	}
	proctime, err := readWireTime(pbevent.Processed, pbevent.ProcessedTime)
	if err != nil {
		return nil, err
	}
	event := &model.UnresolvedEvent{
		Source:                 pbevent.SourceId,
		AltId:                  pbevent.AltId,
		Device:                 pbevent.Device,
		Relationship:           pbevent.Relationship,
		OccurredTime:           occtime,
		ProcessedTime:          proctime,
		EventType:              etype,
		Payload:                payload,
		CredentialType:         pbevent.CredentialType,
		CredentialId:           pbevent.CredentialId,
		SealedCredentialSecret: readSeal(pbevent.SealedSecret, pbevent.CredentialSecret),
		SealedCredentialId:     readSeal(pbevent.SealedToken, pbevent.SealedCredentialId),
		AuthenticatedTransport: pbevent.AuthenticatedTransport,
	}

	return event, nil
}
