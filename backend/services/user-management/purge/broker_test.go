// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestBrokerErasesTenantFromEveryConfiguredGroup(t *testing.T) {
	nc := kvRig(t)
	js, err := nc.JetStream()
	require.NoError(t, err)
	cfg := &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
	for _, suffix := range cfg.Suffixes {
		for g := 0; g < cfg.Count; g++ {
			routed := messaging.GroupSuffix(suffix, g)
			_, err := js.AddStream(&nats.StreamConfig{Name: messaging.StreamName("inst1", routed), Subjects: []string{messaging.StreamSubject("inst1", routed)}})
			require.NoError(t, err)
			for _, tenant := range []string{"acme", "survivor"} {
				_, err = js.Publish(messaging.ScopedSubject("inst1", tenant, routed), []byte(tenant))
				require.NoError(t, err)
			}
		}
	}
	// Exercise the production store registration as well as Broker.Erase.
	stores := DefaultStores(nil, nil, kvConn{nc}, "inst1", nil, nil, cfg)
	var broker Store
	for _, store := range stores {
		if store.Name() == StoreBroker {
			broker = store
		}
	}
	require.NotNil(t, broker)
	out, err := broker.Erase(context.Background(), "acme", time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 6, out.Rows)
	for _, suffix := range cfg.Suffixes {
		for g := 0; g < cfg.Count; g++ {
			routed := messaging.GroupSuffix(suffix, g)
			info, err := js.StreamInfo(messaging.StreamName("inst1", routed))
			require.NoError(t, err)
			require.EqualValues(t, 1, info.State.Msgs)
			raw, err := js.GetMsg(info.Config.Name, info.State.LastSeq)
			require.NoError(t, err)
			require.Equal(t, "survivor", string(raw.Data))
		}
	}
}
