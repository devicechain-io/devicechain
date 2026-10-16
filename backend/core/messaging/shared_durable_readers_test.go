// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// Several readers of ONE suffix in one manager are readers of one durable: a service that wants
// more pulls in flight than one reader keeps (device-management's resolution.inboundReaders)
// builds NewReader that many times. Each reader keeps its own subscription and its own pull;
// everything the manager keeps per durable keeps it once.

// heldPulls is the number of pull requests the broker shows waiting on a durable most often
// over a sampling window: when a pull expires the reader's next one can reach the broker a
// moment before the old one is dropped, so one sample can read high, never for long.
func heldPulls(t *testing.T, nmgr *NatsManager, stream, durable string, window time.Duration) int {
	t.Helper()
	seen := map[int]int{}
	for end := time.Now().Add(window); time.Now().Before(end); {
		ci, err := nmgr.js.ConsumerInfo(stream, durable)
		require.NoError(t, err)
		seen[ci.NumWaiting]++
		time.Sleep(20 * time.Millisecond)
	}
	held, most := 0, 0
	for n, count := range seen {
		if count > most || (count == most && n < held) {
			held, most = n, count
		}
	}
	return held
}

// readUntilDone reads reader until ctx ends, acking what it gets, and returns a func that
// waits for it to stop.
func readUntilDone(ctx context.Context, reader MessageReader, got func(Message)) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if m, err := reader.ReadMessage(ctx); err == nil {
				if got != nil {
					got(m)
				}
				_ = m.Ack()
			}
		}
	}()
	return func() { <-done }
}

// Two readers on one suffix: one consumer on the broker, one durable sampled, one suffix
// listed, one advisory filter on the recorder (a second, identical filter would be refused by
// the broker as overlapping, and the recorder would not start), and two pulls in flight.
func TestTwoReadersOnOneSuffixAreOneDurableWithTwoPulls(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("shared")
	nmgr, readers := recorderRig(t, srv, area, recordNothing, streams.RaiseAlarm, streams.RaiseAlarm)
	require.Len(t, readers, 2)
	stream := StreamName("test", streams.RaiseAlarm)
	durable := DurableName("test", area, streams.RaiseAlarm)

	var names []string
	for n := range nmgr.js.ConsumerNames(stream) {
		names = append(names, n)
	}
	require.Equal(t, []string{durable}, names, "consumers on the stream")
	require.Equal(t, []durableRef{{stream: stream, durable: durable}}, nmgr.trackedDurables(), "durables sampled")
	require.Equal(t, []string{streams.RaiseAlarm}, nmgr.ReaderSuffixes())
	capture, err := nmgr.js.ConsumerInfo(StreamName("test", streams.MaxDeliveries), DurableName("test", area, streams.MaxDeliveries))
	require.NoError(t, err)
	require.Equal(t, []string{AdvisorySubject(stream, durable)}, capture.Config.FilterSubjects, "the recorder's filters")

	ctx, cancel := context.WithCancel(context.Background())
	waits := []func(){readUntilDone(ctx, readers[0], nil), readUntilDone(ctx, readers[1], nil)}
	held := heldPulls(t, nmgr, stream, durable, 2*time.Second)
	cancel()
	for _, w := range waits {
		w()
	}
	require.Equal(t, 2, held, "pull requests held at the broker by two readers")
}

// A message whose deliveries run out under two readers is recorded ONCE: both readers pull the
// one durable, and the broker sends one advisory for it, whichever reader took the deliveries.
func TestAMaxDeliveryUnderTwoReadersIsRecordedOnce(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("shared-maxdeliver"), rec.build, streams.RaiseAlarm, streams.RaiseAlarm)
	seq := exhaust(t, nmgr, readers[0], streams.RaiseAlarm)
	keepPulling(t, readers[0])
	keepPulling(t, readers[1])

	waitWithin(t, 6*time.Second, "the advisory", func() bool { return len(rec.all()) >= 1 })
	// Two more ack windows, so a second record would have arrived.
	time.Sleep(2 * time.Second)
	got := rec.all()
	require.Len(t, got, 1, "records of one exhausted message under two readers")
	require.Equal(t, seq, got[0].StreamSeq)
}

// Every forwarding reader on the durable parks under a closed downstream gate, not just one of
// them: neither is delivered anything while resolved-events refuses. When the gate opens, both
// resume and the messages are each delivered for the first time.
func TestEveryForwardingReaderOnADurableParksUnderAClosedGate(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	em := newBPService(t, srv, "event-management", bounds)
	emr := em.reader(t, streams.ResolvedEvents)
	up := newBPService(t, srv, "event-processing", bounds)
	rw := up.writer(t, streams.ResolvedEvents)
	if refused, _ := fill(t, up, rw, streams.ResolvedEvents, 18, []byte("m")); refused != 0 {
		t.Fatalf("resolved-events refused before it filled: %d", refused)
	}

	dm := newBPService(t, srv, "device-management", bounds)
	in := []*natsReader{dm.reader(t, streams.InboundEvents), dm.reader(t, streams.InboundEvents)}
	require.Equal(t, in[0].durable, in[1].durable, "precondition: one durable")
	es := newBPService(t, srv, "event-sources", bounds)
	w := es.writer(t, streams.InboundEvents)
	require.NoError(t, w.WriteMessages(tenantCtx(), Message{Value: []byte("a")}, Message{Value: []byte("b")}))
	dm.sample(t, streams.ResolvedEvents)
	require.Error(t, dm.nmgr.Backpressure(streams.ResolvedEvents), "precondition: the downstream gate is closed")

	var wg sync.WaitGroup
	errs := make([]error, len(in))
	for i, r := range in {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, errs[i] = r.ReadMessage(ctx)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.True(t, errors.Is(err, io.EOF), "reader %d handed a message out under a closed downstream gate: %v", i+1, err)
	}
	ci, err := dm.nmgr.js.ConsumerInfo(in[0].stream, in[0].durable)
	require.NoError(t, err)
	require.Zero(t, ci.Delivered.Consumer, "deliveries while every reader was parked")

	// event-management catches up past the open threshold; the gate opens.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m, err := emr.ReadMessage(ctx)
		cancel()
		require.NoError(t, err, "drain resolved %d", i)
		require.NoError(t, m.Ack())
	}
	deadline := time.Now().Add(5 * time.Second)
	for dm.nmgr.Backpressure(streams.ResolvedEvents) != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		dm.sample(t, streams.ResolvedEvents)
	}
	require.NoError(t, dm.nmgr.Backpressure(streams.ResolvedEvents), "the gate did not open")

	var mu sync.Mutex
	got := map[uint64]int{}
	ctx, cancel := context.WithCancel(context.Background())
	var waits []func()
	for _, r := range in {
		waits = append(waits, readUntilDone(ctx, r, func(m Message) {
			mu.Lock()
			defer mu.Unlock()
			got[m.StreamSeq] = m.NumDelivered
		}))
	}
	waitWithin(t, 5*time.Second, "both messages after the gate opened", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	cancel()
	for _, wt := range waits {
		wt()
	}
	require.Equal(t, map[uint64]int{1: 1, 2: 1}, got, "stream sequence -> delivery number once resumed")
}
