// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// countingRounds counts the requests a pod's walks make.
type countingRounds struct {
	messaging.RoundPublisher
	pubs, gets atomic.Int64
}

func (c *countingRounds) Publish(ctx context.Context, m messaging.Message) (messaging.PubAckInfo, error) {
	c.pubs.Add(1)
	return c.RoundPublisher.Publish(ctx, m)
}

func (c *countingRounds) Get(ctx context.Context, tenant string, seq uint64) (messaging.StoredMessage, error) {
	c.gets.Add(1)
	return c.RoundPublisher.Get(ctx, tenant, seq)
}

// Two pods read the one durable at once, so most buckets are shared and every chunk of a shared
// bucket meets the other pod's rounds. One of them is killed between a PubAck and the ack (its
// connection cut, every outcome after the first few swallowed) with events in flight; the
// survivor resolves what is redelivered. The identity oracle over the whole stream is the same
// as the single-pod test's: nothing missing, nothing stored twice. The log line reports the
// requests the walks made per event (round publishes plus read-backs).
//
// Runs on one server: the protocol under test is the client's.
func TestTwoPodsSharingTheDurableLoseAndDuplicateNothing(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const events = 6000
	srv := startReadersServer(t)
	instance := fmt.Sprintf("envtwo%d", time.Now().UnixNano())
	base := time.Now().Add(-time.Minute)
	spec := pipelineSpec{
		srv: srv, instance: instance, replicas: 1, resolvers: 8, window: 64, readers: 1, events: 0,
		api: instantApi{}, authMode: config.AuthModeOptional, ackWait: 2 * time.Second,
	}
	// Two pods on one durable: one connection with two inbound readers, each pod a processor of its
	// own (its own resolver pool, writers, round state and cache) over one reader. (Two separate
	// managers would each re-create the durable's subscription under the other.)
	counts := []*countingRounds{}
	var dying *dyingWriter
	s := spec
	s.readers = 2
	s.wrapResolved = func(w messaging.OrderedWriter) messaging.OrderedWriter {
		dying = &dyingWriter{AckedWriter: w.(messaging.AckedWriter), allow: 1}
		return dying
	}
	a := startResolvePipeline(t, s)
	readers := a.iproc.InboundEventsReaders
	a.iproc.InboundEventsReaders = readers[:1]
	bproc := newBenchProcessor(t, a.nmgr, readers[1:], instantApi{}, 64, 4)
	bproc.AuthMode = config.AuthModeOptional
	require.NoError(t, bproc.Initialize(context.Background()))
	b := &resolvePipeline{nmgr: a.nmgr, iproc: bproc, js: a.js, inboundStream: a.inboundStream, resolvedStream: a.resolvedStream}
	for _, p := range []*resolvePipeline{a, b} {
		rp, err := p.nmgr.NewRoundPublisher(streams.ResolvedEvents)
		require.NoError(t, err)
		c := &countingRounds{RoundPublisher: rp}
		counts = append(counts, c)
		WithResolvedEnvelopes(true, c)(p.iproc)
	}

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(1024))
	require.NoError(t, err)
	batch := make([]*esmodel.UnresolvedEvent, events)
	for i := range batch {
		batch[i] = trialEvent(0, i, base)
	}
	require.NoError(t, a.iproc.Start(context.Background()))
	require.NoError(t, b.iproc.Start(context.Background()))
	// Paced bursts, so both pods keep fetching from the one durable and share the buckets.
	go func() {
		for lo := 0; lo < events; lo += 100 {
			publishInbound(t, js, instance, batch[lo:lo+100])
			time.Sleep(8 * time.Millisecond)
		}
	}()
	deadline := time.Now().Add(60 * time.Second)
	for dying.dropped.Load() == 0 {
		require.True(t, time.Now().Before(deadline), "pod A never reached its kill point (settled %d, a members %d, b members %d)", dying.settled.Load(), a.iproc.env.members.Load(), b.iproc.env.members.Load())
		time.Sleep(time.Millisecond)
	}
	// The pod dies: nothing more is acknowledged, and what it holds goes back to the durable.
	go func() { _ = a.iproc.Stop(context.Background()) }()

	durable := messaging.DurableName(instance, "device-management", streams.InboundEvents)
	deadline = time.Now().Add(120 * time.Second)
	for {
		ci, err := js.ConsumerInfo(b.inboundStream, durable)
		require.NoError(t, err)
		if ci.NumPending == 0 && ci.NumAckPending == 0 && ci.AckFloor.Stream == events {
			break
		}
		require.True(t, time.Now().Before(deadline), "the surviving pod did not settle: pending %d, ack pending %d, floor %d",
			ci.NumPending, ci.NumAckPending, ci.AckFloor.Stream)
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, b.iproc.Stop(context.Background()))

	seen, envelopes, legacy, members := storedIdentities(t, js, b.resolvedStream)
	var missing, twice int
	for i := 0; i < events; i++ {
		ev := trialEvent(0, i, base)
		n := seen[identity{ev.Device, "asset-2", ev.OccurredTime.UnixMicro()}]
		switch {
		case n == 0:
			missing++
		case n > 1:
			twice++
		}
	}
	probes := int64(0)
	for _, c := range counts {
		probes += c.pubs.Load() + c.gets.Load()
	}
	t.Logf("two pods, one killed: %d events in %d envelopes + %d legacy; walk requests %d (publishes %d + read-backs %d) = %.4f per event; "+
		"duplicate answers a=%d b=%d; missing %d, duplicated %d",
		members, envelopes, legacy, probes, counts[0].pubs.Load()+counts[1].pubs.Load(), counts[0].gets.Load()+counts[1].gets.Load(),
		float64(probes)/events, a.iproc.env.duplicates.Load(), b.iproc.env.duplicates.Load(), missing, twice)
	require.Zero(t, missing, "resolved events lost")
	require.Zero(t, twice, "resolved events stored more than once")
	require.Equal(t, events, members)
	require.Less(t, float64(probes)/events, 1.0, "the walks made more than a request per event")
}
