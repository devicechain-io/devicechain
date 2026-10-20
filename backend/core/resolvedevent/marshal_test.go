// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	event := &ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   "device-001",
		DeviceTypeToken:     "sensor-type",
		ProfileVersionToken: "temp-profile@3",
		Anchors: []ResolvedAnchor{
			{AnchorType: "customer", AnchorToken: "acme", RelationshipId: 5},
			{AnchorType: "area", AnchorToken: "warehouse-3", RelationshipId: 8},
		},
		ScopeMemberships: []GroupRef{
			{GroupToken: "arid-areas", Version: 2},
			{GroupToken: "beta-fleet", Version: 1},
		},
		OccurredTime:  occurred,
		ProcessedTime: processed,
		EventType:     eventtype.Measurement,
		Payload: &ResolvedMeasurementsPayload{
			Entries: []ResolvedMeasurementsEntry{
				{Entries: []ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5",
					Classifier: &classifier, Unit: &unit, DataType: &dataType}}},
			},
		},
	}

	bytes, err := MarshalResolvedEventAs(event, eventtime.WireCompact)
	require.NoError(t, err)

	got, err := UnmarshalResolvedEvent(bytes)
	require.NoError(t, err)
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
	event := &ResolvedEvent{
		Source:            "http1",
		SourceDeviceToken: "device-001",
		OccurredTime:      now,
		ProcessedTime:     now,
		EventType:         eventtype.Measurement,
		Payload:           &ResolvedMeasurementsPayload{},
	}

	bytes, err := MarshalResolvedEventAs(event, eventtime.WireCompact)
	require.NoError(t, err)
	got, err := UnmarshalResolvedEvent(bytes)
	require.NoError(t, err)
	assert.Equal(t, "", got.DeviceTypeToken)
	assert.Equal(t, "", got.ProfileVersionToken)
}
