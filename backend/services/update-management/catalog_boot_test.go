// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔑 THE START, UNDER THE CATALOG. Every other test here builds a Microservice with
// Deployed=false, so none runs this service's real broker wiring while the catalog is
// enforced. This one does: a Deployed microservice goes through the same service.New /
// Initialize / Start path as main.go (afterMicroserviceInitialized builds Svc with
// createNatsComponents as the oncreate callback), with core/service's REAL dead-letter
// max-delivery recorder, so a stream, reader mode or durable consumer core/streams does not
// declare for update-management fails here instead of crash-looping the pod.
//
// This service opens nothing lazily: its only broker handles are the four in
// createNatsComponents (main.go: DeviceDesired writer, DeadLetters writer via
// newDeadLetterSink, DeviceReports reader, DeviceRoster reader with DeliverNew), and
// afterMicroserviceStarted starts both consumers.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevApi, prevSvc, prevRdb := Microservice, Configuration, NatsManager, Api, Svc, RdbManager
	prevReports, prevRoster, prevMetrics, prevDead, prevDL := ReportsConsumer, RosterConsumer, ReportsMetrics, DeadLetters, DownloadStats
	prevRR, prevRoR, prevSweeper := ReportsReader, RosterReader, Sweeper
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, Api, Svc, RdbManager = prevMs, prevCfg, prevNats, prevApi, prevSvc, prevRdb
		ReportsConsumer, RosterConsumer, ReportsMetrics, DeadLetters, DownloadStats = prevReports, prevRoster, prevMetrics, prevDead, prevDL
		ReportsReader, RosterReader, Sweeper = prevRR, prevRoR, prevSweeper
	})

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("catalogboot%d", time.Now().UnixNano()),
		FunctionalArea: "update-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())

	// As afterMicroserviceInitialized: metrics once, then core/service over the oncreate
	// callback. Only the relational side is stubbed (no database is opened here): Api exists
	// because createNatsComponents wires Api.Desired and the consumers into it.
	buildMetrics()
	RdbManager = &rdb.RdbManager{}
	Api = model.NewApi(RdbManager, nil, model.Limits{})
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initializing under the catalog")
	NatsManager = Svc.Nats
	DeadLetters = Svc.DeadLetters
	t.Cleanup(func() {
		Sweeper = nil // the sweep is a database loop, not started here
		if err := beforeMicroserviceStopped(context.Background()); err != nil {
			t.Logf("stopping the service: %v", err)
		}
		if NatsManager != nil {
			if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
				c.Close()
			}
		}
	})
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	// afterMicroserviceStarted without startSweeper: Svc.Start (writers, readers, recorder),
	// then the two consumers.
	require.NoError(t, Svc.Start(ctx),
		"starting under the catalog: device-desired and dead-letters writers, device-reports and device-roster readers, max-delivery recorder")
	require.NotNil(t, ReportsConsumer, "createNatsComponents built no device-reports consumer")
	require.NotNil(t, RosterConsumer, "createNatsComponents built no device-roster consumer")
	require.NotNil(t, Api.Desired, "createNatsComponents wired no device-desired publisher")
	require.NoError(t, ReportsConsumer.Start(ctx), "starting the device-reports consumer (durable)")
	require.NoError(t, RosterConsumer.Start(ctx), "starting the device-roster consumer (durable)")
}
