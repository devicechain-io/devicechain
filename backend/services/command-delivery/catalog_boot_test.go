// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
)

// TestStartsUnderItsDeclaredCatalog starts this service's real broker wiring with
// Deployed set, the one condition under which core/messaging holds the area to what
// core/streams and core/kv declare for it. Every other test builds a Microservice literal
// with Deployed=false, so a stream, reader mode or consumer this service uses but the
// catalog does not declare would pass CI and crash-loop the pod.
//
// createNatsComponents opens every reader and writer this service has (main.go:
// CommandResponses/DeadLetters/DeviceRoster readers, DeviceCommands/DeadLetters writers)
// and creates their consumers at NewReader time, and Svc.Start installs the real
// max-delivery recorder, so Initialize + Start covers them all. The service opens nothing
// lazily after start and uses no KV bucket.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevRdb, prevApi, prevSvc := Microservice, Configuration, NatsManager, RdbManager, Api, Svc
	prevProc, prevWriteback, prevRoster := CommandDeliveryProcessor, DeadLetterWriteback, RosterConsumer
	prevResponses, prevCommands := CommandResponsesReader, DeviceCommandsWriter
	prevDelivery, prevWritebackMetrics, prevDL := DeliveryMetrics, WritebackMetrics, DeadLetters
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, RdbManager, Api, Svc = prevMs, prevCfg, prevNats, prevRdb, prevApi, prevSvc
		CommandDeliveryProcessor, DeadLetterWriteback, RosterConsumer = prevProc, prevWriteback, prevRoster
		CommandResponsesReader, DeviceCommandsWriter = prevResponses, prevCommands
		DeliveryMetrics, WritebackMetrics, DeadLetters = prevDelivery, prevWritebackMetrics, prevDL
	})

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("catalogboot%d", time.Now().UnixNano()),
		FunctionalArea: "command-delivery",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Configuration = &config.CommandDeliveryConfiguration{SweepIntervalSeconds: config.DefaultSweepIntervalSeconds}

	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	buildMetrics()
	// Built but never initialized (that needs Postgres); model.NewApi only stores it.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil,
		Microservice.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})
	Api = model.NewApi(RdbManager)

	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		_ = Svc.Stop(ctx)
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, Svc.Start(ctx),
		"start under the catalog: command-responses/device-commands/dead-letters/device-roster readers and writers and the max-delivery recorder")
	require.NotNil(t, CommandResponsesReader)
	require.NotNil(t, DeviceCommandsWriter)
	require.NotNil(t, RosterConsumer)
}
