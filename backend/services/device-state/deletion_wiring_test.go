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
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestADeletionFactOnTheBrokerRemovesTheLiveView(t *testing.T) {
	host, port := startEmbeddedNats(t)
	savedMs, savedCfg, savedApi := Microservice, Configuration, Api
	savedState, savedSweep := StateMetrics, InactivitySweepMetrics
	savedProc, savedOwners, savedDeletion, savedOrphan := StateProcessor, OwnershipProcessor, DeletionProcessor, OrphanTask
	savedNats, savedSvc, savedWriter := NatsManager, Svc, InboundEventsWriter
	savedReader, savedOwnerReader := ResolvedEventsReader, OwnershipReader
	t.Cleanup(func() {
		Microservice, Configuration, Api = savedMs, savedCfg, savedApi
		StateMetrics, InactivitySweepMetrics = savedState, savedSweep
		StateProcessor, OwnershipProcessor, DeletionProcessor, OrphanTask = savedProc, savedOwners, savedDeletion, savedOrphan
		NatsManager, Svc, InboundEventsWriter = savedNats, savedSvc, savedWriter
		ResolvedEventsReader, OwnershipReader = savedReader, savedOwnerReader
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
