// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// numberedSamples builds n samples named m000… with distinct, ascending times, so both
// the order and the per-piece latest time are observable in what Emit writes.
func numberedSamples(n int) []Sample {
	s := make([]Sample, n)
	for i := range s {
		s[i] = Sample{Name: fmt.Sprintf("m%03d", i), Value: float64(i) + 0.5, Time: 1_700_000_000_000 + int64(i)}
	}
	return s
}

// decodeMeasurements unmarshals one emitted message the way the resolver does.
func decodeMeasurements(t *testing.T, m messaging.Message) (*esmodel.UnresolvedEvent, *esmodel.UnresolvedMeasurementsPayload) {
	t.Helper()
	ev, err := esproto.UnmarshalUnresolvedEvent(m.Value)
	require.NoError(t, err)
	p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
	require.True(t, ok, "an emitted sample batch is a measurements event, got %T", ev.Payload)
	return ev, p
}

// 🔴 A GATEWAY BATCH OVER THE LIMIT IS SPLIT, NOT REFUSED AND NOT TRUNCATED. 600 samples
// become consecutive events of 256, 256 and 88, in the order they were handed over, each
// dated at ITS OWN latest sample. Before the limit Emit wrote the whole batch as one event.
func TestEmitSplitsABatchIntoEventsOfAtMost256(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	in := numberedSamples(600)
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", in))

	require.Equal(t, 3, len(w.msgs), "600 samples are three events of at most 256")
	var names []string
	for i, m := range w.msgs {
		ev, p := decodeMeasurements(t, m)
		want := []int{256, 256, 88}[i]
		require.Equal(t, want, len(p.Entries), "piece %d", i)
		// The same check a client's own event must pass: every piece the gateway publishes is
		// one the JSON transports would have accepted.
		require.NoError(t, esmodel.CheckReadingCount(p), "piece %d", i)
		assert.Equal(t, "dev-1", string(m.Key), "every piece is keyed by the device, so the stream keeps its order")
		var latest int64
		for _, en := range p.Entries {
			for k := range en.Measurements {
				names = append(names, k)
			}
			if ms := en.OccurredTime.UnixMilli(); ms > latest {
				latest = ms
			}
		}
		assert.Equal(t, latest, ev.OccurredTime.UnixMilli(), "piece %d is dated at its own latest sample", i)
	}
	want := make([]string, len(in))
	for i, s := range in {
		want[i] = s.Name
	}
	assert.Equal(t, want, names, "the pieces carry every sample, once, in the order Emit was given them")
}

// A redelivered or retried batch splits the SAME way, so every piece carries the same dedup
// id and the same bytes, and JetStream drops the repeat. The three pieces' ids are distinct
// from each other: one id for the whole batch would drop the second and third pieces.
func TestASplitBatchIsIdempotentUnderRetry(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	in := numberedSamples(600)
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", in))
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", in))

	require.Equal(t, 6, len(w.msgs))
	for i := 0; i < 3; i++ {
		assert.Equal(t, w.msgs[i].DedupID, w.msgs[i+3].DedupID, "piece %d must dedup against its retry", i)
		assert.True(t, bytes.Equal(w.msgs[i].Value, w.msgs[i+3].Value), "piece %d must be byte-identical on retry", i)
	}
	ids := map[string]bool{w.msgs[0].DedupID: true, w.msgs[1].DedupID: true, w.msgs[2].DedupID: true}
	assert.Len(t, ids, 3, "each piece needs its own dedup id")
}

// 🔴 A BATCH OF 256 OR FEWER IS PUBLISHED EXACTLY AS BEFORE. These literals were captured
// from Emit itself before the split existed (fixed clock, the samples below). A change to
// what Emit passes the dedup hash, or to the bytes it writes, moves them — and a moved id
// lets an in-flight duplicate from before an upgrade through the dedup window again.
// Pinning measurementDedupID alone would not catch that: it is the pure function, not the
// caller.
func TestABatchAtOrUnderTheLimitIsPublishedExactlyAsBefore(t *testing.T) {
	for _, c := range []struct {
		n      int
		dedup  string
		sha256 string
	}{
		{3, "spy2068tpcdekh", "911615560d90773da4fc827343a68d654d47373f83a19a3e0c0aa1a864df7ea3"},
		{256, "sp3ksr7wzo2aawh", "a96d189266f2686ce11a0fa76f2bda82634dc93fa5add600353e2e4432c10817"},
	} {
		w := &fakeWriter{}
		e := NewEmitter(w, fixedNow, "sp", true)
		require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", numberedSamples(c.n)))
		require.Equal(t, 1, len(w.msgs), "%d samples are one event", c.n)
		assert.Equal(t, c.dedup, w.msgs[0].DedupID, "dedup id of a %d-sample batch must not drift", c.n)
		sum := sha256.Sum256(w.msgs[0].Value)
		assert.Equal(t, c.sha256, hex.EncodeToString(sum[:]), "bytes of a %d-sample batch must not drift", c.n)
	}

	// 257 is the first batch that splits: 256 and 1.
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", numberedSamples(257)))
	require.Equal(t, 2, len(w.msgs))
	_, p0 := decodeMeasurements(t, w.msgs[0])
	_, p1 := decodeMeasurements(t, w.msgs[1])
	assert.Len(t, p0.Entries, 256)
	assert.Len(t, p1.Entries, 1)
}

// countingWriter records each WriteMessages call separately, and can refuse one.
type countingWriter struct {
	calls  [][]messaging.Message
	refuse error
}

func (w *countingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	if w.refuse != nil {
		return w.refuse
	}
	w.calls = append(w.calls, msgs)
	return nil
}

// All the pieces go to the writer in ONE call. The stream's backpressure gate refuses a
// call whole, before it publishes anything, so one call keeps that refusal all-or-nothing:
// the Sparkplug host treats it as "do not retry", and a per-piece loop would instead store
// some pieces and then report the refusal as if nothing had been stored.
func TestASplitBatchIsWrittenInOneCallAndRefusedWhole(t *testing.T) {
	w := &countingWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", numberedSamples(600)))
	require.Equal(t, 1, len(w.calls), "every piece in one WriteMessages call")
	assert.Equal(t, 3, len(w.calls[0]))

	refusing := &countingWriter{refuse: fmt.Errorf("wrapped: %w", messaging.ErrStreamBackpressure)}
	e = NewEmitter(refusing, fixedNow, "sp", true)
	err := emitErr(e, context.Background(), "acme", "src", "dev-1", numberedSamples(600))
	assert.True(t, errors.Is(err, messaging.ErrStreamBackpressure), "the refusal reaches the caller as the sentinel, got %v", err)
	assert.Empty(t, refusing.calls, "nothing was written")
}

// A piece whose samples carry no positive time is dated from the BATCH, not the clock, so a
// retry of the same batch still hashes to the same ids. Only a batch with no positive time
// anywhere falls back to the receipt clock, as it always has.
func TestAPieceWithNoTimeOfItsOwnIsDatedFromItsBatch(t *testing.T) {
	in := numberedSamples(300)
	for i := 256; i < 300; i++ {
		in[i].Time = 0 // the second piece carries no time of its own
	}
	tick := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { tick = tick.Add(time.Second); return tick } // a clock that moves
	w := &fakeWriter{}
	e := NewEmitter(w, clock, "sp", true)
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", in))
	require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", in))
	require.Equal(t, 4, len(w.msgs))
	assert.Equal(t, w.msgs[1].DedupID, w.msgs[3].DedupID, "the timeless piece must dedup against its retry")
	ev, _ := decodeMeasurements(t, w.msgs[1])
	assert.Equal(t, int64(1_700_000_000_255), ev.OccurredTime.UnixMilli(), "dated at the batch's latest sample")
}

// partialWriter publishes the first message of a call and then fails, the way a sequential
// publish that loses the stream part-way does.
type partialWriter struct{ stored []messaging.Message }

func (w *partialWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.stored = append(w.stored, msgs[0])
	return errors.New("stream unavailable after the first publish")
}

// What a part-way failure leaves, and what it reports. The leading event is stored (complete
// in itself); the call fails, so the caller's "dropped" counter moves and MeasurementsEmitted
// (samples of batches written IN FULL) does not. A caller that retries re-publishes every
// piece and the stored one dedups; the LwM2M Notify path does not retry.
func TestAPartialWriteStoresTheLeadingEventsAndCountsNothingEmitted(t *testing.T) {
	gql := &fakeGraphQL{responder: func(string, map[string]any) (any, error) { return lookupHit("dev-1"), nil }}
	emitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "emitted"})
	w := &partialWriter{}
	ing := NewIngester(NewRegistrar(gql, "url", "lw-", nil), NewEmitter(w, fixedNow, "lw", true),
		IngestMetrics{MeasurementsEmitted: emitted})

	err := ing.Ingest(context.Background(), "acme", IngestPolicy{Source: "lwm2m"}, "ep-1", numberedSamples(600))
	require.Error(t, err, "a part-way failure is a failure")
	require.Equal(t, 1, len(w.stored), "the leading event was published before the failure")
	_, p := decodeMeasurements(t, w.stored[0])
	assert.Equal(t, 256, len(p.Entries))
	assert.Equal(t, float64(0), testutil.ToFloat64(emitted), "nothing is counted as emitted for a batch not written in full")
}
