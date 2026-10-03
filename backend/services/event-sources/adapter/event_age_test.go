// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtime"
)

// fixedNowMs is fixedNow in milliseconds: a sample time the age limit keeps. A fixture that
// dates a sample 1 ms after the epoch is dated more than the limit before fixedNow.
var fixedNowMs = fixedNow().UnixMilli()

// emitErr is Emit for a test that is not about the age limit: it drops the count.
func emitErr(e *Emitter, ctx context.Context, tenant, source, deviceToken string, samples []Sample) error {
	_, err := e.Emit(ctx, tenant, source, deviceToken, samples)
	return err
}

// storedSamples decodes every measurement event the writer received: metric name to its time.
func storedSamples(t *testing.T, w *fakeWriter) (map[string]time.Time, []*esmodel.UnresolvedEvent) {
	t.Helper()
	out := map[string]time.Time{}
	var events []*esmodel.UnresolvedEvent
	for _, m := range w.msgs {
		ev, err := esproto.UnmarshalUnresolvedEvent(m.Value)
		require.NoError(t, err)
		events = append(events, ev)
		p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
		require.True(t, ok)
		for _, en := range p.Entries {
			require.NotNil(t, en.OccurredTime)
			for k := range en.Measurements {
				out[k] = *en.OccurredTime
			}
		}
	}
	return out, events
}

// A sample dated more than eventtime.MaxAge before receipt is dropped on its own and counted;
// every other sample of the batch is emitted, and the envelope is dated from the kept ones.
// The boundary is the platform's: a sample exactly MaxAge old is kept.
func TestEmitDropsOnlyTheSamplesOlderThanTheAgeLimit(t *testing.T) {
	now := fixedNow()
	atFloor := now.Add(-eventtime.MaxAge).UnixMilli()
	tooOld := now.Add(-eventtime.MaxAge - time.Millisecond).UnixMilli()
	fresh := now.Add(-time.Minute).UnixMilli()

	w := &fakeWriter{}
	dropped, err := NewEmitter(w, fixedNow, "sp", true).Emit(context.Background(), "acme", "sparkplug:h1", "dev-1", []Sample{
		{Name: "setpoint", Value: 1, Time: tooOld},
		{Name: "temperature", Value: 2, Time: fresh},
		{Name: "calibrated", Value: 3, Time: atFloor},
		{Name: "untimed", Value: 4, Time: 0},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dropped)

	stored, events := storedSamples(t, w)
	assert.NotContains(t, stored, "setpoint")
	assert.Equal(t, fresh, stored["temperature"].UnixMilli())
	assert.Equal(t, atFloor, stored["calibrated"].UnixMilli(), "a sample exactly at the limit is kept")
	assert.Equal(t, now.UnixMilli(), stored["untimed"].UnixMilli(), "a sample with no time is dated at receipt")
	require.Len(t, events, 1)
	assert.Equal(t, fresh, events[0].OccurredTime.UnixMilli(), "the envelope is the latest KEPT sample")
	// What is emitted passes the resolver's own check, which reads the same instants.
	assert.NoError(t, esmodel.CheckEventAge(events[0].OccurredTime, events[0].ProcessedTime, events[0].Payload))
}

// A batch every sample of which is too old writes nothing and reports them all.
func TestEmitWritesNothingWhenEverySampleIsTooOld(t *testing.T) {
	old := fixedNow().Add(-400 * 24 * time.Hour).UnixMilli()
	w := &fakeWriter{}
	dropped, err := NewEmitter(w, fixedNow, "lw", true).Emit(context.Background(), "acme", "lwm2m", "dev-1",
		[]Sample{{Name: "a", Value: 1, Time: old}, {Name: "b", Value: 2, Time: old - 1}})
	require.NoError(t, err)
	assert.Equal(t, 2, dropped)
	assert.Empty(t, w.msgs)
}

// The Ingester, which owns the metrics, counts the dropped samples apart from the emitted
// ones, so an operator can see a device whose readings are being dropped for their age.
func TestIngestCountsSamplesDroppedForAge(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	emitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "emitted"})
	tooOld := prometheus.NewCounter(prometheus.CounterOpts{Name: "too_old"})
	w := &fakeWriter{}
	ing := NewIngester(NewRegistrar(gql, "url", "sp-", nil), NewEmitter(w, fixedNow, "sp", true),
		IngestMetrics{MeasurementsEmitted: emitted, TooOldDropped: tooOld})

	old := fixedNow().Add(-400 * 24 * time.Hour).UnixMilli()
	require.NoError(t, ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "sparkplug:h1"}, "g/n", []Sample{
		{Name: "a", Value: 1, Time: old}, {Name: "b", Value: 2, Time: fixedNow().UnixMilli()}, {Name: "c", Value: 3, Time: old}}))
	assert.Equal(t, 2.0, testutil.ToFloat64(tooOld))
	assert.Equal(t, 1.0, testutil.ToFloat64(emitted))
	stored, _ := storedSamples(t, w)
	assert.Equal(t, []string{"b"}, keysOf(stored))
}

func keysOf(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The case the envelope's dating is for: a too-old sample alongside one with no time of its
// own. The untimed sample takes the batch's latest time, so that latest must be read from the
// KEPT samples: read from the original batch, the dropped sample's ancient time would date the
// envelope and the untimed entry, and device-management would refuse the whole event as too old.
func TestEmitDatesUntimedSamplesFromTheKeptSamplesOnly(t *testing.T) {
	now := fixedNow()
	tooOld := now.Add(-eventtime.MaxAge - time.Hour).UnixMilli()

	w := &fakeWriter{}
	dropped, err := NewEmitter(w, fixedNow, "sp", true).Emit(context.Background(), "acme", "sparkplug:h1", "dev-1", []Sample{
		{Name: "setpoint", Value: 1, Time: tooOld},
		{Name: "untimed", Value: 2, Time: 0},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dropped)

	stored, events := storedSamples(t, w)
	assert.Equal(t, []string{"untimed"}, keysOf(stored))
	assert.Equal(t, now.UnixMilli(), stored["untimed"].UnixMilli(), "the untimed entry is dated at receipt")
	require.Len(t, events, 1)
	require.NotNil(t, events[0].OccurredTime)
	assert.Equal(t, now.UnixMilli(), events[0].OccurredTime.UnixMilli(), "the envelope is dated at receipt")
	assert.NoError(t, esmodel.CheckEventAge(events[0].OccurredTime, events[0].ProcessedTime, events[0].Payload))
}
