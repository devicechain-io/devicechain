// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestShardedWritersCheckRoutedBackpressure(t *testing.T) {
	for _, suffix := range []string{streams.InboundEvents, streams.ResolvedEvents} {
		for _, mode := range []string{"sync", "ordered", "async", "round"} {
			t.Run(suffix+"/"+mode, func(t *testing.T) {
				m := n1Manager(t, true)
				await := func(send func(func(error))) error {
					done := make(chan error, 1)
					send(func(err error) { done <- err })
					select {
					case err := <-done:
						return err
					case <-time.After(5 * time.Second):
						t.Fatal("callback timed out")
						return nil
					}
				}
				var publish func(context.Context) error
				switch mode {
				case "sync":
					w, err := m.NewWriter(suffix)
					require.NoError(t, err)
					publish = func(ctx context.Context) error { return w.WriteMessages(ctx, Message{Value: []byte("value")}) }
				case "ordered":
					w, err := m.NewOrderedWriter(suffix, WriterResolution, 4)
					require.NoError(t, err)
					defer w.Close()
					publish = func(ctx context.Context) error {
						return await(func(done func(error)) { w.Publish(ctx, Message{Value: []byte("value")}, done) })
					}
				case "async":
					w, err := m.NewAsyncWriter(suffix, WriterResolution, 4, true)
					require.NoError(t, err)
					defer w.Close()
					publish = func(ctx context.Context) error {
						return await(func(done func(error)) { w.Submit(ctx, []Message{{Value: []byte("value")}}, done) })
					}
				case "round":
					w, err := m.NewRoundPublisher(suffix)
					require.NoError(t, err)
					publish = func(ctx context.Context) error { _, err := w.Publish(ctx, Message{Value: []byte("value")}); return err }
				}
				routed := GroupSuffix(suffix, 1)
				gates := m.backpressure()
				gates.mu.Lock()
				groupGate := gates.gates[routed]
				gates.mu.Unlock()
				require.NotNil(t, groupGate, "the routed stream must have a registered gate")
				require.False(t, groupGate.sampledAt.IsZero(), "registration must measure the group stream")
				require.Contains(t, m.gatingDurables(routed), DurableName("test", streams.BackpressureReadersFor(suffix)[0], routed), "group durable names must retain the group")
				// Use the broker measurement path and check which stream/durables it actually queries.
				original := gates.consumerInfo
				var queried []string
				gates.consumerInfo = func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
					queried = append(queried, stream+"/"+durable)
					return original(ctx, stream, durable)
				}
				m.sampleBackpressure(context.Background(), routed)
				gates.consumerInfo = original
				require.NotEmpty(t, queried)
				for _, query := range queried {
					require.Contains(t, query, routed)
				}
				gates.mu.Lock()
				groupGate.closed = true
				groupGate.sampledAt = time.Now()
				gates.mu.Unlock()
				err := publish(n1Context("tenant-b"))
				var bp *BackpressureError
				require.ErrorAs(t, err, &bp)
				require.Equal(t, StreamName("test", routed), bp.Stream)
				require.NoError(t, publish(n1Context("tenant-a")), "group zero remains open")
				n1Stored(t, m, routed, 0)
				require.Positive(t, gates.gates[suffix].pubMsgs.Load())
				gates.mu.Lock()
				groupGate.closed = false
				gates.mu.Unlock()
				require.NoError(t, publish(n1Context("tenant-b")))
				require.Positive(t, groupGate.pubMsgs.Load(), "volume belongs to the receiving group")
				// A missing group gate must fail closed, even while group zero remains healthy.
				gates.mu.Lock()
				delete(gates.gates, routed)
				gates.mu.Unlock()
				err = publish(n1Context("tenant-b"))
				require.ErrorAs(t, err, &bp)
				require.Equal(t, StreamName("test", routed), bp.Stream)
				require.True(t, bp.Stale)
			})
		}
	}
}

func TestShardedReplayAndRangeReadersRefuse(t *testing.T) {
	m := n1Manager(t, true)
	_, _, err := m.NewReplayReader(streams.ResolvedEvents, 1)
	require.ErrorContains(t, err, "sequence replay not supported for a sharded suffix yet")
	_, _, err = m.NewReplayReaderFromTime(streams.ResolvedEvents, time.Now())
	require.ErrorContains(t, err, "time replay not supported for a sharded suffix yet")
	for _, to := range []uint64{1, rangeDirectMax + 1} {
		_, err = m.NewRangeReader(streams.ResolvedEvents, 1, to)
		require.ErrorContains(t, err, "range reader not supported for a sharded suffix yet")
	}
	// Unselected suffixes keep the empty replay behavior.
	_, _, err = m.NewReplayReader(streams.FailedDecode, 1)
	require.NoError(t, err)
}

func TestShardedTenantPurgeReachesEveryGroup(t *testing.T) {
	m := n1Manager(t, false)
	cfg := n1Config(streams.InboundEvents, streams.ResolvedEvents)
	cfg.Count = 3
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
	for _, suffix := range cfg.Suffixes {
		_, err := m.ensureStream(suffix)
		require.NoError(t, err)
		for g := 0; g < cfg.Count; g++ {
			for _, tenant := range []string{"tenant-a", "tenant-b"} {
				_, err := m.js.Publish(ScopedSubject("test", tenant, GroupSuffix(suffix, g)), []byte(tenant))
				require.NoError(t, err)
			}
		}
	}
	res, err := m.PurgeTenant(context.Background(), "tenant-b")
	require.NoError(t, err)
	require.EqualValues(t, 6, res.Messages)
	require.EqualValues(t, 1, res.PerStream["test_inbound-events-g1"])
	for _, suffix := range cfg.Suffixes {
		for g := 0; g < cfg.Count; g++ {
			routed := GroupSuffix(suffix, g)
			n1Stored(t, m, routed, 1)
			remaining, err := m.js.GetLastMsg(StreamName("test", routed), ScopedSubject("test", "tenant-a", routed))
			require.NoError(t, err)
			require.Equal(t, "tenant-a", string(remaining.Data))
			_, err = m.js.GetLastMsg(StreamName("test", routed), ScopedSubject("test", "tenant-b", routed))
			require.ErrorIs(t, err, nats.ErrMsgNotFound)
		}
	}
	res, err = m.PurgeTenant(context.Background(), "tenant-b")
	require.NoError(t, err)
	require.Zero(t, res.Messages)
}

func TestShardedAdvisorySubjectsIncludeEnabledGroups(t *testing.T) {
	m := n1Manager(t, false)
	base := m.streamSubjects(streams.MaxDeliveries)
	cfg := n1Config(streams.InboundEvents)
	cfg.Count = 3
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
	want := append(base,
		AdvisorySubject(StreamName("test", GroupSuffix(streams.InboundEvents, 1)), "*"),
		AdvisorySubject(StreamName("test", GroupSuffix(streams.InboundEvents, 2)), "*"))
	require.Equal(t, want, m.streamSubjects(streams.MaxDeliveries))
	require.Equal(t, want, m.newStreamConfig(streams.MaxDeliveries).Subjects)
}

func TestEnableStreamShardingRefusesAfterFirstHandle(t *testing.T) {
	constructors := map[string]func(*NatsManager){
		"writer": func(m *NatsManager) { _, err := m.NewWriter(streams.InboundEvents); require.NoError(t, err) },
		"async": func(m *NatsManager) {
			w, err := m.NewAsyncWriter(streams.InboundEvents, WriterResolution, 4, true)
			require.NoError(t, err)
			w.Close()
		},
		"ordered": func(m *NatsManager) {
			w, err := m.NewOrderedWriter(streams.InboundEvents, WriterResolution, 4)
			require.NoError(t, err)
			w.Close()
		},
		"round": func(m *NatsManager) { _, err := m.NewRoundPublisher(streams.InboundEvents); require.NoError(t, err) },
		"reader": func(m *NatsManager) {
			_, err := m.NewReader(streams.InboundEvents)
			require.NoError(t, err)
		},
		"ensure": func(m *NatsManager) { _, err := m.ensureStream(streams.InboundEvents); require.NoError(t, err) },
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			m := n1Manager(t, false)
			construct(m)
			require.ErrorContains(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)), "after constructing a handle or ensuring a stream")
			require.Nil(t, m.streamSharding())
		})
	}
}
