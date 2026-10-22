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
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔴 THE REAL WIRING UNDER THE REAL CATALOG. Every other test here builds a Microservice with
// Deployed=false, which the NATS manager does not hold to core/streams and core/kv. This one is
// Deployed, as core.NewMicroservice makes it in production, so a stream, reader mode or durable
// this service opens that its area does not declare fails here rather than crash-looping a pod.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	host, port := startEmbeddedNats(t)

	savedMs, savedCfg, savedApi := Microservice, Configuration, Api
	savedState, savedSweep := StateMetrics, InactivitySweepMetrics
	savedProc, savedOwners, savedNats, savedSvc := StateProcessor, OwnershipProcessor, NatsManager, Svc
	savedWriter, savedRdr, savedOwnRdr := InboundEventsWriter, ResolvedEventsReader, OwnershipReader
	t.Cleanup(func() {
		Microservice, Configuration, Api = savedMs, savedCfg, savedApi
		StateMetrics, InactivitySweepMetrics = savedState, savedSweep
		StateProcessor, OwnershipProcessor, NatsManager, Svc = savedProc, savedOwners, savedNats, savedSvc
		InboundEventsWriter, ResolvedEventsReader, OwnershipReader = savedWriter, savedRdr, savedOwnRdr
	})

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("catalog-boot-%d", time.Now().UnixNano()),
		FunctionalArea: "device-state",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceState{}))
	Api = model.NewApi(&rdb.RdbManager{Database: db})

	// As afterMicroserviceInitialized builds it, minus the database and GraphQL halves; the
	// real dead-letter recorder core/service installs is kept (it is catalog-checked too).
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog (main.go createNatsComponents: resolved-events reader, inbound-events writer, device-ownership reader)")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		// A refused start leaves no processors to stop; do not let a nil deref mask the refusal.
		if StateProcessor != nil && OwnershipProcessor != nil {
			if err := beforeMicroserviceStopped(context.Background()); err != nil {
				t.Logf("stopping the service: %v", err)
			}
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (both processors)")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	require.NotNil(t, ResolvedEventsReader, "resolved-events reader")
	require.NotNil(t, InboundEventsWriter, "inbound-events writer")
	require.NotNil(t, OwnershipReader, "device-ownership reader")
	// No lazily opened streams: the service has no other NewWriter/NewReader/SubscribeLive/
	// KeyValueStore call site outside createNatsComponents.
}
