// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔑 THE START, UNDER THE CATALOG. Every other test here builds a Microservice with
// Deployed=false, so no test runs this service's real broker wiring while the catalog is
// enforced. This one does: a Deployed microservice goes through the same service.New /
// Initialize / Start path as main.go, with the real createNatsComponents and core/service's
// REAL dead-letter max-delivery recorder, so a stream, reader mode or durable consumer that
// core/streams does not declare for outbound-connectors fails here instead of crash-looping
// the pod.
//
// This service opens nothing lazily: its only broker handles are the three in
// createNatsComponents (main.go: ConnectorDispatchDead writer, DeadLetters writer via
// newDeadLetterIndexWriter, ConnectorDispatch reader via newDispatchReader), plus the
// max-delivery recorder core/service installs.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalogboot%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so this
	// is also the area's permission test: the real wiring runs against it and the broker's log of
	// refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	savedMs, savedCfg, savedSvc, savedNats := Microservice, Configuration, Svc, NatsManager
	savedMetrics, savedUnres, savedDead := DispatchMetrics, EgressUnresolved, DeadLetters
	savedGates, savedLimiter, savedConsumer := safetyGates, RateLimiter, Consumer
	t.Cleanup(func() {
		Microservice, Configuration, Svc, NatsManager = savedMs, savedCfg, savedSvc, savedNats
		DispatchMetrics, EgressUnresolved, DeadLetters = savedMetrics, savedUnres, savedDead
		safetyGates, RateLimiter, Consumer = savedGates, savedLimiter, savedConsumer
	})

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "outbound-connectors",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = broker.NatsConfiguration("outbound-connectors")
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())

	safetyGates = governance.NewSafetyGates(Microservice)
	// As afterMicroserviceInitialized: core/service builds the broker side, buildMetrics hands
	// the producer on, the limiter is the unconfigured (platform default) one.
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	buildMetrics()
	RateLimiter = buildEgressLimiter(Configuration, Microservice.InstanceConfiguration.Infrastructure, EgressUnresolved)

	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initializing under the catalog")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
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

	// afterMicroserviceStarted: Svc.Start builds the connector-dispatch(+dead) writers and the
	// dispatch reader through createNatsComponents, then the consumer starts.
	require.NoError(t, afterMicroserviceStarted(ctx),
		"starting under the catalog: connector-dispatch reader, connector-dispatch-dead and dead-letters writers, max-delivery recorder")
	require.NotNil(t, Consumer, "createNatsComponents built no dispatch consumer")
	broker.RequireNoViolations(t)
}
