// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
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

	event := &model.ResolvedEvent{
		Source:         "http1",
		SourceDeviceId: 4,
		Anchors: []model.ResolvedAnchor{
			{AnchorType: "customer", AnchorId: 3, RelationshipId: 5},
			{AnchorType: "area", AnchorId: 9, RelationshipId: 8},
		},
		OccurredTime:  occurred,
		ProcessedTime: processed,
		EventType:     esmodel.Measurement,
		Payload: &model.ResolvedMeasurementsPayload{
			Entries: []model.ResolvedMeasurementsEntry{
				{Entries: []model.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5"}}},
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
}

// An alarm state-change event (ADR-041) must survive the marshal/unmarshal round-trip
// intact: it crosses the messaging hop to the subscription bridge (2.E) and
// notification consumers, and every field drives what a subscriber renders. The
// optional scalars must round-trip as present (not collapsed to a zero value) so a
// de-escalation's prior severity and a cleared alarm's last value are preserved.
func TestMarshalAlarmStateChangeEventRoundTrips(t *testing.T) {
	occurred := time.Now().UTC().Truncate(time.Second)
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
		OccurredTime:     occurred,
	}

	bytes, err := MarshalAlarmStateChangeEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalAlarmStateChangeEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, got.OccurredTime.Equal(occurred), "occurred time round-trip: got %s want %s", got.OccurredTime, occurred)
	// Zero the times so the rest of the struct can be compared by value.
	event.OccurredTime, got.OccurredTime = time.Time{}, time.Time{}
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
