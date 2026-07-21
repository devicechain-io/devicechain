// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/stretchr/testify/assert"
)

// A resolved event's OccurredTime/ProcessedTime must survive the marshal/unmarshal
// round-trip WITH SUB-SECOND PRECISION: they cross the messaging hop to every
// downstream consumer (event-management persistence, device-state's latest-value +
// connectivity projections), and a dropped timestamp lands as the zero time
// (0001-01-01), corrupting the hypertable partition key and the last-activity/updated
// columns. Precision is load-bearing, not cosmetic: the event natural key is
// (tenant, device, event_type, occurred_time), so truncating occurred_time to the
// whole second silently collapses two distinct sub-second readings from one device
// into one persisted event (and orphans the second's child rows). The literals below
// carry distinct nanoseconds so a marshal that truncated to RFC3339 would fail the
// Equal() assertions below — the check that catches the regression.
func TestMarshalResolvedEventCarriesTimestamps(t *testing.T) {
	occurred := time.Date(2026, 7, 20, 10, 30, 15, 111222333, time.UTC)
	processed := time.Date(2026, 7, 20, 10, 30, 15, 444555666, time.UTC)

	classifier := uint64(42)
	unit := "Cel"
	dataType := "DOUBLE"
	event := &model.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   "device-001",
		DeviceTypeToken:     "sensor-type",
		ProfileVersionToken: "temp-profile@3",
		Anchors: []model.ResolvedAnchor{
			{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 5},
			{AnchorType: "area", AnchorToken: "warehouse-3", RelationshipId: 8},
		},
		ScopeMemberships: []model.GroupRef{
			{GroupToken: "arid-areas", Version: 2},
			{GroupToken: "beta-fleet", Version: 1},
		},
		OccurredTime:  occurred,
		ProcessedTime: processed,
		EventType:     esmodel.Measurement,
		Payload: &model.ResolvedMeasurementsPayload{
			Entries: []model.ResolvedMeasurementsEntry{
				{Entries: []model.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5",
					Classifier: &classifier, Unit: &unit, DataType: &dataType}}},
			},
		},
	}

	bytes, err := MarshalResolvedEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalResolvedEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, got.OccurredTime.Equal(occurred), "occurred time round-trip: got %s want %s", got.OccurredTime, occurred)
	assert.True(t, got.ProcessedTime.Equal(processed), "processed time round-trip: got %s want %s", got.ProcessedTime, processed)
	// The full anchor set survives the round-trip.
	assert.Equal(t, event.Anchors, got.Anchors)
	// The stamped scope memberships survive intact (ADR-062) — the engine's scope check
	// is a set test on exactly these replayed bytes.
	assert.Equal(t, event.ScopeMemberships, got.ScopeMemberships)
	// The denormalized rule-scoping tokens survive intact (ADR-051).
	assert.Equal(t, "sensor-type", got.DeviceTypeToken)
	assert.Equal(t, "temp-profile@3", got.ProfileVersionToken)
	// The bound classifier + denormalized unit/data type survive intact (ADR-016).
	assert.Equal(t, event.Payload, got.Payload)
}

// A resolved event with no rule-scoping tokens (the common unprofiled/unpublished
// device case) round-trips with empty strings: empty encodes as an absent optional
// proto field and decodes back to "" via the generated accessor (ADR-051).
func TestMarshalResolvedEventEmptyProfileScope(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	event := &model.ResolvedEvent{
		Source:            "http1",
		SourceDeviceToken: "device-001",
		OccurredTime:      now,
		ProcessedTime:     now,
		EventType:         esmodel.Measurement,
		Payload:           &model.ResolvedMeasurementsPayload{},
	}

	bytes, err := MarshalResolvedEvent(event)
	assert.NoError(t, err)
	got, err := UnmarshalResolvedEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "", got.DeviceTypeToken)
	assert.Equal(t, "", got.ProfileVersionToken)
}

// An alarm state-change event (ADR-041) must survive the marshal/unmarshal round-trip
// intact: it crosses the messaging hop to the subscription bridge (2.E) and
// notification consumers, and every field drives what a subscriber renders. The
// optional scalars must round-trip as present (not collapsed to a zero value) so a
// de-escalation's prior severity and a cleared alarm's last value are preserved.
func TestMarshalAlarmStateChangeEventRoundTrips(t *testing.T) {
	// Sub-second precision must survive: an operator ack/clear stamps a nanosecond
	// time.Now() into the row and this stream is what a subscriber orders on.
	raised := time.Date(2026, 7, 4, 10, 30, 0, 111222333, time.UTC)
	occurred := time.Date(2026, 7, 4, 10, 42, 17, 987654321, time.UTC)
	by := "op@example.com"
	last := 123.5
	msg := "temperature above 100"

	event := &model.AlarmStateChangeEvent{
		EventType:        model.AlarmEventEscalated,
		AlarmToken:       "alarm-tok",
		OriginatorType:   "device",
		OriginatorId:     7,
		AlarmKey:         "over-temp",
		MetricKey:        "temp",
		State:            string(model.AlarmStateActive),
		Severity:         string(model.AlarmSeverityCritical),
		PreviousSeverity: string(model.AlarmSeverityMajor),
		Acknowledged:     true,
		AcknowledgedBy:   &by,
		LastValue:        &last,
		Message:          &msg,
		RaisedTime:       raised,
		OccurredTime:     occurred,
	}

	bytes, err := MarshalAlarmStateChangeEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalAlarmStateChangeEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, got.OccurredTime.Equal(occurred), "occurred time round-trip (nanos): got %s want %s", got.OccurredTime, occurred)
	assert.True(t, got.RaisedTime.Equal(raised), "raised time round-trip (nanos): got %s want %s", got.RaisedTime, raised)
	// Zero the times so the rest of the struct can be compared by value.
	event.OccurredTime, got.OccurredTime = time.Time{}, time.Time{}
	event.RaisedTime, got.RaisedTime = time.Time{}, time.Time{}
	assert.Equal(t, event, got)

	// An event with the optional fields absent round-trips with nil pointers and an
	// empty previous severity (a first raise / auto-clear carries neither).
	bare := &model.AlarmStateChangeEvent{
		EventType:      model.AlarmEventRaised,
		AlarmToken:     "a2",
		OriginatorType: "device",
		OriginatorId:   1,
		AlarmKey:       "k",
		MetricKey:      "m",
		State:          string(model.AlarmStateActive),
		Severity:       string(model.AlarmSeverityWarning),
		OccurredTime:   occurred,
	}
	bytes, err = MarshalAlarmStateChangeEvent(bare)
	assert.NoError(t, err)
	gotBare, err := UnmarshalAlarmStateChangeEvent(bytes)
	assert.NoError(t, err)
	assert.Nil(t, gotBare.AcknowledgedBy)
	assert.Nil(t, gotBare.LastValue)
	assert.Nil(t, gotBare.Message)
	assert.Equal(t, "", gotBare.PreviousSeverity)
}

// The entity-deletion envelope must survive the marshal/unmarshal round trip
// (ADR-044): event-management keys anchor cleanup on the entity id, and the token +
// deleted time are carried for logging/future reshape, so a dropped field would
// misfire or lose the cleanup.
func TestMarshalDetectionRulesPublishedEventRoundTrips(t *testing.T) {
	// The publish time must survive with sub-second precision: it is the rule-activation
	// half of the dead-man grace-period base (ADR-051 slice 4c-2), and a dropped or
	// rounded value would shift when a never-reported device's absence deadline elapses.
	publishedAt := time.Date(2026, 7, 10, 9, 15, 0, 123456789, time.UTC)
	event := &model.DetectionRulesPublishedEvent{
		ProfileVersionToken: "sensor-profile@3",
		Rules: []model.PublishedDetectionRule{
			// A scoped rule (ADR-062 S4) must carry its group@version pin through the fact.
			{Token: "overheat", Definition: `{"name":"overheat","type":"threshold","when":{"metric":"t","op":"gt","threshold":80}}`,
				EntityGroupToken: "arid-areas", EntityGroupVersion: 4},
			// An unscoped rule carries an empty token / version 0.
			{Token: "flatline", Definition: `{"name":"flatline","type":"absence","timeout":"5m"}`},
		},
		PublishedAt: publishedAt,
	}
	bytes, err := MarshalDetectionRulesPublishedEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalDetectionRulesPublishedEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "sensor-profile@3", got.ProfileVersionToken)
	assert.Equal(t, event.Rules, got.Rules, "every rule's token + opaque definition must round-trip in order")
	assert.True(t, got.PublishedAt.Equal(publishedAt), "published-at round-trip (nanos): got %s want %s", got.PublishedAt, publishedAt)
}

// The device-roster envelope must survive the marshal/unmarshal round trip (ADR-051
// slice 4c-2): the device + stable profile token drive which absence rules an unseen
// device is armed under, and expected-since is the dead-man clock base — a dropped
// field would arm the wrong rule set or shift the deadline.
func TestMarshalDeviceRosterEventRoundTrips(t *testing.T) {
	since := time.Date(2026, 7, 10, 8, 0, 0, 987654321, time.UTC)
	event := &model.DeviceRosterEvent{
		DeviceToken:   "device-001",
		ProfileToken:  "sensor-profile",
		ExpectedSince: since,
	}
	bytes, err := MarshalDeviceRosterEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalDeviceRosterEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "device-001", got.DeviceToken)
	assert.Equal(t, "sensor-profile", got.ProfileToken)
	assert.True(t, got.ExpectedSince.Equal(since), "expected-since round-trip (nanos): got %s want %s", got.ExpectedSince, since)
}

func TestMarshalDeviceAttributeEventRoundTrips(t *testing.T) {
	at := time.Date(2026, 7, 10, 9, 30, 0, 123456789, time.UTC)
	event := &model.DeviceAttributeEvent{
		DeviceToken: "device-001",
		AttrKey:     "maxTemp",
		Scope:       "SERVER",
		Value:       72.5,
		Removed:     false,
		UpdatedAt:   at,
	}
	bytes, err := MarshalDeviceAttributeEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalDeviceAttributeEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "device-001", got.DeviceToken)
	assert.Equal(t, "maxTemp", got.AttrKey)
	assert.Equal(t, "SERVER", got.Scope)
	assert.Equal(t, 72.5, got.Value)
	assert.False(t, got.Removed)
	assert.True(t, got.UpdatedAt.Equal(at), "updated-at round-trip (nanos): got %s want %s", got.UpdatedAt, at)
}

// A genuine upsert of the value 0.0 (Removed=false) round-trips unambiguously: proto3
// elides both the zero double and the false bool on the wire, but Removed stays false so
// the consumer reads a real zero threshold, not a removal.
func TestMarshalDeviceAttributeZeroValueIsNotRemoval(t *testing.T) {
	event := &model.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "floor", Scope: "SHARED", Value: 0.0, Removed: false}
	bytes, err := MarshalDeviceAttributeEvent(event)
	assert.NoError(t, err)
	got, err := UnmarshalDeviceAttributeEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, 0.0, got.Value)
	assert.False(t, got.Removed, "a genuine zero value is not a removal")
}

// A removal event round-trips: removed=true, no value, and the write time preserved.
func TestMarshalDeviceAttributeRemovalRoundTrips(t *testing.T) {
	at := time.Date(2026, 7, 10, 9, 31, 0, 0, time.UTC)
	event := &model.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "maxTemp", Removed: true, UpdatedAt: at}
	bytes, err := MarshalDeviceAttributeEvent(event)
	assert.NoError(t, err)
	got, err := UnmarshalDeviceAttributeEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, got.Removed)
	assert.Equal(t, 0.0, got.Value)
	assert.True(t, got.UpdatedAt.Equal(at))
}

// A roster event for a device whose type has no profile (empty profile token) and a
// detection-rules event with no publish time both round-trip: the empty/zero optional
// fields encode as absent and decode back to their zero values, not to a spurious
// year-1 instant or a decode error.
func TestMarshalOptionalTimeAndProfileAbsent(t *testing.T) {
	roster := &model.DeviceRosterEvent{DeviceToken: "d1", ProfileToken: "", ExpectedSince: time.Time{}}
	bytes, err := MarshalDeviceRosterEvent(roster)
	assert.NoError(t, err)
	got, err := UnmarshalDeviceRosterEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "", got.ProfileToken)
	assert.True(t, got.ExpectedSince.IsZero(), "absent expected-since must decode to the zero time, got %s", got.ExpectedSince)

	rules := &model.DetectionRulesPublishedEvent{ProfileVersionToken: "p@1"}
	bytes, err = MarshalDetectionRulesPublishedEvent(rules)
	assert.NoError(t, err)
	gotRules, err := UnmarshalDetectionRulesPublishedEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, gotRules.PublishedAt.IsZero(), "absent published-at must decode to the zero time, got %s", gotRules.PublishedAt)
}

// An empty rule set (a profile published with no enabled detection rules) round-trips to an
// empty, non-panicking slice.
func TestMarshalDetectionRulesPublishedEventEmpty(t *testing.T) {
	bytes, err := MarshalDetectionRulesPublishedEvent(&model.DetectionRulesPublishedEvent{ProfileVersionToken: "p@1"})
	assert.NoError(t, err)
	got, err := UnmarshalDetectionRulesPublishedEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, "p@1", got.ProfileVersionToken)
	assert.Empty(t, got.Rules)
}

func TestMarshalEntityDeletedEventRoundTrips(t *testing.T) {
	when := time.Date(2026, 7, 6, 8, 30, 0, 123456789, time.UTC)
	event := &model.EntityDeletedEvent{
		EntityType:  entity.TypeCustomer,
		EntityId:    4242,
		EntityToken: "acme-corp",
		DeletedTime: when,
	}
	bytes, err := MarshalEntityDeletedEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalEntityDeletedEvent(bytes)
	assert.NoError(t, err)
	assert.Equal(t, entity.TypeCustomer, got.EntityType)
	assert.Equal(t, uint(4242), got.EntityId)
	assert.Equal(t, "acme-corp", got.EntityToken)
	assert.True(t, got.DeletedTime.Equal(when), "deleted time must round-trip")
}
