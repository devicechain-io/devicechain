// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Several inbound readers per pod (resolution.inboundReaders): each runs its own read loop on
// the one inbound-events durable, so each keeps its own read in flight, and every one of them
// is stopped before the channel they all feed is closed.

// heldReader is a reader whose every read is held until the caller's context ends, as a
// long-poll pull against an empty stream is. It counts the reads in flight, the most it ever
// saw at once, and the reads that have returned; release is how long a read keeps going
// after its context ends, as a pull already in flight does (it ends at its own timeout).
type heldReader struct {
	inflight, peak *atomic.Int32
	returned       *atomic.Int32
	release        time.Duration
}

func (r *heldReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	n := r.inflight.Add(1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-ctx.Done()
	time.Sleep(r.release)
	r.inflight.Add(-1)
	r.returned.Add(1)
	return messaging.Message{}, io.EOF
}

func (r *heldReader) HandleResponse(error) {}

// heldReaders builds n heldReaders sharing one set of counters; reader i keeps reading for
// i x step after its context ends, so the last one is the slowest to stop.
func heldReaders(n int, step time.Duration) (readers []messaging.MessageReader, inflight, peak, returned *atomic.Int32) {
	inflight, peak, returned = &atomic.Int32{}, &atomic.Int32{}, &atomic.Int32{}
	for i := 0; i < n; i++ {
		readers = append(readers, &heldReader{inflight: inflight, peak: peak, returned: returned,
			release: time.Duration(i) * step})
	}
	return readers, inflight, peak, returned
}

// readersProcessor builds the processor through its constructor over readers, with no writers
// and no API: nothing is delivered, so nothing is resolved or published.
func readersProcessor(t *testing.T, readers []messaging.MessageReader) *InboundEventsProcessor {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	iproc := NewInboundEventsProcessor(ms, readers, nil, nil, core.NewNoOpLifecycleCallbacks(), nil,
		config.AuthModeOptional, time.Minute, NewResolveMetrics(ms), WithResolvers(2))
	require.NoError(t, iproc.Initialize(context.Background()))
	return iproc
}

// Every reader the processor is given is read at the same time: with three readers, three
// reads are in flight at once. A processor that read only its first reader, or read its
// readers one after another, holds one.
func TestEveryInboundReaderHasAReadInFlightAtOnce(t *testing.T) {
	readers, inflight, peak, _ := heldReaders(3, 0)
	iproc := readersProcessor(t, readers)
	require.NoError(t, iproc.Start(context.Background()))

	deadline := time.Now().Add(5 * time.Second)
	for inflight.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, iproc.Stop(context.Background()))
	require.EqualValues(t, 3, peak.Load(), "reads in flight at once across three inbound readers")
}

// Stop returns only once EVERY reader's loop has returned. A reader whose read is still in
// flight when the context ends keeps going until it times out by itself; one that came back
// with a message after the channel closed would send on a closed channel. Here the readers
// take 0, 150 and 300 ms to return after the context ends, and when Stop returns all three
// have.
func TestStopWaitsForTheSlowestInboundReader(t *testing.T) {
	readers, inflight, _, returned := heldReaders(3, 150*time.Millisecond)
	iproc := readersProcessor(t, readers)
	require.NoError(t, iproc.Start(context.Background()))
	deadline := time.Now().Add(5 * time.Second)
	for inflight.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.EqualValues(t, 3, inflight.Load(), "precondition: every reader is reading")

	require.NoError(t, iproc.Stop(context.Background()))
	require.EqualValues(t, 3, returned.Load(), "inbound readers that had returned when Stop did")
}

// Start refuses a processor with no reader, or with a nil one, before it starts anything: it
// would otherwise report started and resolve nothing (or panic in a read loop).
func TestStartRefusesAMissingInboundReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		readers []messaging.MessageReader
		want    string
	}{
		{"none", nil, "has no reader"},
		{"a nil one", []messaging.MessageReader{&sliceReader{}, nil}, "inbound reader 2 of 2 is nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iproc := readersProcessor(t, tc.readers)
			err := iproc.Start(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// startReadersServer runs an in-process JetStream server for the crash test.
func startReadersServer(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

// A pod running several inbound readers that dies mid-stream loses nothing and duplicates
// nothing. The whole pipeline is real: three readers on the one durable, the resolver pool,
// and the resolved writer, against an embedded JetStream. The first pod's connection is cut
// with a third of the events resolved, so whatever it had fetched, resolved or published
// without acknowledging is redelivered to a second pod (also three readers) after AckWait.
//
// What reaches resolved-events is every inbound sequence exactly once, under its own dedup
// id: a source acked before its resolved event was stored would be missing, and a republish
// the stream did not recognise would be there twice. The consumer must also have delivered
// more than it holds, or the crash left nothing to redeliver and proved nothing.
func TestSeveralInboundReadersLoseAndDuplicateNothingAcrossACrash(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const events = 3000
	const readers = 3
	srv := startReadersServer(t)
	instance := fmt.Sprintf("readers%d", time.Now().UnixNano())
	event := func(i int) *esmodel.UnresolvedEvent { return benchMeasurement(i % 500) }
	spec := pipelineSpec{
		srv: srv, instance: instance, replicas: 1, resolvers: 4, window: 64, readers: readers,
		events: events, api: instantApi{}, authMode: config.AuthModeOptional, event: event,
		ackWait: 2 * time.Second,
	}

	first := startResolvePipeline(t, spec)
	durable := messaging.DurableName(instance, "device-management", streams.InboundEvents)
	resolvedCount := func() uint64 {
		info, err := first.js.StreamInfo(first.resolvedStream)
		require.NoError(t, err)
		return info.State.Msgs
	}
	require.NoError(t, first.iproc.Start(context.Background()))
	deadline := time.Now().Add(30 * time.Second)
	for resolvedCount() < events/3 {
		require.True(t, time.Now().Before(deadline), "the first pod never resolved a third of the events")
		time.Sleep(time.Millisecond)
	}
	// The crash: the connection goes, with whatever the pod holds unacknowledged.
	first.nmgr.Conn().Close()
	atCrash := resolvedCount()
	require.Less(t, atCrash, uint64(events), "the first pod finished before the crash; nothing was in flight")
	stopped := make(chan struct{})
	go func() { _ = first.iproc.Stop(context.Background()); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the crashed pod's processor did not stop")
	}

	// The replacement pod: the same durable, nothing new published.
	spec.events = 0
	second := startResolvePipeline(t, spec)
	require.NoError(t, second.iproc.Start(context.Background()))
	t.Cleanup(func() { _ = second.iproc.Stop(context.Background()) })

	deadline = time.Now().Add(60 * time.Second)
	var ci *nats.ConsumerInfo
	for {
		var err error
		ci, err = second.js.ConsumerInfo(second.inboundStream, durable)
		require.NoError(t, err)
		if ci.NumPending == 0 && ci.NumAckPending == 0 && ci.AckFloor.Stream == events {
			break
		}
		require.True(t, time.Now().Before(deadline),
			"the second pod did not settle every inbound event: pending %d, ack pending %d, ack floor %d",
			ci.NumPending, ci.NumAckPending, ci.AckFloor.Stream)
		time.Sleep(20 * time.Millisecond)
	}
	require.Greater(t, ci.Delivered.Consumer, uint64(events),
		"the consumer delivered no more than it holds, so the crash left nothing to redeliver")

	// Read resolved-events back whole and check each inbound sequence is there exactly once.
	sub, err := second.js.PullSubscribe("", "", nats.BindStream(second.resolvedStream), nats.DeliverAll(),
		nats.AckNone())
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	seen := map[string]int{}
	for len(seen) < events {
		msgs, err := sub.Fetch(256, nats.MaxWait(2*time.Second))
		if err != nil {
			break
		}
		for _, m := range msgs {
			seen[m.Header.Get(nats.MsgIdHdr)]++
		}
	}
	info, err := second.js.StreamInfo(second.resolvedStream)
	require.NoError(t, err)
	require.EqualValues(t, events, info.State.Msgs, "resolved events stored for %d inbound events", events)
	var missing, twice []string
	for seq := 1; seq <= events; seq++ {
		id := "r:acme:" + strconv.FormatInt(int64(seq), 36) + ":0"
		switch seen[id] {
		case 1:
		case 0:
			missing = append(missing, id)
		default:
			twice = append(twice, id)
		}
	}
	require.Empty(t, missing, "inbound events with no resolved event")
	require.Empty(t, twice, "inbound events resolved more than once")
	t.Logf("crashed at %d of %d resolved; consumer delivered %d", atCrash, events, ci.Delivered.Consumer)
}

// The readers really are one consumer: the broker hands each message to one of them. Kept
// beside the crash test because the crash test's "exactly once" leans on it: three readers
// that were three consumers would each resolve every event, and dedup would hide it.
func TestSeveralInboundReadersShareOneDurable(t *testing.T) {
	srv := startReadersServer(t)
	instance := fmt.Sprintf("onedurable%d", time.Now().UnixNano())
	p := startResolvePipeline(t, pipelineSpec{
		srv: srv, instance: instance, replicas: 1, resolvers: 1, window: 1, readers: 3,
		api: instantApi{}, authMode: config.AuthModeOptional,
	})
	var names []string
	for name := range p.js.ConsumerNames(p.inboundStream) {
		names = append(names, name)
	}
	require.Equal(t, []string{messaging.DurableName(instance, "device-management", streams.InboundEvents)}, names,
		"consumers on the inbound stream")
	require.Len(t, p.iproc.InboundEventsReaders, 3)
}
