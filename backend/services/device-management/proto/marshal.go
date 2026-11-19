// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-device-management/contract"
	"google.golang.org/protobuf/proto"
)

// Marshal an alarm state-change event to protobuf bytes (ADR-041). Timestamps use
// RFC3339Nano to preserve sub-second precision (as every event stream now does): this
// stream drives ordered live UI updates and an operator ack/clear stamps a sub-second
// time.Now() into the row, so preserving that precision keeps the event's timeline
// consistent with the row and lets two same-tick transitions order. The optional scalar
// fields map to protobuf's optional (pointer) encoding so a subscriber can distinguish
// "absent" from a zero value.
func MarshalAlarmStateChangeEvent(event *contract.AlarmStateChangeEvent) ([]byte, error) {
	pbevent := &PAlarmStateChangeEvent{
		ClearReason:    event.ClearReason,
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

		DeviceToken:     event.DeviceToken,
		DeviceName:      event.DeviceName,
		DeviceTypeToken: event.DeviceTypeToken,
		PositionDerived: event.PositionDerived,

		SuppressionWindowToken:       event.SuppressionWindowToken,
		RaisedDuringSuppressionToken: event.RaisedDuringSuppressionToken,
	}
	if event.SuppressedUntil != nil {
		pbevent.SuppressedUntil = event.SuppressedUntil.Format(time.RFC3339Nano)
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
func UnmarshalAlarmStateChangeEvent(encoded []byte) (*contract.AlarmStateChangeEvent, error) {
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

	event := &contract.AlarmStateChangeEvent{
		ClearReason:    pbevent.ClearReason,
		EventType:      contract.AlarmEventType(pbevent.EventType),
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

		DeviceToken:     pbevent.DeviceToken,
		DeviceName:      pbevent.DeviceName,
		DeviceTypeToken: pbevent.DeviceTypeToken,
		PositionDerived: pbevent.PositionDerived,

		RaisedDuringSuppressionToken: pbevent.RaisedDuringSuppressionToken,
	}
	if pbevent.PreviousSeverity != nil {
		event.PreviousSeverity = *pbevent.PreviousSeverity
	}
	// Live suppression is optional, and decoded leniently: a deadline that does not parse
	// (or arrives without the window that set it) is read as NOT suppressed, and the event
	// still decodes. Rejecting the whole event would lose its page, which is the one outcome
	// a suppression field must never cause.
	if pbevent.SuppressedUntil != "" && pbevent.SuppressionWindowToken != "" {
		if until, err := time.Parse(time.RFC3339Nano, pbevent.SuppressedUntil); err == nil {
			event.SuppressedUntil = &until
			event.SuppressionWindowToken = pbevent.SuppressionWindowToken
		}
	}
	return event, nil
}

// Marshal a detection-rules-published event to protobuf bytes (ADR-051 slice 4b-3).
func MarshalDetectionRulesPublishedEvent(event *contract.DetectionRulesPublishedEvent) ([]byte, error) {
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
func UnmarshalDetectionRulesPublishedEvent(encoded []byte) (*contract.DetectionRulesPublishedEvent, error) {
	pbevent := &PDetectionRulesPublishedEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	rules := make([]contract.PublishedDetectionRule, 0, len(pbevent.Rules))
	for _, r := range pbevent.Rules {
		rules = append(rules, contract.PublishedDetectionRule{
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
	return &contract.DetectionRulesPublishedEvent{
		ProfileVersionToken: pbevent.ProfileVersionToken,
		Rules:               rules,
		PublishedAt:         publishedAt,
	}, nil
}

// Marshal a device-roster event to protobuf bytes (ADR-051 slice 4c-2).
func MarshalDeviceRosterEvent(event *contract.DeviceRosterEvent) ([]byte, error) {
	return proto.Marshal(&PDeviceRosterEvent{
		DeviceToken:   event.DeviceToken,
		ProfileToken:  event.ProfileToken,
		ExpectedSince: formatOptionalTime(event.ExpectedSince),
		Disabled:      event.Disabled,
		Enabled:       event.Enabled,
	})
}

// Unmarshal an encoded device-roster event (consumed by event-processing).
func UnmarshalDeviceRosterEvent(encoded []byte) (*contract.DeviceRosterEvent, error) {
	pbevent := &PDeviceRosterEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	expectedSince, err := parseOptionalTime(pbevent.ExpectedSince)
	if err != nil {
		return nil, err
	}
	return &contract.DeviceRosterEvent{
		DeviceToken:   pbevent.DeviceToken,
		ProfileToken:  pbevent.ProfileToken,
		ExpectedSince: expectedSince,
		Disabled:      pbevent.Disabled,
		Enabled:       pbevent.Enabled,
	}, nil
}

// Marshal a device-attribute event to protobuf bytes (ADR-051 slice 4c-3).
func MarshalDeviceAttributeEvent(event *contract.DeviceAttributeEvent) ([]byte, error) {
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
func UnmarshalDeviceAttributeEvent(encoded []byte) (*contract.DeviceAttributeEvent, error) {
	pbevent := &PDeviceAttributeEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	updatedAt, err := parseOptionalTime(pbevent.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &contract.DeviceAttributeEvent{
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

// MarshalDeviceOwnershipChangedEvent encodes a device ownership-changed fact (ADR-081).
func MarshalDeviceOwnershipChangedEvent(event *contract.DeviceOwnershipChangedEvent) ([]byte, error) {
	return proto.Marshal(&PDeviceOwnershipChangedEvent{
		DeviceIncarnation: event.DeviceIncarnation,
		DeviceToken:       event.DeviceToken,
		Anchors:           event.Anchors,
		ChangedTime:       event.ChangedTime.UTC().Format(time.RFC3339Nano),
	})
}

// UnmarshalDeviceOwnershipChangedEvent decodes a device ownership-changed fact. A fact
// with no device token, or with an anchor that is not "<kind>:<token>", is refused: a
// consumer overwrites its projection with what this returns, so a malformed fact must
// not be read as "owned by nobody".
func UnmarshalDeviceOwnershipChangedEvent(encoded []byte) (*contract.DeviceOwnershipChangedEvent, error) {
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
	return &contract.DeviceOwnershipChangedEvent{
		DeviceIncarnation: pbevent.DeviceIncarnation,
		DeviceToken:       pbevent.DeviceToken,
		Anchors:           anchors,
		ChangedTime:       changed,
	}, nil
}
