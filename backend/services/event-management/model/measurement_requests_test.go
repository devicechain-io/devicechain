// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The string to row mapping lives here so the persistence worker and a backfill build a row the
// same way. The worker's own tests (processor) pin its behaviour through it.
func TestMeasurementRequestsForMapsEveryReading(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cls, unit, dt := uint64(7), "W", "DOUBLE"
	ev := Event{}
	reqs, err := MeasurementRequestsFor(ev, dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
		{OccurredTime: at, Entries: []dmmodel.ResolvedMeasurementEntry{
			{Name: "a", Value: "1.5"},
			{Name: "power", Value: "9", Classifier: &cls, Unit: &unit, DataType: &dt, Derived: true},
		}},
		{OccurredTime: at.Add(time.Second), Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "a", Value: "-2"}}},
	}})
	require.NoError(t, err)
	require.Len(t, reqs, 3)
	assert.Equal(t, "a", reqs[0].Name)
	assert.Equal(t, 1.5, *reqs[0].Value)
	assert.Nil(t, reqs[0].Classifier)
	assert.False(t, reqs[0].Derived)
	assert.Equal(t, at, reqs[0].EntryOccurredTime)
	assert.Equal(t, uint(7), *reqs[1].Classifier)
	assert.Equal(t, &unit, reqs[1].Unit)
	assert.Equal(t, &dt, reqs[1].DataType)
	assert.True(t, reqs[1].Derived)
	assert.Equal(t, at.Add(time.Second), reqs[2].EntryOccurredTime)
}

func TestMeasurementRequestsForRefusesWhatCannotBeStored(t *testing.T) {
	for _, bad := range []string{"NaN", "+Inf", "-Inf", "abc", ""} {
		_, err := MeasurementRequestsFor(Event{}, dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
			{Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "m", Value: bad}}}}})
		require.Error(t, err, bad)
		assert.True(t, errors.Is(err, ErrDeterministic), bad)
	}
}
