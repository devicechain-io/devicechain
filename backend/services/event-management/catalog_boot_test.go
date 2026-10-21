// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔴 THE REAL WIRING UNDER THE REAL CATALOG. Every other test here builds a Microservice with
// Deployed=false, which the NATS manager does not hold to core/streams and core/kv. This one is
// Deployed, as core.NewMicroservice makes it in production, so a stream, reader mode or durable
// this service opens that its area does not declare fails here rather than crash-looping a pod.
//
// The database half (the Timescale policies, the backfill worker, the anchor sweep) needs a
// real Postgres and opens nothing on the broker, so it is left out; the NATS half is the
// service's own createNatsComponents and afterMicroserviceStarted.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	host, port := startEmbeddedNats(t)

	savedMs, savedCfg, savedApi := Microservice, Configuration, Api
	savedMetrics, savedBackfillMetrics := PersistMetrics, BackfillMetrics
	savedProc, savedRec, savedNats, savedSvc := EventPersistenceProcessor, EntityAnchorReconciler, NatsManager, Svc
	savedSweep, savedWorker := AnchorSweep, BackfillWorker
	savedRevents, savedFailed, savedDentity := ResolvedEventsReader, FailedEventsWriter, EntityDeletedReader
	t.Cleanup(func() {
		Microservice, Configuration, Api = savedMs, savedCfg, savedApi
		PersistMetrics, BackfillMetrics = savedMetrics, savedBackfillMetrics
		EventPersistenceProcessor, EntityAnchorReconciler, NatsManager, Svc = savedProc, savedRec, savedNats, savedSvc
		AnchorSweep, BackfillWorker = savedSweep, savedWorker
		ResolvedEventsReader, FailedEventsWriter, EntityDeletedReader = savedRevents, savedFailed, savedDentity
	})

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("catalog-boot-%d", time.Now().UnixNano()),
		FunctionalArea: "event-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()
	AnchorSweep, BackfillWorker = nil, nil

	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	Api = model.NewApi(&rdb.RdbManager{Database: db})

	// The real dead-letter recorder core/service installs is kept: it is catalog-checked too.
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog (main.go createNatsComponents: resolved-events reader, failed-events writer, entity-deleted reader)")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		// A refused start leaves no processors to stop; do not let a nil deref mask the refusal.
		if EventPersistenceProcessor != nil && EntityAnchorReconciler != nil {
			if err := beforeMicroserviceStopped(context.Background()); err != nil {
				t.Logf("stopping the service: %v", err)
			}
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (persistence processor, entity-anchor reconciler)")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	require.NotNil(t, ResolvedEventsReader, "resolved-events reader")
	require.NotNil(t, FailedEventsWriter, "failed-events writer")
	require.NotNil(t, EntityDeletedReader, "entity-deleted reader")

	// Opened lazily, per subscriber, after start: graphql/subscriptions_events.go:49 feeds the
	// events subscription from the tenant's resolved-events stream as a live (non-durable) feed.
	live, err := NatsManager.SubscribeLive(ctx, "acme", streams.ResolvedEvents)
	require.NoError(t, err, "live subscription on resolved-events (subscriptions_events.go:49)")
	require.NotNil(t, live)
}
