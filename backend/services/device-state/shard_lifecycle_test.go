// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Unlike the reader-binding probe, this drives the service hooks and leaves all
// reads to the real projection workers. Both groups must make database progress.
func TestServiceHooksStartAndStopEveryResolvedEventsProcessor(t *testing.T) {
	host, port := startEmbeddedNats(t)
	savedMs, savedCfg, savedApi, savedSvc := Microservice, Configuration, Api, Svc
	savedMetrics, savedSweep, savedDropped := StateMetrics, InactivitySweepMetrics, DroppedEvicted
	savedProcs, savedOwners := StateProcessors, OwnershipProcessor
	savedWriter, savedOwnerReader, savedStop := InboundEventsWriter, OwnershipReader, stopStreamSharding
	savedRdb := RdbManager
	t.Cleanup(func() {
		Microservice, Configuration, Api, Svc = savedMs, savedCfg, savedApi, savedSvc
		StateMetrics, InactivitySweepMetrics, DroppedEvicted = savedMetrics, savedSweep, savedDropped
		StateProcessors, OwnershipProcessor = savedProcs, savedOwners
		InboundEventsWriter, OwnershipReader, stopStreamSharding = savedWriter, savedOwnerReader, savedStop
		RdbManager = savedRdb
	})
	Microservice = &core.Microservice{InstanceId: "state-lifecycle", FunctionalArea: "device-state", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host, Port: port, StreamMaxBytes: 8 << 20, StreamMaxBytesCold: 1 << 20,
		ShardGroups: &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}},
	}
	Microservice.MicroserviceConfigurationRaw = []byte(`{"projection":{"writers":1}}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	db := sqlitetest.OpenMemory(t, &gorm.Config{})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTenantFence(db))
	require.NoError(t, db.AutoMigrate(&rdb.PurgedTenant{}, &model.DeviceState{}, &model.LatestMeasurement{}))
	RdbManager = &rdb.RdbManager{Database: db}
	Api = model.NewApi(RdbManager)
	ctx := context.Background()
	mgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		kv, err := m.PartitionPlacementStore()
		if err != nil {
			return err
		}
		// Explicit placements exercise the routed writer for each group.
		if err := partition.Publish(partition.FromKeyValue(kv), partition.Table{
			P: 2, Groups: []int{0, 1}, Generation: 1, Overrides: map[string]int{"tenant-zero": 0, "tenant-one": 1},
		}); err != nil {
			return err
		}
		return createNatsComponents(m)
	})
	mgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, mgr.Initialize(ctx))
	StateProcessors, OwnershipProcessor, stopStreamSharding = nil, nil, nil
	// Only the broker needs lifecycle management here: SQLite is already open,
	// and exercising these hooks does not need an HTTP server or live Postgres.
	Svc = service.FromManagers(Microservice, service.Managers{Nats: mgr})
	t.Cleanup(func() {
		// Also unwind failed assertions/mutations. Stop/Terminate may be refused
		// because the hook or the stop assertions below already completed them.
		for _, sp := range StateProcessors {
			_ = sp.Stop(ctx)
			_ = sp.Terminate(ctx)
		}
		if OwnershipProcessor != nil {
			_ = OwnershipProcessor.Stop(ctx)
			_ = OwnershipProcessor.Terminate(ctx)
		}
		if stopStreamSharding != nil {
			stopStreamSharding()
		}
		_ = mgr.Stop(ctx)
		_ = mgr.Terminate(ctx)
	})

	require.NoError(t, afterMicroserviceStarted(ctx))
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	require.Len(t, StateProcessors, 2)
	writer, err := mgr.NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)
	at := time.Now().UTC().Truncate(time.Second)
	encoded, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source: "lifecycle-test", SourceDeviceToken: "sensor", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5"}},
		}}},
	})
	require.NoError(t, err)
	for group, tenant := range []string{"tenant-zero", "tenant-one"} {
		tctx := core.WithTenant(ctx, tenant)
		require.NoError(t, writer.WriteMessages(tctx, messaging.Message{Key: []byte("sensor"), Value: encoded}))
		require.Eventually(t, func() bool {
			var state model.DeviceState
			err := RdbManager.DB(tctx).Where("device_token = ?", "sensor").First(&state).Error
			return err == nil && state.LastActivityTime.Valid && state.LastActivityTime.Time.Equal(at)
		}, 5*time.Second, 10*time.Millisecond, "group %d processor did not project its resolved event", group)
	}

	require.NoError(t, beforeMicroserviceStopped(ctx))
	for group, sp := range StateProcessors {
		// Terminate is permitted ONLY from Stopped. This observes the lifecycle
		// without exposing its private state or issuing a Stop that masks a missed
		// hook call. A still-Started group must fail here, even after NATS drains.
		require.NoError(t, sp.Terminate(ctx), "group %d processor was not stopped by the service hook", group)
	}
}
