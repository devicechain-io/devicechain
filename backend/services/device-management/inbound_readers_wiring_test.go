// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// resolution.inboundReaders reaches the BROKER: the processor the service builds keeps that
// many pull requests waiting on the inbound-events durable at once, counted by the broker
// itself (ConsumerInfo.NumWaiting) against an empty stream, where every reader's pull is a
// long poll. A document that omits the setting gets 2. One reader, the shape before the
// setting existed, holds 1.
//
// It is the number the setting exists to move: the r10 round trip bound the pod at one pull
// in flight, and a second replica lifted it by being a second pull.
func TestConfiguredInboundReadersHoldThatManyPullsAtTheBroker(t *testing.T) {
	savedMs, savedCfg, savedMetrics, savedDead, savedTransit := Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit
	savedReaders, savedResolved, savedFailed := InboundEventsReaders, ResolvedEventsWriter, FailedEventsWriter
	t.Cleanup(func() {
		Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit = savedMs, savedCfg, savedMetrics, savedDead, savedTransit
		InboundEventsReaders, ResolvedEventsWriter, FailedEventsWriter = savedReaders, savedResolved, savedFailed
	})
	host, port := startEmbeddedNats(t)
	nc, err := nats.Connect(fmt.Sprintf("nats://%s:%d", host, port))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"resolution":{"inboundReaders":3}}`, 3},
		// Counterweight: the default, so a wiring that always built 3 cannot pass both.
		{`{}`, 2},
		{`{"resolution":{"inboundReaders":1}}`, 1},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.MicroserviceConfigurationRaw = []byte(tc.doc)
			require.NoError(t, parseConfiguration())
			ResolveMetrics = processor.NewResolveMetrics(Microservice)
			DeadLetters = deadletter.NewProducer(Microservice)
			SecretTransit = testTransit(t)

			// The readers and writers are built in the manager's oncreate, as
			// createNatsComponents builds them, so the stream sampler that opens the
			// inbound readers' downstream gate starts after them, as it does in the service.
			instance := fmt.Sprintf("readers%d", time.Now().UnixNano())
			Microservice.InstanceId = instance
			Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
			nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
				func(n *messaging.NatsManager) error {
					if err := createInboundReaders(n); err != nil {
						return err
					}
					return createPublishWriters(n)
				})
			nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
				return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
					return messaging.MaxDeliveryLettered, nil
				}, nil
			})
			require.NoError(t, nmgr.Initialize(context.Background()))
			require.NoError(t, nmgr.Start(context.Background()))
			t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
			// The inbound readers park until resolved-events, the stream they forward into,
			// has been measured; the sampler's first tick is 5 s away, so measure it now.
			nmgr.MeasureBackpressureForTesting(t, streams.ResolvedEvents)
			require.NoError(t, nmgr.Backpressure(streams.ResolvedEvents), "precondition: the downstream gate is open")
			require.Len(t, InboundEventsReaders, tc.want, "inbound readers built")
			iproc := newInboundEventsProcessor()
			require.NoError(t, iproc.Initialize(context.Background()))
			require.NoError(t, iproc.Start(context.Background()))

			stream := messaging.StreamName(instance, streams.InboundEvents)
			durable := messaging.DurableName(instance, "device-management", streams.InboundEvents)
			// Sampled across several pull expiries (one second each). The count is the one
			// the broker shows most often: when a pull expires the reader's next one can
			// arrive a moment before the broker drops the old one, so a single sample can
			// read one high, never for long.
			seen := map[int]int{}
			for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
				ci, err := js.ConsumerInfo(stream, durable)
				require.NoError(t, err)
				seen[ci.NumWaiting]++
				time.Sleep(20 * time.Millisecond)
			}
			require.NoError(t, iproc.Stop(context.Background()))
			held, most := 0, 0
			for n, count := range seen {
				if count > most || (count == most && n < held) {
					held, most = n, count
				}
			}
			t.Logf("NumWaiting samples by value: %v", seen)
			require.Equal(t, tc.want, held, "pull requests held at the broker at once (samples by count: %v)", seen)
		})
	}
}
