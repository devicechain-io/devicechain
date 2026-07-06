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
	occurred := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	classifier := uint64(3)
	resolved := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "device-4",
		OccurredTime:      occurred,
		EventType:         esmodel.Measurement,
	}

	me := measurementFromResolved(resolved, dmmodel.ResolvedMeasurementEntry{
		Name:       "temperature",
		Value:      "21.5",
		Classifier: &classifier,
	})

	assert.Equal(t, "device-4", me.DeviceToken)
	assert.Equal(t, esmodel.Measurement, me.EventType)
	assert.Equal(t, occurred, me.OccurredTime)
	assert.Equal(t, "temperature", me.Name)
	require.True(t, me.Value.Valid)
	assert.InDelta(t, 21.5, me.Value.Float64, 1e-9)
	require.NotNil(t, me.Classifier)
	assert.Equal(t, uint(3), *me.Classifier)
}

// A non-numeric measurement value leaves Value null rather than erroring, so one
// bad reading does not drop the stream.
func TestMeasurementFromResolvedNonNumeric(t *testing.T) {
	me := measurementFromResolved(&dmmodel.ResolvedEvent{SourceDeviceToken: "device-1"}, dmmodel.ResolvedMeasurementEntry{
		Name:  "state",
		Value: "OPEN",
	})
	assert.Equal(t, "state", me.Name)
	assert.False(t, me.Value.Valid)
	assert.Nil(t, me.Classifier)
}
