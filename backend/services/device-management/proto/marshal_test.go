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
// round-trip: they cross the messaging hop to every downstream consumer
// (event-management persistence, device-state's latest-value + connectivity
// projections), and a dropped timestamp lands as the zero time (0001-01-01),
// corrupting the hypertable partition key and the last-activity/updated columns.
func TestMarshalResolvedEventCarriesTimestamps(t *testing.T) {
	occurred := time.Now().UTC().Truncate(time.Second)
	processed := occurred.Add(50 * time.Millisecond).Truncate(time.Second)

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
