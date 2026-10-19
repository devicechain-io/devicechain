// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The string to row mapping lives here so the persistence worker and a backfill build a row the
// same way. The worker's own tests (processor) pin its behaviour through it.
func TestMeasurementRequestForMapsEveryField(t *testing.T) {
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cls, unit, dt := uint64(7), "W", "DOUBLE"
	ev := Event{DeviceToken: "d-1"}

	plain, err := MeasurementRequestFor(ev, at, MeasurementReading{Name: "a", Value: "1.5"})
	require.NoError(t, err)
	assert.Equal(t, "a", plain.Name)
	assert.Equal(t, 1.5, *plain.Value)
	assert.Nil(t, plain.Classifier)
	assert.Nil(t, plain.Unit)
	assert.Nil(t, plain.DataType)
	assert.False(t, plain.Derived)
	assert.Equal(t, at, plain.EntryOccurredTime)
	assert.Equal(t, "d-1", plain.Event.DeviceToken)

	derived, err := MeasurementRequestFor(ev, at.Add(time.Second),
		MeasurementReading{Name: "power", Value: "-9", Classifier: &cls, Unit: &unit, DataType: &dt, Derived: true})
	require.NoError(t, err)
	assert.Equal(t, -9.0, *derived.Value)
	assert.Equal(t, uint(7), *derived.Classifier)
	assert.Equal(t, &unit, derived.Unit)
	assert.Equal(t, &dt, derived.DataType)
	assert.True(t, derived.Derived)
	assert.Equal(t, at.Add(time.Second), derived.EntryOccurredTime)
}

func TestMeasurementRequestForRefusesWhatCannotBeStored(t *testing.T) {
	for _, bad := range []string{"NaN", "+Inf", "-Inf", "abc", ""} {
		_, err := MeasurementRequestFor(Event{}, time.Time{}, MeasurementReading{Name: "m", Value: bad})
		require.Error(t, err, bad)
		assert.True(t, errors.Is(err, ErrDeterministic), bad)
	}
}
