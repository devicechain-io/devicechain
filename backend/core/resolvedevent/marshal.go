// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"google.golang.org/protobuf/proto"
)

// Every instant on a resolved event is written in exactly one of two forms
// (eventtime.WireNanos): nanoseconds since the Unix epoch in an sfixed64 field, or, for an
// instant that count cannot hold, an RFC 3339 string in the field every instant was written
// in before. A reader takes the count when it is present and the string otherwise, which is
// also what reads a message written before the count existed: the retained stream the
// replay preview reads holds those.

// marshalEntryTime renders one sample's own instant onto the wire, in one form or the
// other. A resolved entry always carries one, so this never encodes absence — the fields
// stay proto3 `optional` so bytes published before per-sample times existed still decode
// (see unmarshalEntryTime).
//
// format says which form: WireLegacy always writes the string, in UTC, as every release
// before the count did; WireCompact writes the count when it can hold the instant.
func marshalEntryTime(occurred time.Time, format eventtime.WireFormat) (*int64, *string) {
	if nanos, ok := eventtime.WireNanos(occurred); ok && format == eventtime.WireCompact {
		return &nanos, nil
	}
	formatted := eventtime.WireString(occurred)
	return nil, &formatted
}

// readWireTime reads an envelope instant: the count when present, else the string. A
// message with neither fails, as a message with an unparseable string always has.
func readWireTime(nanos *int64, text string) (time.Time, error) {
	if nanos != nil {
		return eventtime.FromWireNanos(*nanos), nil
	}
	return eventtime.ParseWireString(text)
}

// unmarshalEntryTime reads one sample's own instant back off the wire — the count, else
// the string — falling back to the message envelope's time when neither is present.
//
// 🔴 THE FALLBACK IS FOR HISTORY, NOT FOR PRODUCERS. Every resolved event published from
// this version on carries a per-entry time, because resolution always sets one. What is
// absent is the field on events published BEFORE per-sample times were honoured — and
// the replay preview reads exactly those bytes back off the retained stream. Decoding
// them at the envelope time is what they meant when they were written, so history
// replays as it happened rather than being skipped as corrupt.
//
// A value that is PRESENT but unparseable is a different thing entirely and fails: this
// seam is internal, so a malformed instant here is a defect in this repository, and
// swallowing it would silently collapse a batch onto one time.
func unmarshalEntryTime(nanos *int64, raw *string, envelope time.Time) (time.Time, error) {
	if nanos != nil {
		return eventtime.FromWireNanos(*nanos), nil
	}
	if raw == nil {
		return envelope, nil
	}
	parsed, err := eventtime.ParseWireString(*raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("entry occurredTime %q is not an RFC3339 timestamp: %w", *raw, err)
	}
	return parsed, nil
}

// Marshal a failed event to protobuf bytes.
func MarshalFailedEvent(event *FailedEvent) ([]byte, error) {
	// Encode protobuf event.
	pbevent := &PFailedEvent{
		Reason:  FailureReason(event.Reason),
		Service: event.Service,
		Message: event.Message,
		Error:   event.Error,
		Payload: event.Payload,
	}

	// Marshal event to bytes.
	bytes, err := proto.Marshal(pbevent)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

// Unmarshal encoded failed event.
func UnmarshalFailedEvent(encoded []byte) (*FailedEvent, error) {
	// Unmarshal protobuf event.
	pbevent := &PFailedEvent{}
	err := proto.Unmarshal(encoded, pbevent)
	if err != nil {
		return nil, err
	}

	event := &FailedEvent{
		Reason:  uint(pbevent.Reason),
		Service: pbevent.Service,
		Message: pbevent.Message,
		Error:   pbevent.Error,
		Payload: pbevent.Payload,
	}

	return event, nil
}

// Marshal payload for a new relationship event.
func MarshalPayloadForNewRelationshipEvent(payload *ResolvedNewRelationshipPayload) ([]byte, error) {
	pbpayload := &PResolvedNewRelationshipPayload{
		RelationshipTypeId: payload.RelationshipTypeId,
		TargetType:         payload.TargetType,
		TargetId:           payload.TargetId,
	}

	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for a locations event.
func MarshalPayloadForLocationsEvent(payload *ResolvedLocationsPayload, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	pbpayload := &PResolvedLocationsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime, format)
		pbentry := &PResolvedLocationEntry{
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

// Marshal payload for a measurements event. It is written directly onto the wire
// (resolved_wire.go), byte for byte what proto.Marshal writes for the generated message.
func MarshalPayloadForMeasurementsEvent(payload *ResolvedMeasurementsPayload, format eventtime.WireFormat) ([]byte, error) {
	return marshalMeasurementsPayload(payload, format)
}

// Marshal payload for an alerts event.
func MarshalPayloadForAlertsEvent(payload *ResolvedAlertsPayload, format eventtime.WireFormat) ([]byte, error) {
	if !format.Valid() {
		return nil, eventtime.ErrUnknownWireFormat
	}
	pbpayload := &PResolvedAlertsPayload{}
	for _, entry := range payload.Entries {
		nanos, text := marshalEntryTime(entry.OccurredTime, format)
		pbentry := &PResolvedAlertEntry{
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

// Unmarshal a payload into a new relationship event.
func UnmarshalPayloadForNewRelationshipEvent(encoded []byte) (*ResolvedNewRelationshipPayload, error) {
	pbpayload := &PResolvedNewRelationshipPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &ResolvedNewRelationshipPayload{
		RelationshipTypeId: pbpayload.RelationshipTypeId,
		TargetType:         pbpayload.TargetType,
		TargetId:           pbpayload.TargetId,
	}

	return payload, nil
}

// Unmarshal a payload into a locations event. envelope is the message's own occurred
// time, used for an entry published before per-sample times were honoured.
func UnmarshalPayloadForLocationsEvent(encoded []byte, envelope time.Time) (*ResolvedLocationsPayload, error) {
	pbpayload := &PResolvedLocationsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &ResolvedLocationsPayload{}
	entries := make([]ResolvedLocationEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime, envelope)
		if terr != nil {
			return nil, terr
		}
		entry := ResolvedLocationEntry{
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

// Unmarshal a payload into a measurements event. envelope is the message's own occurred
// time, used for an entry published before per-sample times were honoured.
func UnmarshalPayloadForMeasurementsEvent(encoded []byte, envelope time.Time) (*ResolvedMeasurementsPayload, error) {
	pbpayload := &PResolvedMeasurementsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &ResolvedMeasurementsPayload{}
	entries := make([]ResolvedMeasurementsEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		mxs := make([]ResolvedMeasurementEntry, 0)
		for _, pmx := range pbentry.Measurements {
			mx := ResolvedMeasurementEntry{
				Name:       pmx.Name,
				Value:      pmx.Value,
				Classifier: pmx.Classifier,
				Unit:       pmx.Unit,
				DataType:   pmx.DataType,
				Derived:    pmx.Derived,
			}
			mxs = append(mxs, mx)
		}
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime, envelope)
		if terr != nil {
			return nil, terr
		}
		entry := ResolvedMeasurementsEntry{
			Entries:      mxs,
			OccurredTime: occurred,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into an alerts event. envelope is the message's own occurred
// time, used for an entry published before per-sample times were honoured.
func UnmarshalPayloadForAlertsEvent(encoded []byte, envelope time.Time) (*ResolvedAlertsPayload, error) {
	pbpayload := &PResolvedAlertsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &ResolvedAlertsPayload{}
	entries := make([]ResolvedAlertEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime, envelope)
		if terr != nil {
			return nil, terr
		}
		entry := ResolvedAlertEntry{
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

// Marshal payload for a state-change (presence) event.
func MarshalPayloadForStateChangeEvent(payload *ResolvedStateChangePayload) ([]byte, error) {
	pbpayload := &PResolvedStateChangePayload{
		State:             payload.State,
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

// Unmarshal a payload into a state-change (presence) event.
func UnmarshalPayloadForStateChangeEvent(encoded []byte) (*ResolvedStateChangePayload, error) {
	pbpayload := &PResolvedStateChangePayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	return &ResolvedStateChangePayload{
		State:             pbpayload.State,
		Reason:            pbpayload.Reason,
		ExpectedSessionId: pbpayload.ExpectedSessionId,
		SessionId:         pbpayload.SessionId,
		OccurredTime:      pbpayload.OccurredTime,
	}, nil
}

// Marshal resolved payload based on event type, its instants in format.
func MarshalResolvedPayload(etype eventtype.EventType, payload interface{}, format eventtime.WireFormat) ([]byte, error) {
	switch etype {
	case eventtype.NewRelationship:
		if rnapayload, ok := payload.(*ResolvedNewRelationshipPayload); ok {
			return MarshalPayloadForNewRelationshipEvent(rnapayload)
		}
		return nil, fmt.Errorf("invalid new assignment payload: %+v", payload)
	case eventtype.Location:
		if locpayload, ok := payload.(*ResolvedLocationsPayload); ok {
			return MarshalPayloadForLocationsEvent(locpayload, format)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case eventtype.Measurement:
		if mxpayload, ok := payload.(*ResolvedMeasurementsPayload); ok {
			return MarshalPayloadForMeasurementsEvent(mxpayload, format)
		}
		return nil, fmt.Errorf("invalid measurement payload: %+v", payload)
	case eventtype.Alert:
		if apayload, ok := payload.(*ResolvedAlertsPayload); ok {
			return MarshalPayloadForAlertsEvent(apayload, format)
		}
		return nil, fmt.Errorf("invalid alert payload: %+v", payload)
	case eventtype.StateChange:
		if scpayload, ok := payload.(*ResolvedStateChangePayload); ok {
			return MarshalPayloadForStateChangeEvent(scpayload)
		}
		return nil, fmt.Errorf("invalid state-change payload: %+v", payload)
	default:
		return nil, fmt.Errorf("unable to marshal unresolved payload for event type: %s", etype.String())
	}
}

// Unmarshal unresolved payload based on event type. envelope is the message's own
// occurred time, used for an entry published before per-sample times were honoured.
func UnmarshalResolvedPayload(etype eventtype.EventType, payload []byte, envelope time.Time) (interface{}, error) {
	switch etype {
	case eventtype.NewRelationship:
		return UnmarshalPayloadForNewRelationshipEvent(payload)
	case eventtype.Location:
		return UnmarshalPayloadForLocationsEvent(payload, envelope)
	case eventtype.Measurement:
		return UnmarshalPayloadForMeasurementsEvent(payload, envelope)
	case eventtype.Alert:
		return UnmarshalPayloadForAlertsEvent(payload, envelope)
	case eventtype.StateChange:
		return UnmarshalPayloadForStateChangeEvent(payload)
	default:
		return nil, fmt.Errorf("unable to unmarshal resolved payload for event type: %s", etype.String())
	}
}

// Marshal a resolved event to protobuf bytes. It is written directly onto the wire
// (resolved_wire.go), byte for byte what proto.Marshal writes for the generated message:
// every resolved event device-management publishes passes through here.
//
// It writes the format this process is configured to write (eventtime.ProducerWireFormat,
// from infrastructure.messaging.wireFormat), which every producer of the message shares.
func MarshalResolvedEvent(event *ResolvedEvent) ([]byte, error) {
	return marshalResolvedEvent(event, eventtime.ProducerWireFormat())
}

// MarshalResolvedEventAs is MarshalResolvedEvent in an explicit format. An unknown format,
// the zero value included, is refused.
func MarshalResolvedEventAs(event *ResolvedEvent, format eventtime.WireFormat) ([]byte, error) {
	return marshalResolvedEvent(event, format)
}

// Unmarshal encoded resolved event.
func UnmarshalResolvedEvent(encoded []byte) (*ResolvedEvent, error) {
	// Unmarshal protobuf event.
	pbevent := &PResolvedEvent{}
	err := proto.Unmarshal(encoded, pbevent)
	if err != nil {
		return nil, err
	}

	// The envelope's time is parsed BEFORE the payload because the payload needs it: an
	// entry published before per-sample times were honoured carries no time of its own
	// and decodes at the envelope's, which is what it meant when it was written.
	occurred, err := readWireTime(pbevent.Occurred, pbevent.OccurredTime)
	if err != nil {
		return nil, err
	}
	processed, err := readWireTime(pbevent.Processed, pbevent.ProcessedTime)
	if err != nil {
		return nil, err
	}

	// Unmarshal payload based on event type.
	payload, err := UnmarshalResolvedPayload(eventtype.EventType(pbevent.EventType), pbevent.Payload, occurred)
	if err != nil {
		return nil, err
	}

	anchors := make([]ResolvedAnchor, 0, len(pbevent.Anchors))
	for _, a := range pbevent.Anchors {
		anchors = append(anchors, ResolvedAnchor{
			AnchorType:     a.AnchorType,
			AnchorToken:    a.AnchorToken,
			RelationshipId: uint(a.RelationshipId),
		})
	}
	memberships := make([]GroupRef, 0, len(pbevent.ScopeMemberships))
	for _, m := range pbevent.ScopeMemberships {
		memberships = append(memberships, GroupRef{
			GroupToken: m.GroupToken,
			Version:    m.Version,
		})
	}

	event := &ResolvedEvent{
		Source:               pbevent.Source,
		AltId:                pbevent.AltId,
		SourceDeviceToken:    pbevent.SourceDeviceToken,
		Anchors:              anchors,
		DeviceTypeToken:      pbevent.GetDeviceTypeToken(),
		ProfileVersionToken:  pbevent.GetProfileVersionToken(),
		ScopeMemberships:     memberships,
		ExternalId:           pbevent.GetExternalId(),
		FenceSetVersion:      pbevent.GetFenceSetVersion(),
		SourceDeviceDisabled: pbevent.GetSourceDeviceDisabled(),
		GatewayToken:         pbevent.GetGatewayToken(),
		OccurredTime:         occurred,
		ProcessedTime:        processed,
		EventType:            eventtype.EventType(pbevent.EventType),
		Payload:              payload,
	}

	return event, nil
}
