// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔑 THE WIRING, THROUGH THE BROKER. A device ownership-changed fact published on
// device-management's stream reaches the projection through the consumer createNatsComponents
// builds and afterMicroserviceStarted starts: nothing else runs those two lines, and a service
// that built the consumer on the wrong stream, or never started it, would leave every
// processor test green while no device ever left its old owner's view.
//
// 🔴 It therefore drives the service's OWN start and stop hooks, not a copy of them: an
// earlier shape of this test called OwnershipProcessor.Start itself, and stayed green with
// the Start line deleted from afterMicroserviceStarted — a consumer built and never
// started, which is precisely the wiring fault it exists to catch.
func TestAnOwnershipFactOnTheBrokerRestampsTheLiveView(t *testing.T) {
	host, port := startEmbeddedNats(t)

	savedMs, savedCfg, savedApi := Microservice, Configuration, Api
	savedState, savedSweep := StateMetrics, InactivitySweepMetrics
	savedProc, savedOwners, savedNats, savedSvc := StateProcessor, OwnershipProcessor, NatsManager, Svc
	savedWriter := InboundEventsWriter
	t.Cleanup(func() {
		Microservice, Configuration, Api = savedMs, savedCfg, savedApi
		StateMetrics, InactivitySweepMetrics = savedState, savedSweep
		StateProcessor, OwnershipProcessor, NatsManager, Svc = savedProc, savedOwners, savedNats, savedSvc
		InboundEventsWriter = savedWriter
	})

	area := fmt.Sprintf("device-state-owners-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: area, FunctionalArea: area, Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	// A shared-cache database: the consumer writes from its own goroutine, on a connection
	// of its own, and a bare ":memory:" would give that connection an empty database.
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceState{}))
	Api = model.NewApi(&rdb.RdbManager{Database: db})
	tenant := core.WithTenant(context.Background(), "acme-tenant")
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	_, err := Api.MergeDeviceState(tenant, "pump-1", t0, nil,
		model.DeviceIdentity{Owners: model.Owners{Anchors: []string{"customer:acme"}, At: t0}})
	require.NoError(t, err)

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	// core/service installs the dead-letter recorder in production; nothing here runs out of
	// deliveries, so one that records nothing stands in for it.
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, NatsManager.Initialize(context.Background()))
	// The NATS manager is started by afterMicroserviceStarted through core/service, as in
	// production (no Rdb or GraphQL manager here: a nil entry is skipped, not refused), and
	// stopped by beforeMicroserviceStopped, which stops both consumers first.
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})
	t.Cleanup(func() {
		if err := beforeMicroserviceStopped(context.Background()); err != nil {
			t.Logf("stopping the service: %v", err)
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(context.Background()))
	require.NotNil(t, OwnershipProcessor, "createNatsComponents built no ownership consumer")
	// Readers fetch only once the service is ready, as in production after the auth gate opens.
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	// Published exactly as device-management publishes it: its own writer, on the tenant's
	// subject, carrying the device's whole current set.
	writer, err := NatsManager.NewWriter(streams.DeviceOwnership)
	require.NoError(t, err)
	fact, err := dmproto.MarshalDeviceOwnershipChangedEvent(&dmmodel.DeviceOwnershipChangedEvent{
		DeviceToken: "pump-1", Anchors: []string{"customer:globex"}, ChangedTime: t0.Add(time.Minute)})
	require.NoError(t, err)
	require.NoError(t, writer.WriteMessages(tenant, messaging.Message{Key: []byte("pump-1"), Value: fact}))

	require.Eventually(t, func() bool {
		var row model.DeviceState
		if err := db.WithContext(tenant).Where("device_token = ?", "pump-1").First(&row).Error; err != nil {
			return false
		}
		return len(row.ScopeAnchors) == 1 && row.ScopeAnchors[0] == "customer:globex"
	}, 15*time.Second, 50*time.Millisecond, "the fact never restamped the device's live view")
}
