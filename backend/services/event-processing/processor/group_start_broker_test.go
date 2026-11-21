// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Start the production group readers under their generated grant, including term
// acquisition: legacy cleanup happens in BindTerm, after manager startup succeeds.
func TestShardedStartIssuesNoRefusedAdminCall(t *testing.T) {
	const instance = "groupstart"
	cfg := &mscfg.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{streams.ResolvedEvents}}
	b := permtest.Start(t, instance, permtest.WithoutReconcile(), permtest.WithOpenUser(), permtest.WithShardGroupPermissions(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	adminMS := &core.Microservice{InstanceId: instance}
	adminMS.UseMetricsRegistry(prometheus.NewRegistry())
	adminMS.InstanceConfiguration.Infrastructure.Nats = b.AdminNatsConfiguration()
	adminMS.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
	admin := messaging.NewBrokerAdmin(adminMS)
	require.NoError(t, admin.ExecuteInitialize(ctx))
	defer admin.Close()
	_, err := admin.ReconcileBroker(ctx, []string{"event-processing"})
	require.NoError(t, err)

	observer := b.ConnectOpen(t)
	js, err := observer.JetStream()
	require.NoError(t, err)
	baseStream := messaging.StreamName(instance, streams.ResolvedEvents)
	legacy := messaging.DurableName(instance, "event-processing", streams.ResolvedEvents)
	_, err = js.AddConsumer(baseStream, &nats.ConsumerConfig{
		Durable: legacy, AckPolicy: nats.AckExplicitPolicy,
		FilterSubject: messaging.StreamSubject(instance, streams.ResolvedEvents),
	})
	require.NoError(t, err)
	deletes, err := observer.SubscribeSync("$JS.API.CONSUMER.DELETE.>")
	require.NoError(t, err)
	require.NoError(t, observer.Flush())

	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing", Deployed: true}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = b.NatsConfiguration("event-processing")
	ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
	store, gate := brokerStore(t), NewTermGate()
	var readers []messaging.MessageReader
	manager := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(nm *messaging.NatsManager) error {
		if err := nm.EnableStreamSharding(groupTestRouter{}, cfg); err != nil {
			return err
		}
		var err error
		_, readers, err = groupInputs(nm, store, gate)
		return err
	})
	manager.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	require.NoError(t, manager.Initialize(ctx))
	t.Cleanup(func() { require.NoError(t, manager.Stop(context.Background())) })
	require.NoError(t, manager.Start(ctx))
	require.Len(t, readers, cfg.Count)
	_, err = js.ConsumerInfo(baseStream, legacy)
	require.NoError(t, err, "legacy cleanup must wait until leadership")

	gate.Enter(func() bool { return true })
	for term := 0; term < 2; term++ {
		for g, reader := range readers {
			bound := reader.(messaging.TermBoundReader)
			require.NoError(t, bound.BindTerm())
			physical := messaging.GroupSuffix(streams.ResolvedEvents, g)
			info, err := js.ConsumerInfo(messaging.StreamName(instance, physical),
				messaging.AckFloorDurableName(instance, "event-processing", physical))
			require.NoError(t, err)
			require.Equal(t, nats.AckAllPolicy, info.Config.AckPolicy)
			require.NoError(t, bound.UnbindTerm())
		}
	}
	_, err = js.ConsumerInfo(baseStream, legacy)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "group zero must still retire its real legacy durable")
	require.NoError(t, manager.Conn().Flush())
	b.RequireNoViolations(t)

	// Watching accepted requests also rejects a pointless delete whose name was
	// merely changed to fit the grant. Only group zero has a legacy name to retire.
	for term := 0; term < 2; term++ {
		msg, err := deletes.NextMsg(time.Second)
		require.NoError(t, err)
		require.Equal(t, "$JS.API.CONSUMER.DELETE."+baseStream+"."+legacy, msg.Subject)
	}
	pending, _, err := deletes.Pending()
	require.NoError(t, err)
	require.Zero(t, pending, "nonzero groups must issue no legacy delete, even one allowed by the grant")
}
