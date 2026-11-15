// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func unroutedReadConstructors() []struct {
	name string
	open func(*NatsManager, string) error
} {
	return []struct {
		name string
		open func(*NatsManager, string) error
	}{
		{"NewReader", func(m *NatsManager, s string) error { _, err := m.NewReader(s); return err }},
		{"SubscribeLive", func(m *NatsManager, s string) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err := m.SubscribeLive(ctx, "tenant-a", s)
			return err
		}},
		{"NewRangeReader", func(m *NatsManager, s string) error { _, err := m.NewRangeReader(s, 1, 1); return err }},
		{"NewReplayReader", func(m *NatsManager, s string) error { _, _, err := m.NewReplayReader(s, 1); return err }},
		{"NewReplayReaderFromTime", func(m *NatsManager, s string) error {
			_, _, err := m.NewReplayReaderFromTime(s, time.Now())
			return err
		}},
	}
}

func TestUnroutedReadConstructorsRefuse(t *testing.T) {
	// A broker lets each row reach ordinary construction when its guard is removed.
	srv := startEmbeddedServer(t)
	for _, tc := range unroutedReadConstructors() {
		for _, enforced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enforced=%t", tc.name, enforced), func(t *testing.T) {
				m, _ := metricsManager(t, srv)
				m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = n1Config(streams.ResolvedEvents)
				m.Microservice.Deployed = enforced
				err := tc.open(m, streams.ResolvedEvents)
				require.ErrorIs(t, err, ErrShardRoutingNotEnabled)
				require.ErrorContains(t, err, streams.ResolvedEvents)
				require.ErrorContains(t, err, "shardgroups.Enable")
			})
		}
	}
}

func TestUnroutedReadControls(t *testing.T) {
	srv := startEmbeddedServer(t)
	for _, tc := range unroutedReadConstructors() {
		for _, control := range []string{"nil-config", "unselected-suffix", "count-one"} {
			t.Run(tc.name+"/"+control, func(t *testing.T) {
				m, _ := metricsManager(t, srv)
				cfg := n1Config(streams.ResolvedEvents)
				switch control {
				case "nil-config":
					cfg = nil
				case "unselected-suffix":
					cfg = n1Config(streams.InboundEvents)
				case "count-one":
					cfg.Count = 1
				}
				m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
				w, err := m.NewWriter(streams.ResolvedEvents)
				require.NoError(t, err)
				require.NoError(t, w.WriteMessages(n1Context("tenant-a"), Message{Value: []byte("event")}))
				require.NoError(t, tc.open(m, streams.ResolvedEvents))
			})
		}
	}
	t.Run("enabled-group-zero", func(t *testing.T) {
		m, _ := metricsManager(t, srv)
		cfg := n1Config(streams.ResolvedEvents)
		m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
		require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
		_, err := m.NewReader(streams.ResolvedEvents, ReaderWithShardGroup(0))
		require.NoError(t, err)
	})
	t.Run("nil-microservice", func(t *testing.T) {
		require.NoError(t, (&NatsManager{}).refuseUnroutedRead(streams.ResolvedEvents))
	})
}

func unroutedWriteConstructors() []struct {
	name string
	open func(*NatsManager, string) error
} {
	return []struct {
		name string
		open func(*NatsManager, string) error
	}{
		{"NewWriter", func(m *NatsManager, s string) error { _, err := m.NewWriter(s); return err }},
		{"NewOrderedWriter", func(m *NatsManager, s string) error {
			w, err := m.NewOrderedWriter(s, WriterCapture, 4)
			if err == nil {
				w.Close()
			}
			return err
		}},
		{"NewAsyncWriter", func(m *NatsManager, s string) error {
			w, err := m.NewAsyncWriter(s, WriterCapture, 4, true)
			if err == nil {
				w.Close()
			}
			return err
		}},
		{"NewRoundPublisher", func(m *NatsManager, s string) error { _, err := m.NewRoundPublisher(s); return err }},
	}
}

func TestUnroutedWriteConstructorsRefuse(t *testing.T) {
	srv := startEmbeddedServer(t)
	for _, tc := range unroutedWriteConstructors() {
		for _, enforced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enforced=%t", tc.name, enforced), func(t *testing.T) {
				m, _ := metricsManager(t, srv)
				m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = n1Config(streams.InboundEvents)
				m.Microservice.Deployed = enforced
				err := tc.open(m, streams.InboundEvents)
				require.ErrorIs(t, err, ErrShardRoutingNotEnabled)
				require.ErrorContains(t, err, streams.InboundEvents)
				require.ErrorContains(t, err, "shardgroups.Enable")
			})
		}
	}
}

func TestUnroutedWriteControls(t *testing.T) {
	srv := startEmbeddedServer(t)
	for _, tc := range unroutedWriteConstructors() {
		for _, control := range []string{"nil-config", "unselected-suffix", "count-one", "enabled-routing"} {
			t.Run(tc.name+"/"+control, func(t *testing.T) {
				m, _ := metricsManager(t, srv)
				cfg := n1Config(streams.InboundEvents)
				switch control {
				case "nil-config":
					cfg = nil
				case "unselected-suffix":
					cfg = n1Config(streams.ResolvedEvents)
				case "count-one":
					cfg.Count = 1
				case "enabled-routing":
					require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
				}
				m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
				require.NoError(t, tc.open(m, streams.InboundEvents))
			})
		}
	}
}
