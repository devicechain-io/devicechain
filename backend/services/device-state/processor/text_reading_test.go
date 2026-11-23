// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

func readingEvent(entries ...resolvedevent.ResolvedMeasurementEntry) *resolvedevent.ResolvedEvent {
	return &resolvedevent.ResolvedEvent{
		EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{
			{OccurredTime: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), Entries: entries}}},
	}
}

// A STRING reading keeps its text (even text that parses as a number), a numeric reading keeps
// its number, and what is neither, or cannot be stored, is skipped without taking its neighbours.
func TestMeasurementInputsKeepATextReadingAsText(t *testing.T) {
	in := measurementInputs(readingEvent(
		resolvedevent.ResolvedMeasurementEntry{Name: "mode", Value: "heating", DataType: strp("STRING")},
		resolvedevent.ResolvedMeasurementEntry{Name: "code", Value: "12", DataType: strp("STRING")},
		resolvedevent.ResolvedMeasurementEntry{Name: "temp", Value: "21.5", DataType: strp("DOUBLE")},
		resolvedevent.ResolvedMeasurementEntry{Name: "undeclared", Value: "7"},
		resolvedevent.ResolvedMeasurementEntry{Name: "words", Value: "not a number"},
		resolvedevent.ResolvedMeasurementEntry{Name: "nul", Value: "a\x00b", DataType: strp("STRING")},
		resolvedevent.ResolvedMeasurementEntry{Name: "bad", Value: "a\xffb", DataType: strp("STRING")},
		resolvedevent.ResolvedMeasurementEntry{Name: "big", Value: strings.Repeat("x", resolvedevent.MaxStringValueBytes+1), DataType: strp("STRING")},
	))
	byName := map[string]model.LatestMeasurementInput{}
	for _, m := range in {
		byName[m.Name] = m
	}
	require.Len(t, byName, 4, "mode, code, temp and undeclared are kept; the rest are skipped")

	require.Equal(t, "heating", *byName["mode"].TextValue)
	require.False(t, byName["mode"].Value.Valid)
	require.Equal(t, "12", *byName["code"].TextValue, "declared STRING stays text although it parses")
	require.False(t, byName["code"].Value.Valid)
	require.Nil(t, byName["temp"].TextValue)
	require.Equal(t, 21.5, byName["temp"].Value.Float64)
	require.Nil(t, byName["undeclared"].TextValue)
	require.Equal(t, 7.0, byName["undeclared"].Value.Float64)
}
