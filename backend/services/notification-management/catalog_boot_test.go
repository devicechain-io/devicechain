// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-notification-management/processor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔴 THE REAL WIRING UNDER THE REAL CATALOG. Every other test here builds a Microservice with
// Deployed=false, which the NATS manager does not hold to core/streams and core/kv. This one is
// Deployed, as core.NewMicroservice makes it in production, so a stream, reader mode or durable
// this service opens that its area does not declare fails here rather than crash-looping a pod.
//
// The database half (policies, the secret store, the retention sweep and the escalation
// scheduler) opens nothing on the broker and is left out; the NATS half is the service's own
// createNatsComponents and afterMicroserviceStarted.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalog-boot-%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so this
	// is also the area's permission test: the real wiring runs against it and the broker's log of
	// refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	savedMs, savedCfg, savedSvc, savedNats := Microservice, Configuration, Svc, NatsManager
	savedMetrics, savedDead, savedNotifier := NotifyMetrics, DeadLetters, Notifier
	savedProc, savedReader := NotificationProcessor, AlarmEventsReader
	savedSweep, savedEsc := RetentionSweeper, EscalationScheduler
	t.Cleanup(func() {
		Microservice, Configuration, Svc, NatsManager = savedMs, savedCfg, savedSvc, savedNats
		NotifyMetrics, DeadLetters, Notifier = savedMetrics, savedDead, savedNotifier
		NotificationProcessor, AlarmEventsReader = savedProc, savedReader
		RetentionSweeper, EscalationScheduler = savedSweep, savedEsc
	})

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "notification-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = broker.NatsConfiguration("notification-management")
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	RetentionSweeper, EscalationScheduler = nil, nil

	// The real dead-letter recorder core/service installs is kept: it is catalog-checked too.
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	buildMetrics()
	// createNatsComponents hands the dead-letter sink to the notifier; the notifier's database
	// and secret dependencies are not exercised by starting.
	Notifier = processor.NewPolicyNotifier(nil, nil, 1, time.Second, func(string) bool { return false }, nil, NotifyMetrics)

	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog (main.go createNatsComponents: alarm-events durable reader, dead-letters writer)")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		// A refused start leaves no processor to stop; do not let a nil deref mask the refusal.
		if NotificationProcessor != nil {
			if err := beforeMicroserviceStopped(context.Background()); err != nil {
				t.Logf("stopping the service: %v", err)
			}
		}
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (notification processor)")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	require.NotNil(t, AlarmEventsReader, "alarm-events reader")
	// The dead-letter writer is opened once, in createNatsComponents via newDeadLetterSink
	// (main.go:114); the service has no other NewWriter/NewReader/SubscribeLive/KeyValueStore
	// call site, so nothing is opened lazily after start.
	broker.RequireNoViolations(t)
}
