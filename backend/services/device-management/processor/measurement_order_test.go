// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 WHY THESE TESTS EXIST, and why the rest of the measurement suite could not have
// caught what they cover.
//
// event-management derives an event's IDENTITY from json.Marshal of the resolved
// payload (DeriveEventId), and that identity is the primary key that makes a redelivery
// idempotent. json.Marshal of a SLICE preserves slice order, so the order this resolver
// emits measurements in IS part of the event's identity — a varying order is a varying
// id, a missed ON CONFLICT, and a double-persisted event with doubled rollup sums.
//
// Every pre-existing measurement fixture resolves to AT MOST ONE SURVIVING ENTRY, so the
// whole suite was structurally incapable of observing the randomisation — it could not have
// failed no matter how wrong the ordering was. That happens two ways, and the second is the
// one worth remembering: measurementEvent() puts a single key in the map, and the two
// fixtures that DO carry two keys (event_resolver_test.go:494 and :522) pair a numeric key
// with a non-storable one that resolution drops, leaving one entry again. A multi-key
// fixture is therefore not sufficient on its own — the entries have to SURVIVE.
//
// The stability contract these support — that repeated resolutions encode identically — is
// asserted for every payload type in payload_canonical_test.go. What lives here is the part
// that is specific to measurements: which order, and which slice must NOT be ordered.

// The metric names every fixture in this package uses. Deliberately NOT in alphabetical
// order as written, deliberately more than one, and all five numeric so all five SURVIVE
// resolution. The values are distinct so a test that mixed up entries reads as wrong rather
// than as equal.
var orderFixture = map[string]string{
	"temperature": "21.5",
	"humidity":    "48",
	"pressure":    "1013",
	"voltage":     "3.7",
	"amperage":    "0.4",
}

// fixtureTime is the instant these fixtures are anchored to. A real value, not the zero
// time, so "the sample kept its own instant" is distinguishable from "the instant was
// dropped".
var fixtureTime = time.Date(2026, 8, 14, 9, 15, 30, 123456789, time.UTC)

// An api that declares no metric definitions, so numeric fixtures resolve unclassified and
// lenient (ADR-016). That keeps these tests on ORDER rather than on classification.
func noMetricDefsApi() model.DeviceManagementApi {
	api := new(dmtest.MockApi)
	api.Mock.On("MetricDefinitionsByDeviceType").Return([]*model.MetricDefinition{}, nil)
	return api
}

func orderResolver() *EventResolver {
	return NewEventResolver(1, noMetricDefsApi(), config.AuthModeOptional, EventTimePolicy{},
		nil, nil, nil, nil, nil, nil)
}

// Resolve one multi-metric sample and hand back the entries of its single sample.
func resolveMeasurements(t *testing.T, ms map[string]string) []model.ResolvedMeasurementEntry {
	t.Helper()
	event := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: ms}},
		},
	}
	out, err := orderResolver().ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, event)
	require.NoError(t, err)
	payload, ok := out.(*model.ResolvedMeasurementsPayload)
	require.True(t, ok, "expected *model.ResolvedMeasurementsPayload, got %T", out)
	require.Len(t, payload.Entries, 1)
	return payload.Entries[0].Entries
}

// The resolver emits a sample's measurements in name order.
//
// 🔑 THIS IS NOT SUBSUMED BY THE STABILITY TEST, and the distinction is the whole point: a
// resolver that produced a per-PROCESS stable but arbitrary order would satisfy stability
// within one run and still derive a different id after a restart. Resolution happens again
// on the other side of a crash — that is the redelivery this exists for — so the order has
// to be the SAME order, not merely a repeatable one. Only pinning the actual sequence says
// that.
func TestMeasurementsResolveInNameOrder(t *testing.T) {
	entries := resolveMeasurements(t, orderFixture)

	// The fixture must be able to express a wrong order at all. One surviving entry has
	// only one possible arrangement, which is precisely the blind spot that let this ship.
	require.Len(t, entries, len(orderFixture),
		"the fixture lost entries in resolution; with fewer survivors than keys this test is "+
			"measuring something other than order")

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"amperage", "humidity", "pressure", "temperature", "voltage"}, names,
		"measurements must resolve in name order; event-management hashes this slice's order into the event id")
}

// 🔴 THE COUNTERPART TO THE SORT: the OUTER slice must NOT be sorted.
//
// A payload's outer entries are the device's own samples, in the order the device sent
// them, and that order is its own datum — each sample carries its own instant, DETECT folds
// them into windowed accumulators in slice order, and reordering them changes what a
// windowed rule computes. The wire preserves it (the unresolved payload's Entries is a
// SLICE, unlike the map inside each entry), so unlike the inner order there is a real device
// order here to keep.
//
// This exists because a mutation that sorted the outer slice too SURVIVED the whole suite:
// every other multi-sample fixture in the repo happens to carry samples whose names are
// already ascending, so a sort was a no-op on all of them and the gap was invisible. The
// fixture below is deliberately DESCENDING by first name, and dated out of order as well, so
// any rule imposed on the outer slice — alphabetical or chronological — moves it.
func TestTheDeviceSampleOrderIsNotSorted(t *testing.T) {
	earlier := fixtureTime.Add(-time.Minute)
	event := &esmodel.UnresolvedEvent{
		Device: "TEST-123", EventType: esmodel.Measurement, OccurredTime: fixtureTime,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{
				// First sample: names sort LAST, instant sorts LAST.
				{OccurredTime: &fixtureTime, Measurements: map[string]string{"voltage": "3.7", "temperature": "21.5"}},
				// Second sample: names sort FIRST, instant sorts FIRST.
				{OccurredTime: &earlier, Measurements: map[string]string{"amperage": "0.4", "humidity": "48"}},
			},
		},
	}

	out, err := orderResolver().ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, event)
	require.NoError(t, err)
	payload := out.(*model.ResolvedMeasurementsPayload)
	require.Len(t, payload.Entries, 2)

	// Within each sample the measurements ARE sorted — that half is the fix.
	assert.Equal(t, "temperature", payload.Entries[0].Entries[0].Name)
	assert.Equal(t, "amperage", payload.Entries[1].Entries[0].Name)

	// Across samples nothing is sorted: sample 0 is still the one the device sent first,
	// even though both an alphabetical and a chronological rule would put it second.
	assert.True(t, payload.Entries[0].OccurredTime.Equal(fixtureTime),
		"the device's sample order changed: sample 0 came back at %v, want %v",
		payload.Entries[0].OccurredTime, fixtureTime)
	assert.True(t, payload.Entries[1].OccurredTime.Equal(earlier),
		"the device's sample order changed: sample 1 came back at %v, want %v",
		payload.Entries[1].OccurredTime, earlier)
}

// The single-metric CONTROL. It was already stable before the fix — one key has one order —
// so it must stay stable after. Its job is to fail if the tests above ever start passing for
// a reason unrelated to ordering: a resolver that dropped every measurement would make the
// encodings identical AND empty.
func TestSingleMeasurementResolutionIsStableAndNonEmpty(t *testing.T) {
	entries := resolveMeasurements(t, map[string]string{"temperature": "21.5"})

	require.Len(t, entries, 1, "the resolver dropped a valid undeclared numeric measurement")
	assert.Equal(t, "temperature", entries[0].Name)
	assert.Equal(t, "21.5", entries[0].Value)
}
