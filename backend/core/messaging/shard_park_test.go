// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

type placementParkRouter struct {
	ready  atomic.Bool
	calls  atomic.Int32
	reason error
}

func (r *placementParkRouter) ShardGroup(string) (int, error) { return 0, nil }
func (r *placementParkRouter) Ready() error {
	r.calls.Add(1)
	if !r.ready.Load() {
		return r.reason
	}
	return nil
}

func TestUngroupedCaptureParksWhilePlacementUnavailable(t *testing.T) {
	m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
	router := &placementParkRouter{reason: errors.New("no placement")}
	require.NoError(t, m.EnableStreamSharding(router, n1Config(streams.InboundEvents)))
	_, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	reader, err := m.NewReader(streams.DeviceEventsCapture)
	require.NoError(t, err)
	r := reader.(*natsReader)
	for _, suffix := range r.downstreams {
		require.NoError(t, m.Backpressure(suffix))
	}
	subject := DeviceEventsSubject("test", "tenant-a", "device")
	_, err = m.js.Publish(subject, []byte("held"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*termGatePoll)
	_, err = r.ReadMessage(ctx)
	cancel()
	require.ErrorIs(t, err, io.EOF)
	info, err := m.js.ConsumerInfo(r.stream, r.durable)
	require.NoError(t, err)
	require.Zero(t, info.Delivered.Consumer, "placement hold must spend no delivery")
	require.Zero(t, info.NumAckPending)
	require.ErrorIs(t, r.downstreamRefusal(), ErrUnplacedTenant)
	require.ErrorIs(t, r.downstreamRefusal(), router.reason)
	require.GreaterOrEqual(t, router.calls.Load(), int32(3))
	r.ahead = true
	r.startAhead()
	require.Zero(t, r.aheadStarts.Load(), "fetch-ahead must also hold on unavailable placement")
	r.ahead = false
	router.ready.Store(true)
	n1Read(t, r, subject, "held")
}

func TestCaptureIgnoresPlacementForUnselectedDownstream(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "unselected"}[enabled], func(t *testing.T) {
			m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
			router := &placementParkRouter{reason: errors.New("must not consult")}
			if enabled {
				require.NoError(t, m.EnableStreamSharding(router, n1Config(streams.ResolvedEvents)))
			}
			_, err := m.NewWriter(streams.InboundEvents)
			require.NoError(t, err)
			reader, err := m.NewReader(streams.DeviceEventsCapture)
			require.NoError(t, err)
			subject := DeviceEventsSubject("test", "tenant-a", "device")
			_, err = m.js.Publish(subject, []byte("open"))
			require.NoError(t, err)
			n1Read(t, reader, subject, "open")
			require.Zero(t, router.calls.Load())
		})
	}
}

func setShardParkGate(t *testing.T, m *NatsManager, suffix string, closed bool) {
	t.Helper()
	gates := m.backpressure()
	gates.mu.Lock()
	defer gates.mu.Unlock()
	gate := gates.gates[suffix]
	require.NotNil(t, gate)
	gate.closed = closed
	gate.sampledAt = time.Now()
}

func TestUngroupedForwardingReaderParksOnEveryShardGate(t *testing.T) {
	m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
	_, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	reader, err := m.NewReader(streams.DeviceEventsCapture)
	require.NoError(t, err)
	r := reader.(*natsReader)
	subject := DeviceEventsSubject("test", "tenant-b", "device")
	_, err = m.js.Publish(subject, []byte("waiting"))
	require.NoError(t, err)
	require.NoError(t, m.Backpressure(streams.InboundEvents))
	setShardParkGate(t, m, "inbound-events-g1", true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*termGatePoll)
	_, err = r.ReadMessage(ctx)
	cancel()
	require.ErrorIs(t, err, io.EOF)
	info, err := m.js.ConsumerInfo(r.stream, r.durable)
	require.NoError(t, err)
	require.Zero(t, info.Delivered.Consumer, "a parked reader must spend no delivery")
	require.Zero(t, info.NumAckPending)
	require.Equal(t, []string{streams.InboundEvents, "inbound-events-g1"}, r.downstreams)
	r.ahead = true
	r.startAhead()
	require.Zero(t, r.aheadStarts.Load(), "fetch-ahead must check group 1 too")
	r.ahead = false
	setShardParkGate(t, m, "inbound-events-g1", false)
	n1Read(t, r, subject, "waiting")
	// With all gates open a fresh message is read without a park timeout.
	_, err = m.js.Publish(subject, []byte("open"))
	require.NoError(t, err)
	n1Read(t, r, subject, "open")
	// Losing registration must refuse rather than opening the forwarding path.
	gates := m.backpressure()
	gates.mu.Lock()
	delete(gates.gates, "inbound-events-g1")
	gates.mu.Unlock()
	var bp *BackpressureError
	require.ErrorAs(t, r.downstreamRefusal(), &bp)
	require.True(t, bp.Stale)
}

func TestGroupedZeroReaderIgnoresOtherShardGate(t *testing.T) {
	m := newBPService(t, startEmbeddedServer(t), "device-management", bpBounds{}).nmgr
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents, streams.ResolvedEvents)))
	_, err := m.NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)
	reader, err := m.NewReader(streams.InboundEvents, ReaderWithShardGroup(0))
	require.NoError(t, err)
	require.Equal(t, []string{streams.ResolvedEvents}, reader.(*natsReader).downstreams)
	setShardParkGate(t, m, "resolved-events-g1", true)
	_, err = m.js.Publish(ScopedSubject("test", "tenant-a", streams.InboundEvents), []byte("group zero"))
	require.NoError(t, err)
	n1Read(t, reader, "test.tenant-a.inbound-events", "group zero")
}

func TestForwardingPublishersDoNotRefuseFetchedMessages(t *testing.T) {
	for _, mode := range []string{"ordered", "round", "async-forwarding"} {
		t.Run(mode, func(t *testing.T) {
			m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
			require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
			var publish func() error
			switch mode {
			case "ordered", "async-forwarding":
				done := make(chan error, 1)
				if mode == "ordered" {
					w, err := m.NewOrderedWriter(streams.InboundEvents, WriterCapture, 4)
					require.NoError(t, err)
					defer w.Close()
					publish = func() error {
						w.Publish(n1Context("tenant-b"), Message{Value: []byte("forward")}, func(err error) { done <- err })
						select {
						case err := <-done:
							return err
						case <-time.After(3 * time.Second):
							t.Fatal("publish callback timed out")
							return nil
						}
					}
				} else {
					w, err := m.NewAsyncWriter(streams.InboundEvents, WriterCapture, 4, false)
					require.NoError(t, err)
					defer w.Close()
					publish = func() error {
						w.Submit(n1Context("tenant-b"), []Message{{Value: []byte("forward")}}, func(err error) { done <- err })
						select {
						case err := <-done:
							return err
						case <-time.After(3 * time.Second):
							t.Fatal("publish callback timed out")
							return nil
						}
					}
				}
			case "round":
				w, err := m.NewRoundPublisher(streams.InboundEvents)
				require.NoError(t, err)
				publish = func() error {
					_, err := w.Publish(n1Context("tenant-b"), Message{Value: []byte("forward")})
					return err
				}
			}
			setShardParkGate(t, m, "inbound-events-g1", true)
			require.NoError(t, publish())
			n1Stored(t, m, "inbound-events-g1", 1)
		})
	}
}

func TestReaderDownstreamsAndMqttStoreCoverEveryGroup(t *testing.T) {
	m := n1Manager(t, false)
	require.Equal(t, []string{streams.InboundEvents}, m.readerDownstreams(streams.ForwardsInto(streams.DeviceEventsCapture, "event-sources"), nil))
	require.Empty(t, m.readerDownstreams("", nil))
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
	m.Microservice.FunctionalArea = "event-sources"
	_, err := m.ensureStream(streams.InboundEvents)
	require.NoError(t, err)
	_, err = m.js.AddStream(&nats.StreamConfig{Name: MqttMessageStore, Subjects: []string{"$MQTT.msgs.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	reader, err := m.NewMqttMessageStoreReader(MqttFailClosedEvents)
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, []string{streams.InboundEvents, "inbound-events-g1"}, reader.downstreams)
	for _, suffix := range reader.downstreams {
		require.NoError(t, m.Backpressure(suffix))
	}
}

func TestShardedForwardingWriterConstructionGuard(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, area := range []string{"event-sources", "device-state"} {
			t.Run(area+"/"+map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
				m := newBPService(t, startEmbeddedServer(t), area, bpBounds{}).nmgr
				if enabled {
					require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
				}
				w, err := m.NewOrderedWriter(streams.InboundEvents, WriterCapture, 4)
				if enabled && area == "device-state" {
					require.ErrorContains(t, err, "must declare forwarding")
				} else {
					require.NoError(t, err)
					w.Close()
				}
				_, err = m.NewRoundPublisher(streams.InboundEvents)
				if enabled && area == "device-state" {
					require.ErrorContains(t, err, "must declare forwarding")
				} else if area == "event-sources" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "ReadBackWriters")
				}
				// Async callers retain their own admission policy even without a forwarding declaration.
				aw, err := m.NewAsyncWriter(streams.InboundEvents, WriterCapture, 4, true)
				require.NoError(t, err)
				aw.Close()
			})
		}
	}
}
