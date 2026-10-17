// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/eventtime"
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
func MarshalFailedEvent(event *model.FailedEvent) ([]byte, error) {
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
func UnmarshalFailedEvent(encoded []byte) (*model.FailedEvent, error) {
	// Unmarshal protobuf event.
	pbevent := &PFailedEvent{}
	err := proto.Unmarshal(encoded, pbevent)
	if err != nil {
		return nil, err
	}

	event := &model.FailedEvent{
		Reason:  uint(pbevent.Reason),
		Service: pbevent.Service,
		Message: pbevent.Message,
		Error:   pbevent.Error,
		Payload: pbevent.Payload,
	}

	return event, nil
}

// Marshal payload for a new relationship event.
func MarshalPayloadForNewRelationshipEvent(payload *model.ResolvedNewRelationshipPayload) ([]byte, error) {
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
func MarshalPayloadForLocationsEvent(payload *model.ResolvedLocationsPayload, format eventtime.WireFormat) ([]byte, error) {
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
func MarshalPayloadForMeasurementsEvent(payload *model.ResolvedMeasurementsPayload, format eventtime.WireFormat) ([]byte, error) {
	return marshalMeasurementsPayload(payload, format)
}

// Marshal payload for an alerts event.
func MarshalPayloadForAlertsEvent(payload *model.ResolvedAlertsPayload, format eventtime.WireFormat) ([]byte, error) {
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
func UnmarshalPayloadForNewRelationshipEvent(encoded []byte) (*model.ResolvedNewRelationshipPayload, error) {
	pbpayload := &PResolvedNewRelationshipPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedNewRelationshipPayload{
		RelationshipTypeId: pbpayload.RelationshipTypeId,
		TargetType:         pbpayload.TargetType,
		TargetId:           pbpayload.TargetId,
	}

	return payload, nil
}

// Unmarshal a payload into a locations event. envelope is the message's own occurred
// time, used for an entry published before per-sample times were honoured.
func UnmarshalPayloadForLocationsEvent(encoded []byte, envelope time.Time) (*model.ResolvedLocationsPayload, error) {
	pbpayload := &PResolvedLocationsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedLocationsPayload{}
	entries := make([]model.ResolvedLocationEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime, envelope)
		if terr != nil {
			return nil, terr
		}
		entry := model.ResolvedLocationEntry{
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
func UnmarshalPayloadForMeasurementsEvent(encoded []byte, envelope time.Time) (*model.ResolvedMeasurementsPayload, error) {
	pbpayload := &PResolvedMeasurementsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedMeasurementsPayload{}
	entries := make([]model.ResolvedMeasurementsEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		mxs := make([]model.ResolvedMeasurementEntry, 0)
		for _, pmx := range pbentry.Measurements {
			mx := model.ResolvedMeasurementEntry{
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
		entry := model.ResolvedMeasurementsEntry{
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
func UnmarshalPayloadForAlertsEvent(encoded []byte, envelope time.Time) (*model.ResolvedAlertsPayload, error) {
	pbpayload := &PResolvedAlertsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedAlertsPayload{}
	entries := make([]model.ResolvedAlertEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		occurred, terr := unmarshalEntryTime(pbentry.Occurred, pbentry.OccurredTime, envelope)
		if terr != nil {
			return nil, terr
		}
		entry := model.ResolvedAlertEntry{
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
func MarshalPayloadForStateChangeEvent(payload *model.ResolvedStateChangePayload) ([]byte, error) {
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
func UnmarshalPayloadForStateChangeEvent(encoded []byte) (*model.ResolvedStateChangePayload, error) {
	pbpayload := &PResolvedStateChangePayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	return &model.ResolvedStateChangePayload{
		State:             pbpayload.State,
		Reason:            pbpayload.Reason,
		ExpectedSessionId: pbpayload.ExpectedSessionId,
		SessionId:         pbpayload.SessionId,
		OccurredTime:      pbpayload.OccurredTime,
	}, nil
}

// Marshal resolved payload based on event type, its instants in format.
func MarshalResolvedPayload(etype esmodel.EventType, payload interface{}, format eventtime.WireFormat) ([]byte, error) {
	switch etype {
	case esmodel.NewRelationship:
		if rnapayload, ok := payload.(*model.ResolvedNewRelationshipPayload); ok {
			return MarshalPayloadForNewRelationshipEvent(rnapayload)
		}
		return nil, fmt.Errorf("invalid new assignment payload: %+v", payload)
	case esmodel.Location:
		if locpayload, ok := payload.(*model.ResolvedLocationsPayload); ok {
			return MarshalPayloadForLocationsEvent(locpayload, format)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case esmodel.Measurement:
		if mxpayload, ok := payload.(*model.ResolvedMeasurementsPayload); ok {
			return MarshalPayloadForMeasurementsEvent(mxpayload, format)
		}
		return nil, fmt.Errorf("invalid measurement payload: %+v", payload)
	case esmodel.Alert:
		if apayload, ok := payload.(*model.ResolvedAlertsPayload); ok {
			return MarshalPayloadForAlertsEvent(apayload, format)
		}
		return nil, fmt.Errorf("invalid alert payload: %+v", payload)
	case esmodel.StateChange:
		if scpayload, ok := payload.(*model.ResolvedStateChangePayload); ok {
			return MarshalPayloadForStateChangeEvent(scpayload)
		}
		return nil, fmt.Errorf("invalid state-change payload: %+v", payload)
	default:
		return nil, fmt.Errorf("unable to marshal unresolved payload for event type: %s", etype.String())
	}
}

// Unmarshal unresolved payload based on event type. envelope is the message's own
// occurred time, used for an entry published before per-sample times were honoured.
func UnmarshalResolvedPayload(etype esmodel.EventType, payload []byte, envelope time.Time) (interface{}, error) {
	switch etype {
	case esmodel.NewRelationship:
		return UnmarshalPayloadForNewRelationshipEvent(payload)
	case esmodel.Location:
		return UnmarshalPayloadForLocationsEvent(payload, envelope)
	case esmodel.Measurement:
		return UnmarshalPayloadForMeasurementsEvent(payload, envelope)
	case esmodel.Alert:
		return UnmarshalPayloadForAlertsEvent(payload, envelope)
	case esmodel.StateChange:
		return UnmarshalPayloadForStateChangeEvent(payload)
	default:
		return nil, fmt.Errorf("unable to unmarshal resolved payload for event type: %s", etype.String())
	}
}

// Marshal an alarm state-change event to protobuf bytes (ADR-041). Timestamps use
// RFC3339Nano to preserve sub-second precision (as every event stream now does): this
// stream drives ordered live UI updates and an operator ack/clear stamps a sub-second
// time.Now() into the row, so preserving that precision keeps the event's timeline
// consistent with the row and lets two same-tick transitions order. The optional scalar
// fields map to protobuf's optional (pointer) encoding so a subscriber can distinguish
// "absent" from a zero value.
func MarshalAlarmStateChangeEvent(event *model.AlarmStateChangeEvent) ([]byte, error) {
	pbevent := &PAlarmStateChangeEvent{
		EventType:      string(event.EventType),
		AlarmToken:     event.AlarmToken,
		OriginatorType: event.OriginatorType,
		OriginatorId:   uint64(event.OriginatorId),
		AlarmKey:       event.AlarmKey,
		MetricKey:      event.MetricKey,
		State:          event.State,
		Severity:       event.Severity,
		Acknowledged:   event.Acknowledged,
		AcknowledgedBy: event.AcknowledgedBy,
		LastValue:      event.LastValue,
		OccurredTime:   event.OccurredTime.Format(time.RFC3339Nano),
		RaisedTime:     event.RaisedTime.Format(time.RFC3339Nano),
		ScopeAnchors:   event.ScopeAnchors,
	}
	if event.PreviousSeverity != "" {
		ps := event.PreviousSeverity
		pbevent.PreviousSeverity = &ps
	}

	bytes, err := proto.Marshal(pbevent)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Unmarshal an encoded alarm state-change event.
func UnmarshalAlarmStateChangeEvent(encoded []byte) (*model.AlarmStateChangeEvent, error) {
	pbevent := &PAlarmStateChangeEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}

	occurred, err := time.Parse(time.RFC3339Nano, pbevent.OccurredTime)
	if err != nil {
		return nil, err
	}
	raised, err := time.Parse(time.RFC3339Nano, pbevent.RaisedTime)
	if err != nil {
		return nil, err
	}

	event := &model.AlarmStateChangeEvent{
		EventType:      model.AlarmEventType(pbevent.EventType),
		AlarmToken:     pbevent.AlarmToken,
		OriginatorType: pbevent.OriginatorType,
		OriginatorId:   uint(pbevent.OriginatorId),
		AlarmKey:       pbevent.AlarmKey,
		MetricKey:      pbevent.MetricKey,
		State:          pbevent.State,
		Severity:       pbevent.Severity,
		Acknowledged:   pbevent.Acknowledged,
		AcknowledgedBy: pbevent.AcknowledgedBy,
		LastValue:      pbevent.LastValue,
		RaisedTime:     raised,
		OccurredTime:   occurred,
		ScopeAnchors:   pbevent.ScopeAnchors,
	}
	if pbevent.PreviousSeverity != nil {
		event.PreviousSeverity = *pbevent.PreviousSeverity
	}
	return event, nil
}

// Marshal an entity-deletion event to protobuf bytes (ADR-044).
func MarshalEntityDeletedEvent(event *model.EntityDeletedEvent) ([]byte, error) {
	pbevent := &PEntityDeletedEvent{
		EntityType:  string(event.EntityType),
		EntityId:    uint64(event.EntityId),
		EntityToken: event.EntityToken,
		DeletedTime: event.DeletedTime.Format(time.RFC3339Nano),
	}
	return proto.Marshal(pbevent)
}

// Unmarshal an encoded entity-deletion event.
func UnmarshalEntityDeletedEvent(encoded []byte) (*model.EntityDeletedEvent, error) {
	pbevent := &PEntityDeletedEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	deleted, err := time.Parse(time.RFC3339Nano, pbevent.DeletedTime)
	if err != nil {
		return nil, err
	}
	return &model.EntityDeletedEvent{
		EntityType:  entity.Type(pbevent.EntityType),
		EntityId:    uint(pbevent.EntityId),
		EntityToken: pbevent.EntityToken,
		DeletedTime: deleted,
	}, nil
}

// Marshal a detection-rules-published event to protobuf bytes (ADR-051 slice 4b-3).
func MarshalDetectionRulesPublishedEvent(event *model.DetectionRulesPublishedEvent) ([]byte, error) {
	rules := make([]*PPublishedDetectionRule, 0, len(event.Rules))
	for _, r := range event.Rules {
		rules = append(rules, &PPublishedDetectionRule{
			Token:              r.Token,
			Definition:         r.Definition,
			EntityGroupToken:   r.EntityGroupToken,
			EntityGroupVersion: r.EntityGroupVersion,
		})
	}
	return proto.Marshal(&PDetectionRulesPublishedEvent{
		ProfileVersionToken: event.ProfileVersionToken,
		Rules:               rules,
		PublishedAt:         formatOptionalTime(event.PublishedAt),
	})
}

// Unmarshal an encoded detection-rules-published event (consumed by event-processing).
func UnmarshalDetectionRulesPublishedEvent(encoded []byte) (*model.DetectionRulesPublishedEvent, error) {
	pbevent := &PDetectionRulesPublishedEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	rules := make([]model.PublishedDetectionRule, 0, len(pbevent.Rules))
	for _, r := range pbevent.Rules {
		rules = append(rules, model.PublishedDetectionRule{
			Token:              r.Token,
			Definition:         r.Definition,
			EntityGroupToken:   r.EntityGroupToken,
			EntityGroupVersion: r.EntityGroupVersion,
		})
	}
	publishedAt, err := parseOptionalTime(pbevent.PublishedAt)
	if err != nil {
		return nil, err
	}
	return &model.DetectionRulesPublishedEvent{
		ProfileVersionToken: pbevent.ProfileVersionToken,
		Rules:               rules,
		PublishedAt:         publishedAt,
	}, nil
}

// Marshal a device-roster event to protobuf bytes (ADR-051 slice 4c-2).
func MarshalDeviceRosterEvent(event *model.DeviceRosterEvent) ([]byte, error) {
	return proto.Marshal(&PDeviceRosterEvent{
		DeviceToken:   event.DeviceToken,
		ProfileToken:  event.ProfileToken,
		ExpectedSince: formatOptionalTime(event.ExpectedSince),
		Disabled:      event.Disabled,
		Enabled:       event.Enabled,
	})
}

// Unmarshal an encoded device-roster event (consumed by event-processing).
func UnmarshalDeviceRosterEvent(encoded []byte) (*model.DeviceRosterEvent, error) {
	pbevent := &PDeviceRosterEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	expectedSince, err := parseOptionalTime(pbevent.ExpectedSince)
	if err != nil {
		return nil, err
	}
	return &model.DeviceRosterEvent{
		DeviceToken:   pbevent.DeviceToken,
		ProfileToken:  pbevent.ProfileToken,
		ExpectedSince: expectedSince,
		Disabled:      pbevent.Disabled,
		Enabled:       pbevent.Enabled,
	}, nil
}

// Marshal a device-attribute event to protobuf bytes (ADR-051 slice 4c-3).
func MarshalDeviceAttributeEvent(event *model.DeviceAttributeEvent) ([]byte, error) {
	return proto.Marshal(&PDeviceAttributeEvent{
		DeviceToken: event.DeviceToken,
		AttrKey:     event.AttrKey,
		Scope:       event.Scope,
		Value:       event.Value,
		Removed:     event.Removed,
		UpdatedAt:   formatOptionalTime(event.UpdatedAt),
	})
}

// Unmarshal an encoded device-attribute event (consumed by event-processing).
func UnmarshalDeviceAttributeEvent(encoded []byte) (*model.DeviceAttributeEvent, error) {
	pbevent := &PDeviceAttributeEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	updatedAt, err := parseOptionalTime(pbevent.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &model.DeviceAttributeEvent{
		DeviceToken: pbevent.DeviceToken,
		AttrKey:     pbevent.AttrKey,
		Scope:       pbevent.Scope,
		Value:       pbevent.Value,
		Removed:     pbevent.Removed,
		UpdatedAt:   updatedAt,
	}, nil
}

// formatOptionalTime renders a timestamp as RFC3339Nano, mapping the zero time to
// the empty string so an unset optional time round-trips as absent rather than as a
// spurious year-1 instant.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// parseOptionalTime is the inverse of formatOptionalTime: an empty string is the
// zero time (the field was absent), any other value must parse or the decode fails
// closed rather than silently dropping a malformed timestamp.
func parseOptionalTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// Marshal a resolved event to protobuf bytes. It is written directly onto the wire
// (resolved_wire.go), byte for byte what proto.Marshal writes for the generated message:
// every resolved event device-management publishes passes through here.
//
// It writes the format this process is configured to write (eventtime.ProducerWireFormat,
// from infrastructure.messaging.wireFormat), which every producer of the message shares.
func MarshalResolvedEvent(event *model.ResolvedEvent) ([]byte, error) {
	return marshalResolvedEvent(event, eventtime.ProducerWireFormat())
}

// MarshalResolvedEventAs is MarshalResolvedEvent in an explicit format. An unknown format,
// the zero value included, is refused.
func MarshalResolvedEventAs(event *model.ResolvedEvent, format eventtime.WireFormat) ([]byte, error) {
	return marshalResolvedEvent(event, format)
}

// Unmarshal encoded resolved event.
func UnmarshalResolvedEvent(encoded []byte) (*model.ResolvedEvent, error) {
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
	payload, err := UnmarshalResolvedPayload(esmodel.EventType(pbevent.EventType), pbevent.Payload, occurred)
	if err != nil {
		return nil, err
	}

	anchors := make([]model.ResolvedAnchor, 0, len(pbevent.Anchors))
	for _, a := range pbevent.Anchors {
		anchors = append(anchors, model.ResolvedAnchor{
			AnchorType:     a.AnchorType,
			AnchorToken:    a.AnchorToken,
			RelationshipId: uint(a.RelationshipId),
		})
	}
	memberships := make([]model.GroupRef, 0, len(pbevent.ScopeMemberships))
	for _, m := range pbevent.ScopeMemberships {
		memberships = append(memberships, model.GroupRef{
			GroupToken: m.GroupToken,
			Version:    m.Version,
		})
	}

	event := &model.ResolvedEvent{
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
		OccurredTime:         occurred,
		ProcessedTime:        processed,
		EventType:            esmodel.EventType(pbevent.EventType),
		Payload:              payload,
	}

	return event, nil
}

// MarshalDeviceOwnershipChangedEvent encodes a device ownership-changed fact (ADR-081).
func MarshalDeviceOwnershipChangedEvent(event *model.DeviceOwnershipChangedEvent) ([]byte, error) {
	return proto.Marshal(&PDeviceOwnershipChangedEvent{
		DeviceToken: event.DeviceToken,
		Anchors:     event.Anchors,
		ChangedTime: event.ChangedTime.UTC().Format(time.RFC3339Nano),
	})
}

// UnmarshalDeviceOwnershipChangedEvent decodes a device ownership-changed fact. A fact
// with no device token, or with an anchor that is not "<kind>:<token>", is refused: a
// consumer overwrites its projection with what this returns, so a malformed fact must
// not be read as "owned by nobody".
func UnmarshalDeviceOwnershipChangedEvent(encoded []byte) (*model.DeviceOwnershipChangedEvent, error) {
	pbevent := &PDeviceOwnershipChangedEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	if pbevent.DeviceToken == "" {
		return nil, fmt.Errorf("device ownership-changed fact names no device")
	}
	changed, err := time.Parse(time.RFC3339Nano, pbevent.ChangedTime)
	if err != nil {
		return nil, err
	}
	anchors := make([]string, 0, len(pbevent.Anchors))
	for _, a := range pbevent.Anchors {
		kind, token, ok := strings.Cut(a, ":")
		if !ok || kind == "" || token == "" {
			return nil, fmt.Errorf("device ownership-changed fact carries a malformed anchor %q", a)
		}
		anchors = append(anchors, a)
	}
	return &model.DeviceOwnershipChangedEvent{
		DeviceToken: pbevent.DeviceToken,
		Anchors:     anchors,
		ChangedTime: changed,
	}, nil
}
