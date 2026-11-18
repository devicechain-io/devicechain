// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
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

func TestADeletionFactOnTheBrokerRemovesTheLiveView(t *testing.T) {
	host, port := startEmbeddedNats(t)
	savedMs, savedCfg, savedApi := Microservice, Configuration, Api
	savedState, savedSweep := StateMetrics, InactivitySweepMetrics
	savedProc, savedOwners, savedDeletion, savedOrphan := StateProcessors, OwnershipProcessor, DeletionProcessor, OrphanTask
	savedNats, savedSvc, savedWriter := NatsManager, Svc, InboundEventsWriter
	savedOwnerReader, savedStop := OwnershipReader, stopStreamSharding
	t.Cleanup(func() {
		Microservice, Configuration, Api = savedMs, savedCfg, savedApi
		StateMetrics, InactivitySweepMetrics = savedState, savedSweep
		StateProcessors, OwnershipProcessor, DeletionProcessor, OrphanTask = savedProc, savedOwners, savedDeletion, savedOrphan
		NatsManager, Svc, InboundEventsWriter = savedNats, savedSvc, savedWriter
		OwnershipReader, stopStreamSharding = savedOwnerReader, savedStop
	})
	area := fmt.Sprintf("device-state-delete-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: area, FunctionalArea: area, Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()
	OrphanTask = nil
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}, &model.DroppedMeasurement{}))
	Api = model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "tenant-a")
	_, err := Api.MergeDeviceState(ctx, "truck-7", time.Now(), nil, model.DeviceIdentity{})
	require.NoError(t, err)
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, NatsManager.Initialize(context.Background()))
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})
	t.Cleanup(func() {
		if err := beforeMicroserviceStopped(context.Background()); err != nil {
			t.Logf("stopping: %v", err)
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(context.Background()))
	require.NotNil(t, DeletionProcessor)
	require.NotNil(t, DeletionProcessor.Hold)
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	writer, err := NatsManager.NewWriter(streams.EntityDeleted)
	require.NoError(t, err)
	b, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityToken: "truck-7", EntityId: 17, DeletedTime: time.Now().UTC().Add(time.Minute)})
	require.NoError(t, err)
	require.NoError(t, writer.WriteMessages(ctx, messaging.Message{Key: []byte("truck-7"), Value: b}))
	require.Eventually(t, func() bool {
		var n int64
		return Api.RDB.DB(ctx).Unscoped().Model(&model.DeviceState{}).Where("device_token = ?", "truck-7").Count(&n).Error == nil && n == 0
	}, 15*time.Second, 50*time.Millisecond, "deletion fact never removed the live view")
}

// A shard group owns a device's EVENTS, not its state: with resolved-events split across two
// groups, a deletion fact still purges a device whichever group projected it. Each tenant is
// pinned to one group, and its state is written by that group's own processor before the fact
// arrives, so the group-1 device can only exist if group 1 is wired, and only disappears if
// the single deletion consumer reaches state that group 1 wrote.
//
// Negative controls, each planted and seen to fail when this test was written: wiring only
// group 0 (StateProcessors built from the first reader alone) fails the group-1 precondition,
// and a deletion consumer that never starts fails the purge for both groups.
func TestADeletionFactPurgesADeviceInEveryShardGroup(t *testing.T) {
	host, port := startEmbeddedNats(t)
	savedMs, savedCfg, savedApi, savedSvc, savedRdb := Microservice, Configuration, Api, Svc, RdbManager
	savedState, savedSweep, savedDropped := StateMetrics, InactivitySweepMetrics, DroppedEvicted
	savedProcs, savedOwners, savedDeletion, savedOrphan := StateProcessors, OwnershipProcessor, DeletionProcessor, OrphanTask
	savedNats, savedWriter, savedOwnerReader, savedStop := NatsManager, InboundEventsWriter, OwnershipReader, stopStreamSharding
	t.Cleanup(func() {
		Microservice, Configuration, Api, Svc, RdbManager = savedMs, savedCfg, savedApi, savedSvc, savedRdb
		StateMetrics, InactivitySweepMetrics, DroppedEvicted = savedState, savedSweep, savedDropped
		StateProcessors, OwnershipProcessor, DeletionProcessor, OrphanTask = savedProcs, savedOwners, savedDeletion, savedOrphan
		NatsManager, InboundEventsWriter, OwnershipReader, stopStreamSharding = savedNats, savedWriter, savedOwnerReader, savedStop
	})
	area := fmt.Sprintf("device-state-delete-groups-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: area, FunctionalArea: "device-state", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host, Port: port, StreamMaxBytes: 8 << 20, StreamMaxBytesCold: 1 << 20,
		ShardGroups: &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}},
	}
	Microservice.MicroserviceConfigurationRaw = []byte(`{"projection":{"writers":1}}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()
	OrphanTask = nil

	db := sqlitetest.OpenMemory(t, &gorm.Config{})
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTenantFence(db))
	require.NoError(t, db.AutoMigrate(&rdb.PurgedTenant{}, &model.DeviceState{}, &model.LatestMeasurement{},
		&model.LatestLocation{}, &model.DroppedMeasurement{}))
	RdbManager = &rdb.RdbManager{Database: db}
	Api = model.NewApi(RdbManager)
	ctx := context.Background()
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		kv, err := m.PartitionPlacementStore()
		if err != nil {
			return err
		}
		if err := partition.Publish(partition.FromKeyValue(kv), partition.Table{
			P: 2, Groups: []int{0, 1}, Generation: 1, Overrides: map[string]int{"tenant-zero": 0, "tenant-one": 1},
		}); err != nil {
			return err
		}
		return createNatsComponents(m)
	})
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, NatsManager.Initialize(ctx))
	StateProcessors, OwnershipProcessor, DeletionProcessor, stopStreamSharding = nil, nil, nil, nil
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})
	t.Cleanup(func() {
		if err := beforeMicroserviceStopped(ctx); err != nil {
			t.Logf("stopping: %v", err)
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx))
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	require.NotNil(t, DeletionProcessor)

	events, err := NatsManager.NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)
	at := time.Now().UTC().Truncate(time.Second)
	encoded, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source: "deletion-groups-test", SourceDeviceToken: "sensor", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5"}},
		}}},
	})
	require.NoError(t, err)
	rows := func(tctx context.Context, m any) int64 {
		var n int64
		if err := RdbManager.DB(tctx).Unscoped().Model(m).Where("device_token = ?", "sensor").Count(&n).Error; err != nil {
			return -1
		}
		return n
	}
	tenants := []string{"tenant-zero", "tenant-one"}
	for group, tenant := range tenants {
		tctx := core.WithTenant(ctx, tenant)
		require.NoError(t, events.WriteMessages(tctx, messaging.Message{Key: []byte("sensor"), Value: encoded}))
		require.Eventually(t, func() bool {
			return rows(tctx, &model.DeviceState{}) == 1 && rows(tctx, &model.LatestMeasurement{}) == 1
		}, 10*time.Second, 20*time.Millisecond, "group %d processor never projected %s's device", group, tenant)
	}

	deletions, err := NatsManager.NewWriter(streams.EntityDeleted)
	require.NoError(t, err)
	fact, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: entity.TypeDevice,
		EntityToken: "sensor", EntityId: 17, DeletedTime: time.Now().UTC().Add(time.Minute)})
	require.NoError(t, err)
	// Group 1 first: it is the group a group-0-only wiring would miss.
	for _, group := range []int{1, 0} {
		tctx := core.WithTenant(ctx, tenants[group])
		require.NoError(t, deletions.WriteMessages(tctx, messaging.Message{Key: []byte("sensor"), Value: fact}))
		require.Eventually(t, func() bool {
			return rows(tctx, &model.DeviceState{}) == 0 && rows(tctx, &model.LatestMeasurement{}) == 0
		}, 15*time.Second, 50*time.Millisecond, "deletion fact never purged the device group %d projected", group)
	}
}

// The single deletion consumer above is correct only while entity-deleted facts are not
// sharded: were they, a plain reader would read group 0's subject alone and every other
// group's deletions would sit unread. Sharding them must therefore stay refused by the
// instance config until device-state builds a deletion consumer per group.
func TestEntityDeletedFactsCannotBeShardedWhileOneConsumerReadsThem(t *testing.T) {
	err := (&mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.EntityDeleted}}).Validate()
	require.Error(t, err, "entity-deleted became shardable: device-state's single deletion consumer would miss groups > 0")
	require.NoError(t, (&mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}).Validate(),
		"control: the resolved-events stream the state processors shard on must stay shardable")
}
