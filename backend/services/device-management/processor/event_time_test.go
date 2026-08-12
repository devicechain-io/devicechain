// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Resolution is the one place the platform decides what instant a reading happened at.
// These tests hold that: the entry-versus-envelope choice, the future bound, and — the
// one that is easiest to leave out — the bound on the ENVELOPE itself.

// boundedResolver builds a resolver with a real event-time policy and a counter to read
// back. No API, device or relation: the location branch reads only event.Payload.
func boundedResolver(skew time.Duration) (*EventResolver, prometheus.Counter) {
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_event_time_bounded_total"})
	policy := EventTimePolicy{MaxFutureSkew: skew, Bounded: counter}
	return NewEventResolver(1, nil, "", policy, nil, nil, nil, nil, nil, nil), counter
}

// timedLocationEvent assembles an unresolved location batch under a known envelope/processed
// time. Each fix's own instant is given as a pointer so nil means "the device timed only
// the message".
func timedLocationEvent(envelope, processed time.Time, fixTimes ...*time.Time) *esmodel.UnresolvedEvent {
	lat, lon := "33.749", "-84.388"
	entries := make([]esmodel.UnresolvedLocationEntry, 0, len(fixTimes))
	for _, at := range fixTimes {
		entries = append(entries, esmodel.UnresolvedLocationEntry{
			Latitude: &lat, Longitude: &lon, OccurredTime: at,
		})
	}
	return &esmodel.UnresolvedEvent{
		Source:        "test",
		Device:        "TEST-123",
		EventType:     esmodel.Location,
		OccurredTime:  envelope,
		ProcessedTime: processed,
		Payload:       &esmodel.UnresolvedLocationsPayload{Entries: entries},
	}
}

func resolveFixes(t *testing.T, rez *EventResolver, event *esmodel.UnresolvedEvent) []model.ResolvedLocationEntry {
	t.Helper()
	resolved, err := rez.ResolveLocationsEventPayload(context.Background(), nil, nil, event)
	require.NoError(t, err)
	payload, ok := resolved.(*model.ResolvedLocationsPayload)
	require.True(t, ok, "expected *model.ResolvedLocationsPayload, got %T", resolved)
	return payload.Entries
}

// A batch keeps every sample's own instant, and a sample with none takes the message's.
//
// This is the fidelity the whole change exists for: a store-and-forward upload spanning a
// minute is a minute of history, not a minute of readings recorded at one instant. Both
// halves are asserted together on purpose — a resolver that stamped every entry with the
// envelope would satisfy the fallback assertion alone.
func TestABatchKeepsEachSamplesOwnInstant(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	envelope := processed.Add(-time.Second)
	first := processed.Add(-90 * time.Second)
	second := processed.Add(-30 * time.Second)

	rez, counter := boundedResolver(5 * time.Minute)
	entries := resolveFixes(t, rez, timedLocationEvent(envelope, processed, &first, &second, nil))
	require.Len(t, entries, 3)

	assert.True(t, entries[0].OccurredTime.Equal(first), "fix 0: got %v want %v", entries[0].OccurredTime, first)
	assert.True(t, entries[1].OccurredTime.Equal(second), "fix 1: got %v want %v", entries[1].OccurredTime, second)
	assert.True(t, entries[2].OccurredTime.Equal(envelope),
		"a fix that reported no time takes the message's: got %v want %v", entries[2].OccurredTime, envelope)
	assert.Zero(t, testutil.ToFloat64(counter), "no reported time led the server clock; nothing should be counted")
}

// A sample time far in the future is replaced with the ceiling, and counted.
//
// 🔴 The per-entry path is not a lesser copy of the envelope path — it is the one an
// attacker would reach for once the envelope is bounded, and it feeds the same
// strictly-newer projections. A bound applied only to the envelope would leave the
// latest-measurement and last-known-position rows poisonable through the entry, with no
// repair path outside a tenant purge.
func TestAPoisonedSampleTimeIsBoundedAndCounted(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	poisoned := processed.Add(72 * time.Hour)
	honest := processed.Add(-time.Minute)

	rez, counter := boundedResolver(skew)
	entries := resolveFixes(t, rez, timedLocationEvent(processed, processed, &poisoned, &honest))
	require.Len(t, entries, 2)

	assert.True(t, entries[0].OccurredTime.Equal(processed.Add(skew)),
		"a far-future sample time must be replaced with the ceiling: got %v", entries[0].OccurredTime)
	// The sibling is the counterweight: a bound that flattened every entry to the ceiling
	// would satisfy the assertion above while destroying the batch.
	assert.True(t, entries[1].OccurredTime.Equal(honest),
		"an honest sample time must pass through untouched: got %v", entries[1].OccurredTime)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter), "exactly the poisoned sample should be counted")
}

// 🔴 THE ENVELOPE IS BOUNDED TOO, AND THIS IS THE TEST THAT MATTERS MOST.
//
// The resolved envelope time is what advances the live connectivity projection's
// last-activity column, under a strictly-newer guard. Leaving it unbounded lets a device
// pin that column with a single timestamp: the inactivity sweep then never fires for it
// again, the device can never be observed to go offline, and nothing resets the row short
// of purging the tenant. That is a device freezing its own presence — and presence is
// precisely the signal command delivery is being taught to trust before it holds a command
// for an unreachable device. A gate reading a frozen projection is worse than no gate.
func TestAPoisonedEnvelopeTimeCannotFreezePresence(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	rez, counter := boundedResolver(skew)

	event := timedLocationEvent(processed.Add(365*24*time.Hour), processed, nil)
	result := rez.MergeToResolveEvent(deviceWithToken("TEST-123"), nil, nil, event, nil, &model.ProfileScope{})

	assert.True(t, result.Resolved.OccurredTime.Equal(processed.Add(skew)),
		"an envelope a year in the future must be replaced with the ceiling: got %v",
		result.Resolved.OccurredTime)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))
	// The server-stamped processed time is NOT bounded and must not be: it is our own
	// clock, it is what the bound is measured against, and it is what makes the bound
	// deterministic under replay.
	assert.True(t, result.Resolved.ProcessedTime.Equal(processed),
		"the server-stamped processed time must pass through untouched")
}

// A non-positive tolerance disables the bound, and that is a real operator choice rather
// than an accident — but it is worth pinning, because it is the configuration in which
// every hazard above is live again.
func TestADisabledBoundLetsAReportedTimeThrough(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	future := processed.Add(72 * time.Hour)

	rez, counter := boundedResolver(0)
	entries := resolveFixes(t, rez, timedLocationEvent(processed, processed, &future))
	require.Len(t, entries, 1)

	assert.True(t, entries[0].OccurredTime.Equal(future), "got %v", entries[0].OccurredTime)
	assert.Zero(t, testutil.ToFloat64(counter), "nothing was bounded, so nothing may be counted")
}

// The measurement and alert branches keep each entry's own instant, exactly as the
// location branch does.
//
// 🔴 THESE EXIST BECAUSE THE LOCATION BRANCH ALONE WAS NOT COVERAGE. Every consumer now
// trusts the resolved entry absolutely — nothing downstream re-derives a time — so a
// single line here flattening measurements to the envelope would flatten them platform
// wide, in the historian, both live projections, detection and replay at once. Reverting
// just these two lines left every test in this module green; three payload types resolved
// by three near-identical loops is precisely the shape where testing one proves the other
// two only by resemblance.

// resolverWithMetricDefs builds a bounded resolver over a mock that declares no metric
// definitions, so measurements resolve as undeclared-but-numeric (kept, unclassified) —
// the lenient path, and the one that exercises the entry-time mapping without dragging in
// the ADR-016 classifier fixtures.
func resolverWithMetricDefs(t *testing.T, skew time.Duration) *EventResolver {
	t.Helper()
	api := new(dmtest.MockApi)
	api.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_resolver_bound_total"})
	policy := EventTimePolicy{MaxFutureSkew: skew, Bounded: counter}
	return NewEventResolver(1, api, "", policy, nil, nil, nil, nil, nil, nil)
}

func TestAMeasurementBatchKeepsEachSamplesOwnInstant(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	envelope := processed.Add(-time.Second)
	first := processed.Add(-90 * time.Second)

	event := &esmodel.UnresolvedEvent{
		Source: "test", Device: "TEST-123", EventType: esmodel.Measurement,
		OccurredTime: envelope, ProcessedTime: processed,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{OccurredTime: &first, Measurements: map[string]string{"temperature": "21.5"}},
			{Measurements: map[string]string{"temperature": "22.5"}}, // no time of its own
		}},
	}
	resolved, err := resolverWithMetricDefs(t, 5*time.Minute).
		ResolveMeasurementsEventPayload(context.Background(), deviceWithToken("TEST-123"), nil, event)
	require.NoError(t, err)
	payload, ok := resolved.(*model.ResolvedMeasurementsPayload)
	require.True(t, ok, "expected *model.ResolvedMeasurementsPayload, got %T", resolved)
	require.Len(t, payload.Entries, 2)

	assert.True(t, payload.Entries[0].OccurredTime.Equal(first),
		"the buffered sample kept %v, want its own instant %v", payload.Entries[0].OccurredTime, first)
	assert.True(t, payload.Entries[1].OccurredTime.Equal(envelope),
		"a sample with no reported time takes the message's: got %v want %v",
		payload.Entries[1].OccurredTime, envelope)
}

func TestAMeasurementSampleTimeIsBounded(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	poisoned := processed.Add(72 * time.Hour)

	event := &esmodel.UnresolvedEvent{
		Source: "test", Device: "TEST-123", EventType: esmodel.Measurement,
		OccurredTime: processed, ProcessedTime: processed,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{OccurredTime: &poisoned, Measurements: map[string]string{"temperature": "21.5"}},
		}},
	}
	resolved, err := resolverWithMetricDefs(t, skew).
		ResolveMeasurementsEventPayload(context.Background(), deviceWithToken("TEST-123"), nil, event)
	require.NoError(t, err)
	payload := resolved.(*model.ResolvedMeasurementsPayload)
	require.Len(t, payload.Entries, 1)
	assert.True(t, payload.Entries[0].OccurredTime.Equal(processed.Add(skew)),
		"a poisoned sample time must be replaced with the ceiling: got %v", payload.Entries[0].OccurredTime)
}

func TestAnAlertBatchKeepsEachEntrysOwnInstant(t *testing.T) {
	processed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	envelope := processed.Add(-time.Second)
	raised := processed.Add(-90 * time.Second)

	rez, _ := boundedResolver(5 * time.Minute)
	event := &esmodel.UnresolvedEvent{
		Source: "test", Device: "TEST-123", EventType: esmodel.Alert,
		OccurredTime: envelope, ProcessedTime: processed,
		Payload: &esmodel.UnresolvedAlertsPayload{Entries: []esmodel.UnresolvedAlertEntry{
			{OccurredTime: &raised, Type: "SENSOR_FAULT", Level: 3, Message: "probe A open circuit"},
			{Type: "SENSOR_FAULT", Level: 3, Message: "probe B open circuit"}, // no time of its own
		}},
	}
	resolved, err := rez.ResolveAlertsEventPayload(context.Background(), nil, nil, event)
	require.NoError(t, err)
	payload, ok := resolved.(*model.ResolvedAlertsPayload)
	require.True(t, ok, "expected *model.ResolvedAlertsPayload, got %T", resolved)
	require.Len(t, payload.Entries, 2)

	assert.True(t, payload.Entries[0].OccurredTime.Equal(raised),
		"the alert kept %v, want the instant it was raised %v", payload.Entries[0].OccurredTime, raised)
	assert.True(t, payload.Entries[1].OccurredTime.Equal(envelope),
		"an alert with no reported time takes the message's: got %v want %v",
		payload.Entries[1].OccurredTime, envelope)
}
