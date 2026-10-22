// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// referenceOpener replaces the replay reader with a reference implementation that is nothing like
// it: one `GetMsg` per sequence, straight off the stream, no batches and no direct gets. A term
// build over it is the golden for a term build over the real reader.
type referenceOpener struct {
	b *detectBroker
	*messaging.NatsManager
}

func (o *referenceOpener) NewReplayReader(suffix string, startSeq uint64) (messaging.ReplayReader, uint64, error) {
	js, err := o.b.nc.JetStream()
	if err != nil {
		return nil, 0, err
	}
	stream := messaging.StreamName(o.b.instance, suffix)
	info, err := js.StreamInfo(stream)
	if err != nil {
		return nil, 0, err
	}
	return &referenceReader{js: js, stream: stream, next: startSeq, head: info.State.LastSeq}, info.State.LastSeq, nil
}

type referenceReader struct {
	js         nats.JetStreamContext
	stream     string
	next, head uint64
}

func (r *referenceReader) Read(context.Context) (messaging.Message, error) {
	for r.next <= r.head {
		seq := r.next
		r.next++
		raw, err := r.js.GetMsg(r.stream, seq)
		if err != nil {
			continue // deleted or evicted
		}
		m := messaging.NewConsumedMessage(raw.Subject, raw.Data, 1, nil, nil)
		m.StreamSeq, m.AppendTime = raw.Sequence, raw.Time
		return m, nil
	}
	return messaging.Message{}, io.EOF
}

func (r *referenceReader) Close() error { return nil }

// 🔴 THE GOLDEN FOR THE REPLAY'S MOVE OFF AN EPHEMERAL CONSUMER. A term build over the real replay
// reader (batched reads by sequence) must end with the same engine state, and the same
// detections, as one over a reference that fetches each sequence on its own: every event read,
// in order, none skipped and none twice.
func TestTermBuildReplayMatchesAReferenceRead(t *testing.T) {
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
		ev := &resolvedevent.ResolvedEvent{
			Source: "http1", SourceDeviceToken: fmt.Sprintf("d%03d", i), ProfileVersionToken: "p@1",
			OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: value}},
			}}},
		}
		body, err := resolvedevent.MarshalResolvedEvent(ev)
		require.NoError(t, err)
		_, err = js.Publish(messaging.ScopedSubject(b.instance, "acme", streams.ResolvedEvents), body)
		require.NoError(t, err)
	}

	build := func(o ReplayOpener) (snapshot []byte, lastSeq uint64, detections int) {
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

	// The reference first: it is the golden, and it must itself have read every event.
	wantSnap, wantSeq, wantDetections := build(&referenceOpener{b: b, NatsManager: nmgr})
	require.Equal(t, uint64(events), wantSeq)
	require.Positive(t, wantDetections)

	gotSnap, gotSeq, gotDetections := build(nmgr)
	require.Equal(t, wantSeq, gotSeq)
	require.Equal(t, wantDetections, gotDetections, "the replay skipped or repeated an event")
	require.Equal(t, wantSnap, gotSnap, "the engine state after the replay differs from the reference read's")
}
