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
		RelationshipId: 5,
		OccurredTime:   occurred,
		ProcessedTime:  processed,
		EventType:      esmodel.Measurement,
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
}
