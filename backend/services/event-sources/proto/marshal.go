// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/presence"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"

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

// What a producer WRITES is chosen by its wire format (eventtime.WireFormat): WireCompact
// writes the count for every instant it can hold and the seal in binary; WireLegacy writes
// exactly what the release before the count wrote, every instant as a string and every
// seal in the text form, so a consumer still on that release reads it. What a reader
// takes does not depend on the format: both, always.

// wireTime is an envelope instant in the form format writes it: the count, or the string.
//
// The legacy string keeps the instant's own offset, as the release before wrote it (the
// envelope was never normalized to UTC; the entries were). The compact form's string, for
// an instant the count cannot hold, is in UTC where RFC 3339 can carry it there
// (eventtime.AppendWireString).
func wireTime(at time.Time, format eventtime.WireFormat) (*int64, string) {
	if format == eventtime.WireLegacy {
		return nil, string(eventtime.AppendOwnOffsetWireString(nil, at))
	}
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
	return eventtime.ParseWireString(text)
}

// marshalEntryTime renders one sample's own instant onto the wire, in one form or the
// other, or in neither when the device timed only the message. Absence is meaningful
// BEFORE resolution — it is what tells the resolver to fall back to the envelope — so it
// survives the round trip rather than being defaulted here.
//
// An entry's string is in UTC in both formats, as it always was, unless UTC cannot carry it
// (eventtime.AppendWireString).
func marshalEntryTime(occurred *time.Time, format eventtime.WireFormat) (*int64, *string) {
	if occurred == nil {
		return nil, nil
	}
	if nanos, ok := eventtime.WireNanos(*occurred); ok && format == eventtime.WireCompact {
		return &nanos, nil
	}
	text := eventtime.WireString(*occurred)
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
	parsed, err := eventtime.ParseWireString(*raw)
	if err != nil {
		return nil, fmt.Errorf("entry occurredTime %q is not an RFC3339 timestamp: %w", *raw, err)
	}
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

// sealAsText is a binary seal in the text form the release before the binary form wrote
// (credential.SealedText), or nil when there is no seal.
func sealAsText(sealed []byte) (*string, error) {
	if sealed == nil {
		return nil, nil
	}
	text, err := credential.SealedText(sealed)
	if err != nil {
		return nil, err
	}
	return &text, nil
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
func MarshalPayloadForLocationsEvent(payload *model.UnresolvedLocationsPayload, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	pbpayload := &PUnresolvedLocationsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime, format)
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
func MarshalPayloadForMeasurementsEvent(payload *model.UnresolvedMeasurementsPayload, format eventtime.WireFormat) ([]byte, error) {
	return marshalMeasurementsPayload(payload, format)
}

// Marshal payload for an alerts event.
func MarshalPayloadForAlertsEvent(payload *model.UnresolvedAlertsPayload, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	pbpayload := &PUnresolvedAlertsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime, format)
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
		State:             presence.State(pbpayload.State),
		Reason:            pbpayload.Reason,
		ExpectedSessionId: pbpayload.ExpectedSessionId,
		SessionId:         pbpayload.SessionId,
		OccurredTime:      pbpayload.OccurredTime,
	}, nil
}

// Marshal unresolved payload based on event type, its instants in format.
func MarshalUnresolvedPayload(etype eventtype.EventType, payload interface{}, format eventtime.WireFormat) ([]byte, error) {
	switch etype {
	case eventtype.NewRelationship:
		if napayload, ok := payload.(*model.UnresolvedNewRelationshipPayload); ok {
			return MarshalPayloadForNewRelationshipEvent(napayload)
		}
		return nil, fmt.Errorf("invalid new-relationship payload: %+v", payload)
	case eventtype.Location:
		if locpayload, ok := payload.(*model.UnresolvedLocationsPayload); ok {
			return MarshalPayloadForLocationsEvent(locpayload, format)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case eventtype.Measurement:
		if mxpayload, ok := payload.(*model.UnresolvedMeasurementsPayload); ok {
			return MarshalPayloadForMeasurementsEvent(mxpayload, format)
		}
		return nil, fmt.Errorf("invalid measurement payload: %+v", payload)
	case eventtype.Alert:
		if apayload, ok := payload.(*model.UnresolvedAlertsPayload); ok {
			return MarshalPayloadForAlertsEvent(apayload, format)
		}
		return nil, fmt.Errorf("invalid alert payload: %+v", payload)
	case eventtype.StateChange:
		if scpayload, ok := payload.(*model.UnresolvedStateChangePayload); ok {
			return MarshalPayloadForStateChangeEvent(scpayload)
		}
		return nil, fmt.Errorf("invalid state-change payload: %+v", payload)
	default:
		return nil, fmt.Errorf("unable to marshal unresolved payload for event type: %s", etype.String())
	}
}

// Unmarshal unresolved payload based on event type.
func UnmarshalUnresolvedPayload(etype eventtype.EventType, payload []byte) (interface{}, error) {
	switch etype {
	case eventtype.NewRelationship:
		return UnmarshalPayloadForNewRelationshipEvent(payload)
	case eventtype.Location:
		return UnmarshalPayloadForLocationsEvent(payload)
	case eventtype.Measurement:
		return UnmarshalPayloadForMeasurementsEvent(payload)
	case eventtype.Alert:
		return UnmarshalPayloadForAlertsEvent(payload)
	case eventtype.StateChange:
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
//
// It writes the format this process is configured to write (eventtime.ProducerWireFormat,
// from infrastructure.messaging.wireFormat), which every producer of the message shares.
func MarshalUnresolvedEvent(event *model.UnresolvedEvent) ([]byte, error) {
	return MarshalUnresolvedEventAs(event, eventtime.ProducerWireFormat())
}

// MarshalUnresolvedEventAs is MarshalUnresolvedEvent in an explicit format. An unknown
// format, the zero value included, is refused.
func MarshalUnresolvedEventAs(event *model.UnresolvedEvent, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	if event.CredentialSecret != nil {
		return nil, ErrUnsealedCredentialSecret
	}
	// An access token is the credential itself: only its sealed form is marshalled.
	if IsBearerCredentialType(event.CredentialType) && event.CredentialId != nil {
		return nil, ErrUnsealedCredentialId
	}
	plbytes, err := MarshalUnresolvedPayload(event.EventType, event.Payload, format)
	if err != nil {
		return nil, err
	}

	// Encode protobuf event. Each instant in one form (wireTime). Each seal in the form
	// format writes: binary in its own fields, or, for WireLegacy, the text form in the
	// fields the release before wrote it in, converted without re-sealing.
	occurred, occurredText := wireTime(event.OccurredTime, format)
	processed, processedText := wireTime(event.ProcessedTime, format)
	sealedSecret, sealedToken := event.SealedCredentialSecret, event.SealedCredentialId
	var secretText, tokenText *string
	if format == eventtime.WireLegacy {
		if secretText, err = sealAsText(sealedSecret); err != nil {
			return nil, fmt.Errorf("sealed credential secret: %w", err)
		}
		if tokenText, err = sealAsText(sealedToken); err != nil {
			return nil, fmt.Errorf("sealed access token: %w", err)
		}
		sealedSecret, sealedToken = nil, nil
	}
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
		CredentialSecret:       secretText,
		SealedCredentialId:     tokenText,
		SealedSecret:           sealedSecret,
		SealedToken:            sealedToken,
		AuthenticatedTransport: event.AuthenticatedTransport,
		BrokerAsserted:         event.BrokerAsserted,
		Gateway:                event.Gateway,
		Integration:            event.Integration,
		SealedIntegrationToken: event.SealedIntegrationToken,
		IntegrationIdentifier:  event.IntegrationIdentifier,
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
	etype := eventtype.EventType(pbevent.EventType)

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
		BrokerAsserted:         pbevent.BrokerAsserted,
		Gateway:                pbevent.Gateway,
		Integration:            pbevent.Integration,
		SealedIntegrationToken: pbevent.SealedIntegrationToken,
		IntegrationIdentifier:  pbevent.IntegrationIdentifier,
	}

	return event, nil
}
