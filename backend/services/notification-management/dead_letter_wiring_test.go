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
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// A letter written through the dead-letter sink this service builds lands on the platform
// dead-letter stream, under the tenant it was written for, stamped with THIS service as its
// source.
//
// 🔑 IT GOES THROUGH THE SERVICE'S OWN WIRING, NOT A COPY OF IT, in the three steps main.go
// takes: core/service (service.New) builds the producer, this service's own buildMetrics
// hands it to the package, and newDeadLetterSink builds the sink.
// What is read back is what the BROKER stored, so a sink over the wrong stream, or one
// built from a producer other than this service's, fails here as "nothing arrived" or as
// the wrong source rather than passing on the sink's own account of what it meant to write.
//
// The Microservice gets a registry of its own, so buildMetrics' collectors cannot collide
// with another test's in this package (or with a second run under -count).
//
// What stays untested is the one line in createNatsComponents that calls the helper and
// hands its result on — the same residue this package's reader helpers accept.
func TestADeadLetterLandsOnThePlatformStreamWithThisServiceAsSource(t *testing.T) {
	host, port := startEmbeddedNats(t)

	prevMs, prevSvc, prevDead := Microservice, Svc, DeadLetters
	t.Cleanup(func() { Microservice, Svc, DeadLetters = prevMs, prevSvc, prevDead })

	instance := fmt.Sprintf("dlwiring%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: instance, FunctionalArea: "notification-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	// The producer exactly as production gets it: core/service builds it for a service
	// with a broker, and buildMetrics hands it to the package.
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{
		OnCreate: func(*messaging.NatsManager) error { return nil },
	}})
	buildMetrics()
	require.Same(t, Svc.DeadLetters, DeadLetters, "buildMetrics did not hand on the producer core/service built")

	var sink *deadletter.Sink
	nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(m *messaging.NatsManager) error {
			var err error
			sink, err = newDeadLetterSink(m)
			return err
		})
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(DeadLetters))
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NotNil(t, sink)

	require.NoError(t, sink.Write(core.WithTenant(context.Background(), "acme"), deadletter.Envelope{
		Kind:       deadletter.KindNotification,
		Reason:     deadletter.ReasonExhausted,
		Summary:    "wiring test letter",
		OccurredAt: time.Now(),
	}))

	subject, letter := msgtest.OnlyDeadLetter(t, nmgr.Conn(), instance)
	require.Equal(t, messaging.ScopedSubject(instance, "acme", streams.DeadLetters), subject,
		"the letter was not stored on the tenant's dead-letter subject")
	require.Equal(t, "notification-management", letter.Source, "the letter does not name this service as its source")
	require.Equal(t, deadletter.KindNotification, letter.Kind)
	require.Equal(t, "wiring test letter", letter.Summary)
}
