// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE HOP THE ORDERING FIX DEPENDS ON, AND WHICH NOTHING PREVIOUSLY TESTED.
//
// The resolver sorts a sample's measurements by name because event-management derives
// the event's identity from the ORDER of that slice. But the resolver and the digest sit
// in different services: the payload is marshalled to protobuf, published, and decoded
// again before anything hashes it. So the sort is only worth having while this hop
// preserves order.
//
// It does today because the resolved measurements travel as a `repeated` field. Nothing
// asserted that. Had it been declared a proto `map` — the shape the UNRESOLVED side
// actually uses one hop earlier, which makes it a plausible thing to copy — order would
// be destroyed in transit, the resolver's sort would silently buy nothing, and the event
// id would go back to varying per resolution with the fix apparently still in place.
//
// The sibling to this test is the whole reason it is worth writing: the location payload
// blind spot survived for months because the round trip that "covered" it carried an
// EMPTY Entries slice.

// 🔴 THE FIXTURE IS DELIBERATELY IN DESCENDING NAME ORDER, and that is what makes the test
// able to fail. An already-sorted fixture cannot distinguish "the transport preserved my
// order" from "the transport imposed its own" — a wire that sorted would return exactly the
// same bytes, and the assertion would pass while proving nothing. The one order the
// transport must not be allowed to agree with by luck is the resolver's own.
//
// The denormalized fields ride on one entry and not the others, so a shuffle that happened
// to preserve names is still caught by which entry carries the classifier.
func descendingSample(occurred time.Time) *model.ResolvedMeasurementsPayload {
	classifier := uint64(42)
	unit := "Cel"
	dataType := "DOUBLE"
	return &model.ResolvedMeasurementsPayload{
		Entries: []model.ResolvedMeasurementsEntry{{
			Entries: []model.ResolvedMeasurementEntry{
				{Name: "voltage", Value: "3.7"},
				{Name: "temperature", Value: "21.5", Classifier: &classifier, Unit: &unit, DataType: &dataType},
				{Name: "pressure", Value: "1013"},
				{Name: "humidity", Value: "48"},
				{Name: "amperage", Value: "0.4"},
			},
			OccurredTime: occurred,
		}},
	}
}

// Measurements must come back in exactly the order they went in, values and denormalized
// fields still attached to their own names.
func TestMeasurementPayloadRoundTripCarriesTheProducersOrderVerbatim(t *testing.T) {
	occurred := at(t, "2026-08-09T14:32:07.125Z")

	encoded, err := MarshalResolvedPayload(esmodel.Measurement, descendingSample(occurred))
	require.NoError(t, err)

	generic, err := UnmarshalResolvedPayload(esmodel.Measurement, encoded, time.Time{})
	require.NoError(t, err)

	decoded, ok := generic.(*model.ResolvedMeasurementsPayload)
	require.True(t, ok, "expected *model.ResolvedMeasurementsPayload, got %T", generic)
	require.Len(t, decoded.Entries, 1)

	got := decoded.Entries[0].Entries
	require.Len(t, got, 5, "the round trip dropped measurements, so any order assertion below is moot")

	names := make([]string, 0, len(got))
	values := make([]string, 0, len(got))
	for _, e := range got {
		names = append(names, e.Name)
		values = append(values, e.Value)
	}
	assert.Equal(t, []string{"voltage", "temperature", "pressure", "humidity", "amperage"}, names,
		"the transport reordered a payload; it must carry the producer's order verbatim, "+
			"otherwise a sort applied at the producer proves nothing about what the digest sees")
	assert.Equal(t, []string{"3.7", "21.5", "1013", "48", "0.4"}, values,
		"each value must still travel with its own name")

	require.NotNil(t, got[1].Classifier, "temperature lost its classifier in transit")
	assert.Equal(t, uint64(42), *got[1].Classifier)
	assert.Nil(t, got[0].Classifier, "an unclassified entry gained a classifier in transit")
}

// Several samples in one message keep THEIR order too — that outer slice is the device's
// own batch sequence, which the resolver deliberately does not sort.
func TestMeasurementRoundTripPreservesSampleOrder(t *testing.T) {
	first := at(t, "2026-08-09T14:32:07.125Z")
	second := at(t, "2026-08-09T14:32:08.250Z")
	payload := &model.ResolvedMeasurementsPayload{
		Entries: []model.ResolvedMeasurementsEntry{
			{Entries: []model.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5"}}, OccurredTime: second},
			{Entries: []model.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.6"}}, OccurredTime: first},
		},
	}

	encoded, err := MarshalResolvedPayload(esmodel.Measurement, payload)
	require.NoError(t, err)
	generic, err := UnmarshalResolvedPayload(esmodel.Measurement, encoded, time.Time{})
	require.NoError(t, err)
	decoded := generic.(*model.ResolvedMeasurementsPayload)

	require.Len(t, decoded.Entries, 2)
	// Deliberately out of time order in the fixture: if the wire (or a future consumer)
	// sorted samples by their instant, this would come back swapped.
	assert.True(t, decoded.Entries[0].OccurredTime.Equal(second),
		"sample order changed in transit: first sample came back at %v, want %v",
		decoded.Entries[0].OccurredTime, second)
	assert.Equal(t, "21.5", decoded.Entries[0].Entries[0].Value)
	assert.Equal(t, "21.6", decoded.Entries[1].Entries[0].Value)
}
