// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// stealingOpener opens the real replay reader, and after afterReads messages have been read
// takes the next steal deliveries from that reader's own consumer without acknowledging them:
// the broker has counted them as sent and the reader never sees them, which is what a
// connection that drops mid-response does.
type stealingOpener struct {
	t *testing.T
	b *detectBroker
	*messaging.NatsManager
	afterReads int
	steal      int
}

func (o *stealingOpener) NewReplayReader(suffix string, startSeq uint64) (messaging.ReplayReader, uint64, error) {
	rd, head, err := o.NatsManager.NewReplayReader(suffix, startSeq)
	if err != nil || o.steal == 0 {
		return rd, head, err
	}
	return &stealingReader{ReplayReader: rd, o: o, suffix: suffix}, head, nil
}

type stealingReader struct {
	messaging.ReplayReader
	o      *stealingOpener
	suffix string
	reads  int
}

func (r *stealingReader) Read(ctx context.Context) (messaging.Message, error) {
	if r.reads == r.o.afterReads {
		r.reads++
		r.steal()
	}
	m, err := r.ReplayReader.Read(ctx)
	if err == nil {
		r.reads++
	}
	return m, err
}

func (r *stealingReader) steal() {
	t, o := r.o.t, r.o
	js, err := o.b.nc.JetStream()
	require.NoError(t, err)
	stream := messaging.StreamName(o.b.instance, r.suffix)
	var replay string
	for ci := range js.Consumers(stream) {
		if ci.Config.Durable == "" { // the replay reader's ephemeral
			replay = ci.Name
		}
	}
	require.NotEmpty(t, replay, "the replay reader's consumer")
	sub, err := js.PullSubscribe(messaging.StreamSubject(o.b.instance, r.suffix), "", nats.Bind(stream, replay))
	require.NoError(t, err)
	lost, err := sub.Fetch(o.steal, nats.MaxWait(5*time.Second))
	require.NoError(t, err)
	require.Len(t, lost, o.steal)
}

// A term build whose replay consumer lost deliveries must end with the same engine state, and
// the same detections, as one that lost none: the lost events are read again, not skipped.
func TestTermBuildReplayWithLostDeliveriesMatchesACleanReplay(t *testing.T) {
	const events = 600
	b := startDetectBroker(t)
	nmgr, _ := b.detectManager(t, newTestStore(t))

	js, err := b.nc.JetStream()
	require.NoError(t, err)
	start := time.Now().Add(-time.Hour)
	for i := 0; i < events; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		// Every event breaches the threshold, so every one fires: a replay that skips even
		// one changes the detection count, not just the engine state.
		value := "90"
		ev := &dmmodel.ResolvedEvent{
			Source: "http1", SourceDeviceToken: fmt.Sprintf("d%03d", i), ProfileVersionToken: "p@1",
			OccurredTime: at, ProcessedTime: at, EventType: esmodel.Measurement,
			Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
				OccurredTime: at, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: value}},
			}}},
		}
		body, err := dmproto.MarshalResolvedEvent(ev)
		require.NoError(t, err)
		_, err = js.Publish(messaging.ScopedSubject(b.instance, "acme", streams.ResolvedEvents), body)
		require.NoError(t, err)
	}

	build := func(o *stealingOpener) (snapshot []byte, lastSeq uint64, detections int) {
		reg := thresholdReg(t)
		w := &captureWriter{}
		rp := &ResolvedEventsProcessor{
			Replay: o,
			Store:  newTestStore(t),
			cfg: Config{
				PartitionId:        "singleton",
				Suffix:             streams.ResolvedEvents,
				CheckpointEvents:   10000,
				CheckpointInterval: time.Hour,
				TickInterval:       time.Hour,
				Clock:              detectcore.RealClock{},
			},
			registry:  reg,
			publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
			clock:     detectcore.RealClock{},
			procCtx:   context.Background(),
		}
		require.NoError(t, rp.restore(context.Background()))
		require.NoError(t, rp.replayToHead())
		snap, err := rp.engine.Snapshot()
		require.NoError(t, err)
		return snap, rp.engine.LastSeq(), w.writes
	}

	cleanSnap, cleanSeq, cleanDetections := build(&stealingOpener{t: t, b: b, NatsManager: nmgr})
	require.Equal(t, uint64(events), cleanSeq)
	require.Positive(t, cleanDetections)

	// After the first four fetch batches (256 messages) have been read, 40 deliveries are lost from the next.
	lossySnap, lossySeq, lossyDetections := build(&stealingOpener{t: t, b: b, NatsManager: nmgr, afterReads: 256, steal: 40})
	require.Equal(t, cleanSeq, lossySeq)
	require.Equal(t, cleanDetections, lossyDetections, "a lost delivery's detection was skipped")
	require.Equal(t, cleanSnap, lossySnap, "the engine state after a replay with lost deliveries differs from a clean replay")
}
