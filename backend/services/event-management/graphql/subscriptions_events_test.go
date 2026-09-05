// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// measurementFromResolved maps a resolved entry onto the same MeasurementEvent
// shape the query returns, so a streamed event and a queried one resolve
// identically. This locks the proto->model mapping (device id, occurred time,
// name, parsed value, classifier) against a drift in the resolved-event shape.
func TestMeasurementFromResolved(t *testing.T) {
	// The sample's instant is DIFFERENT from the message's, and that difference is the
	// point of the test: with both set to the same value, the streamed event would look
	// correct whether it read the sample's time or the envelope's — which is exactly how
	// this surface stamped the wrong one while its own comment promised otherwise.
	envelope := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sample := time.Date(2026, 7, 1, 11, 58, 30, 0, time.UTC)
	classifier := uint64(3)
	resolved := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "device-4",
		OccurredTime:      envelope,
		EventType:         esmodel.Measurement,
	}
	entry := dmmodel.ResolvedMeasurementsEntry{OccurredTime: sample}

	me, err := measurementFromResolved("tenant1", resolved, entry, dmmodel.ResolvedMeasurementEntry{
		Name:       "temperature",
		Value:      "21.5",
		Classifier: &classifier,
	})
	require.NoError(t, err)

	assert.Equal(t, "device-4", me.DeviceToken)
	assert.Equal(t, esmodel.Measurement, me.EventType)
	assert.Equal(t, sample, me.OccurredTime,
		"a streamed reading must carry the SAMPLE's instant, which is what the historian stores for it")
	assert.Equal(t, "temperature", me.Name)
	require.True(t, me.Value.Valid)
	assert.InDelta(t, 21.5, me.Value.Float64, 1e-9)
	require.NotNil(t, me.Classifier)
	assert.Equal(t, uint(3), *me.Classifier)
}

// A non-numeric measurement value leaves Value null rather than erroring, so one
// bad reading does not drop the stream.
func TestMeasurementFromResolvedNonNumeric(t *testing.T) {
	me, err := measurementFromResolved("tenant1", &dmmodel.ResolvedEvent{SourceDeviceToken: "device-1"},
		dmmodel.ResolvedMeasurementsEntry{}, dmmodel.ResolvedMeasurementEntry{
			Name:  "state",
			Value: "OPEN",
		})
	require.NoError(t, err)
	assert.Equal(t, "state", me.Name)
	assert.False(t, me.Value.Valid)
	assert.Nil(t, me.Classifier)
}
