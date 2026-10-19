// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-sources/adapter"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The live sample is every measurement of a message that shares an instant, not one payload
// entry: Sparkplug and LwM2M ingest emit ONE entry per metric, and a formula over two of them
// must still compute.

func resolveEntries(t *testing.T, profile model.ProfileResolution, entries ...esmodel.UnresolvedMeasurementsEntry) *resolvedevent.ResolvedMeasurementsPayload {
	t.Helper()
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.calcObs.metrics = testCalcMetrics()
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	event := &esmodel.UnresolvedEvent{Source: "test", Device: "meter-1", EventType: esmodel.Measurement,
		OccurredTime: at, ProcessedTime: at.Add(time.Hour),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: entries}}
	out, err := rez.ResolveMeasurementsEventPayload(core.WithTenant(context.Background(), "acme"), device, profile, event)
	require.NoError(t, err)
	return out.(*resolvedevent.ResolvedMeasurementsPayload)
}

func entry(at *time.Time, kv ...string) esmodel.UnresolvedMeasurementsEntry {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return esmodel.UnresolvedMeasurementsEntry{Measurements: m, OccurredTime: at}
}

func derivedValues(p *resolvedevent.ResolvedMeasurementsPayload) map[string]string {
	out := map[string]string{}
	for _, s := range p.Entries {
		for _, e := range s.Entries {
			if e.Derived {
				out[e.Name] = e.Value
			}
		}
	}
	return out
}

func powerProfile() model.ProfileResolution {
	return meterResolution(
		calculatedDef(5, "power", model.MetricDouble, "W", "voltage * current"),
		calculatedDef(6, "power_kw", model.MetricDouble, "kW", "power / 1000.0"),
	)
}

// One entry per metric, no entry time (the shape Sparkplug and LwM2M ingest emit): the
// envelope instant is shared, so the two form one sample and the chain computes.
func TestPerMetricEntriesSharingTheEnvelopeInstantFormOneSample(t *testing.T) {
	got := resolveEntries(t, powerProfile(), entry(nil, "voltage", "230"), entry(nil, "current", "2"))
	require.Len(t, got.Entries, 2, "no entry is added or merged")
	assert.Equal(t, map[string]string{"power": "460", "power_kw": "0.46"}, derivedValues(got))
}

// Separate entries carrying equal explicit instants (distinct pointers) group too.
func TestSeparateEntriesAtTheSameExplicitInstantFormOneSample(t *testing.T) {
	a, b := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	got := resolveEntries(t, powerProfile(), entry(&a, "voltage", "230"), entry(&b, "current", "2"))
	assert.Equal(t, map[string]string{"power": "460", "power_kw": "0.46"}, derivedValues(got))
	derived := 0
	for _, s := range got.Entries {
		for _, e := range s.Entries {
			if e.Derived {
				derived++
			}
		}
	}
	assert.Equal(t, 2, derived, "each derived value is produced once, not once per entry")
	// Carried by the sample's first entry.
	assert.Contains(t, names(got.Entries[0].Entries), "power*")
	assert.NotContains(t, names(got.Entries[1].Entries), "power*")
}

// Distinct instants stay distinct samples: no value is computed across them.
func TestEntriesAtDistinctInstantsAreSeparateSamples(t *testing.T) {
	a, b := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 11, 0, 1, 0, time.UTC)
	got := resolveEntries(t, powerProfile(), entry(&a, "voltage", "230"), entry(&b, "current", "2"))
	assert.Empty(t, derivedValues(got))
}

// Mixed instants: each instant's inputs are computed together; an out-of-order entry still
// joins its instant's sample.
func TestMixedInstantsGroupByInstant(t *testing.T) {
	a, b := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 11, 0, 1, 0, time.UTC)
	got := resolveEntries(t, powerProfile(),
		entry(&a, "voltage", "10"), entry(&b, "voltage", "20"), entry(&a, "current", "3"), entry(&b, "current", "4"))
	require.Len(t, got.Entries, 4)
	byTime := map[time.Time]string{}
	for _, s := range got.Entries {
		for _, e := range s.Entries {
			if e.Name == "power" {
				byTime[s.OccurredTime] = e.Value
			}
		}
	}
	assert.Equal(t, map[time.Time]string{a: "30", b: "80"}, byTime)
}

// The same metric twice at one instant: the formula reads the FIRST value in message order.
func TestADuplicateNameAtOneInstantKeepsTheFirstValue(t *testing.T) {
	got := resolveEntries(t, powerProfile(),
		entry(nil, "voltage", "230"), entry(nil, "voltage", "999"), entry(nil, "current", "2"))
	assert.Equal(t, "460", derivedValues(got)["power"])
}

// A device-sent value under a calculated key is still dropped, in whichever entry it arrives.
func TestADeviceSentCalculatedKeyInALaterEntryIsDropped(t *testing.T) {
	got := resolveEntries(t, powerProfile(), entry(nil, "voltage", "230"), entry(nil, "current", "2", "power", "1"))
	assert.Equal(t, "460", derivedValues(got)["power"])
	for _, s := range got.Entries {
		for _, e := range s.Entries {
			if e.Name == "power" {
				assert.True(t, e.Derived, "only the computed power survives")
			}
		}
	}
}

// An entry with no time of its own is dated at the envelope instant, so it joins an entry that
// names that same instant explicitly: grouping keys on the BOUNDED instant, not on whether the
// device sent one.
func TestAnUntimedEntryJoinsAnExplicitEntryAtTheEnvelopeInstant(t *testing.T) {
	envelope := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) // resolveEntries' event time
	got := resolveEntries(t, powerProfile(), entry(nil, "voltage", "230"), entry(&envelope, "current", "2"))
	assert.Equal(t, map[string]string{"power": "460", "power_kw": "0.46"}, derivedValues(got))
}

// What the Sparkplug and LwM2M hosts actually publish: one entry per metric, each with its OWN
// millisecond instant (Sparkplug: the metric timestamp, else the payload's; LwM2M: the SenML
// record's resolved time), built by the shared emitter and carried over the wire codec. Metrics
// stamped at the same millisecond form one sample; metrics stamped even a millisecond apart are
// separate samples and a formula over both does not compute — the instant is matched exactly.
func TestIngestShapedEventsThroughTheEmitterAndWire(t *testing.T) {
	ms := time.Date(2026, 10, 16, 11, 59, 0, 0, time.UTC).UnixMilli()
	for _, tc := range []struct {
		name    string
		samples []adapter.Sample
		want    map[string]string
	}{
		{"same millisecond", []adapter.Sample{{Name: "voltage", Value: 230, Time: ms}, {Name: "current", Value: 2, Time: ms}},
			map[string]string{"power": "460", "power_kw": "0.46"}},
		{"three milliseconds apart", []adapter.Sample{{Name: "voltage", Value: 230, Time: ms}, {Name: "current", Value: 2, Time: ms + 3}},
			map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			msgs, _, err := adapter.NewEmitter(nil, func() time.Time { return now }, "sp", false).
				BuildMeasurements("acme", "sparkplug", "meter-1", tc.samples)
			require.NoError(t, err)
			require.Len(t, msgs, 1)
			event, err := esproto.UnmarshalUnresolvedEvent(msgs[0].Value)
			require.NoError(t, err)
			rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
			rez.calcObs.metrics = testCalcMetrics()
			device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
			out, err := rez.ResolveMeasurementsEventPayload(core.WithTenant(context.Background(), "acme"), device, powerProfile(), event)
			require.NoError(t, err)
			assert.Equal(t, tc.want, derivedValues(out.(*resolvedevent.ResolvedMeasurementsPayload)))
		})
	}
}
