// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"google.golang.org/protobuf/proto"
)

// optionalString maps a model string to a proto3 optional field: an empty value
// is encoded as absent (nil) rather than a present empty string, so a device with
// no resolvable profile version reads back as the empty token via the generated
// Get* accessor.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
func MarshalPayloadForLocationsEvent(payload *model.ResolvedLocationsPayload) ([]byte, error) {
	pbpayload := &PResolvedLocationsPayload{}
	for _, entry := range payload.Entries {
		pbentry := &PResolvedLocationEntry{
			Latitude:     entry.Latitude,
			Longitude:    entry.Longitude,
			Elevation:    entry.Elevation,
			OccurredTime: entry.OccurredTime,
		}
		pbpayload.Entries = append(pbpayload.Entries, pbentry)
	}
	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for a measurements event.
func MarshalPayloadForMeasurementsEvent(payload *model.ResolvedMeasurementsPayload) ([]byte, error) {
	pbpayload := &PResolvedMeasurementsPayload{}
	for _, mxsentry := range payload.Entries {
		pmxentries := make([]*PResolvedMeasurementEntry, 0)
		for _, mxentry := range mxsentry.Entries {
			pmxentry := &PResolvedMeasurementEntry{
				Name:       mxentry.Name,
				Value:      mxentry.Value,
				Classifier: mxentry.Classifier,
				Unit:       mxentry.Unit,
				DataType:   mxentry.DataType,
			}
			pmxentries = append(pmxentries, pmxentry)
		}
		pbentry := &PResolvedMeasurementsEntry{
			Measurements: pmxentries,
			OccurredTime: mxsentry.OccurredTime,
		}
		pbpayload.Entries = append(pbpayload.Entries, pbentry)
	}
	bytes, err := proto.Marshal(pbpayload)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// Marshal payload for an alerts event.
func MarshalPayloadForAlertsEvent(payload *model.ResolvedAlertsPayload) ([]byte, error) {
	pbpayload := &PResolvedAlertsPayload{}
	for _, entry := range payload.Entries {
		pbentry := &PResolvedAlertEntry{
			Type:         entry.Type,
			Level:        entry.Level,
			Message:      entry.Message,
			Source:       entry.Source,
			OccurredTime: entry.OccurredTime,
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

// Unmarshal a payload into a locations event.
func UnmarshalPayloadForLocationsEvent(encoded []byte) (*model.ResolvedLocationsPayload, error) {
	pbpayload := &PResolvedLocationsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedLocationsPayload{}
	entries := make([]model.ResolvedLocationEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		entry := model.ResolvedLocationEntry{
			Latitude:     pbentry.Latitude,
			Longitude:    pbentry.Longitude,
			Elevation:    pbentry.Elevation,
			OccurredTime: pbentry.OccurredTime,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into a measurements event.
func UnmarshalPayloadForMeasurementsEvent(encoded []byte) (*model.ResolvedMeasurementsPayload, error) {
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
			}
			mxs = append(mxs, mx)
		}
		entry := model.ResolvedMeasurementsEntry{
			Entries:      mxs,
			OccurredTime: pbentry.OccurredTime,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Unmarshal a payload into an alerts event.
func UnmarshalPayloadForAlertsEvent(encoded []byte) (*model.ResolvedAlertsPayload, error) {
	pbpayload := &PResolvedAlertsPayload{}
	err := proto.Unmarshal(encoded, pbpayload)
	if err != nil {
		return nil, err
	}
	payload := &model.ResolvedAlertsPayload{}
	entries := make([]model.ResolvedAlertEntry, 0)
	for _, pbentry := range pbpayload.Entries {
		entry := model.ResolvedAlertEntry{
			Type:         pbentry.Type,
			Level:        pbentry.Level,
			Message:      pbentry.Message,
			Source:       pbentry.Source,
			OccurredTime: pbentry.OccurredTime,
		}
		entries = append(entries, entry)
	}
	payload.Entries = entries
	return payload, nil
}

// Marshal payload for a state-change (presence) event.
func MarshalPayloadForStateChangeEvent(payload *model.ResolvedStateChangePayload) ([]byte, error) {
	pbpayload := &PResolvedStateChangePayload{
		State:        payload.State,
		Reason:       payload.Reason,
		SessionId:    payload.SessionId,
		OccurredTime: payload.OccurredTime,
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
		State:        pbpayload.State,
		Reason:       pbpayload.Reason,
		SessionId:    pbpayload.SessionId,
		OccurredTime: pbpayload.OccurredTime,
	}, nil
}

// Marshal unresolved payload based on event type.
func MarshalResolvedPayload(etype esmodel.EventType, payload interface{}) ([]byte, error) {
	switch etype {
	case esmodel.NewRelationship:
		if rnapayload, ok := payload.(*model.ResolvedNewRelationshipPayload); ok {
			return MarshalPayloadForNewRelationshipEvent(rnapayload)
		}
		return nil, fmt.Errorf("invalid new assignment payload: %+v", payload)
	case esmodel.Location:
		if locpayload, ok := payload.(*model.ResolvedLocationsPayload); ok {
			return MarshalPayloadForLocationsEvent(locpayload)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case esmodel.Measurement:
		if mxpayload, ok := payload.(*model.ResolvedMeasurementsPayload); ok {
			return MarshalPayloadForMeasurementsEvent(mxpayload)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case esmodel.Alert:
		if apayload, ok := payload.(*model.ResolvedAlertsPayload); ok {
			return MarshalPayloadForAlertsEvent(apayload)
		}
		return nil, fmt.Errorf("invalid location payload: %+v", payload)
	case esmodel.StateChange:
		if scpayload, ok := payload.(*model.ResolvedStateChangePayload); ok {
			return MarshalPayloadForStateChangeEvent(scpayload)
		}
		return nil, fmt.Errorf("invalid state-change payload: %+v", payload)
	default:
		return nil, fmt.Errorf("unable to marshal unresolved payload for event type: %s", etype.String())
	}
}

// Unmarshal unresolved payload based on event type.
func UnmarshalResolvedPayload(etype esmodel.EventType, payload []byte) (interface{}, error) {
	switch etype {
	case esmodel.NewRelationship:
		return UnmarshalPayloadForNewRelationshipEvent(payload)
	case esmodel.Location:
		return UnmarshalPayloadForLocationsEvent(payload)
	case esmodel.Measurement:
		return UnmarshalPayloadForMeasurementsEvent(payload)
	case esmodel.Alert:
		return UnmarshalPayloadForAlertsEvent(payload)
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
		Message:        event.Message,
		OccurredTime:   event.OccurredTime.Format(time.RFC3339Nano),
		RaisedTime:     event.RaisedTime.Format(time.RFC3339Nano),
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
		Message:        pbevent.Message,
		RaisedTime:     raised,
		OccurredTime:   occurred,
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

// Marshal a resolved event to protobuf bytes.
func MarshalResolvedEvent(event *model.ResolvedEvent) ([]byte, error) {
	// Encode payload.
	pybytes, err := MarshalResolvedPayload(event.EventType, event.Payload)
	if err != nil {
		return nil, err
	}

	// Encode protobuf event.
	anchors := make([]*PResolvedAnchor, 0, len(event.Anchors))
	for _, a := range event.Anchors {
		anchors = append(anchors, &PResolvedAnchor{
			AnchorType:     a.AnchorType,
			AnchorToken:    a.AnchorToken,
			RelationshipId: uint64(a.RelationshipId),
		})
	}
	memberships := make([]*PScopeMembership, 0, len(event.ScopeMemberships))
	for _, m := range event.ScopeMemberships {
		memberships = append(memberships, &PScopeMembership{
			GroupToken: m.GroupToken,
			Version:    m.Version,
		})
	}
	pbevent := &PResolvedEvent{
		Source:              event.Source,
		AltId:               event.AltId,
		SourceDeviceToken:   event.SourceDeviceToken,
		Anchors:             anchors,
		DeviceTypeToken:     optionalString(event.DeviceTypeToken),
		ProfileVersionToken: optionalString(event.ProfileVersionToken),
		ScopeMemberships:    memberships,
		ExternalId:          optionalString(event.ExternalId),
		OccurredTime:        event.OccurredTime.Format(time.RFC3339Nano),
		ProcessedTime:       event.ProcessedTime.Format(time.RFC3339Nano),
		EventType:           int64(event.EventType),
		Payload:             pybytes,
	}

	// Marshal event to bytes.
	bytes, err := proto.Marshal(pbevent)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

// Unmarshal encoded resolved event.
func UnmarshalResolvedEvent(encoded []byte) (*model.ResolvedEvent, error) {
	// Unmarshal protobuf event.
	pbevent := &PResolvedEvent{}
	err := proto.Unmarshal(encoded, pbevent)
	if err != nil {
		return nil, err
	}

	// Unmarshal payload based on event type.
	payload, err := UnmarshalResolvedPayload(esmodel.EventType(pbevent.EventType), pbevent.Payload)
	if err != nil {
		return nil, err
	}

	occurred, err := time.Parse(time.RFC3339Nano, pbevent.OccurredTime)
	if err != nil {
		return nil, err
	}
	processed, err := time.Parse(time.RFC3339Nano, pbevent.ProcessedTime)
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
		Source:              pbevent.Source,
		AltId:               pbevent.AltId,
		SourceDeviceToken:   pbevent.SourceDeviceToken,
		Anchors:             anchors,
		DeviceTypeToken:     pbevent.GetDeviceTypeToken(),
		ProfileVersionToken: pbevent.GetProfileVersionToken(),
		ScopeMemberships:    memberships,
		ExternalId:          pbevent.GetExternalId(),
		OccurredTime:        occurred,
		ProcessedTime:       processed,
		EventType:           esmodel.EventType(pbevent.EventType),
		Payload:             payload,
	}

	return event, nil
}
