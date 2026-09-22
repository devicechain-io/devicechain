// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/graphql"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/service"
)

// 🔴 WHAT THIS FILE EXISTS FOR, because it was written after the defect it would have
// caught shipped to main.
//
// When this service moved onto core/service, its stopper's three manager stops should have
// collapsed into one Svc.Stop. Only the GraphQL line was replaced; the NATS and Rdb stops
// below it were left behind. Svc.Stop leaves all three Stopped, and the lifecycle state
// machine permits a stop only from Initialized or Started — so the next line returned
// "cannot stop component ... while it is Stopped", every shutdown failed, Terminate was
// skipped (teardown deliberately does not attempt it after a failed Stop), and the process
// exited 1 on what was an orderly SIGTERM.
//
// 🔑 THE SIX OTHER CONVERTED SERVICES COULD NOT HAVE CAUGHT THIS, WHICH IS WHY THE FIX IS A
// NEW SHAPE OF TEST RATHER THAN A COPY OF THEIRS. Their graphql_shutdown_order_test.go
// fixtures leave the Rdb manager UNINITIALIZED on purpose, so that its refusal marks the end
// of the stopper and proves it ran to completion. That makes Svc.Stop fail, so those
// fixtures return at the Svc.Stop line and never reach anything after it. They pin the
// ORDER of the stops; they are structurally blind to an EXTRA one.
//
// So this fixture does the opposite: it gives Svc only the two managers a unit test can
// really stop, so Svc.Stop SUCCEEDS and the stopper runs past it. What comes after is then
// observable, which is the whole point.
//
// go build cannot see this either: a second X.Stop(ctx) is well-typed. The verdict is a
// runtime lifecycle state, so only something that RUNS the stopper can report it.

// startEmbeddedNats runs an in-process NATS server and returns its host and port.
func startEmbeddedNats(t *testing.T) (string, uint32) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), uint32(port)
}

// countingStopper returns callbacks that count how many times a component's stop actually
// ran its callbacks, so "stopped twice" is read as a NUMBER rather than inferred from an
// error string.
func countingStopper(n *int) core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Postprocess = func(context.Context) error {
		*n++
		return nil
	}
	return cb
}

// TestTheStopperStopsEachManagerExactlyOnce drives the real beforeMicroserviceStopped.
//
// Two assertions, and they fail for different reasons on purpose. The error says the
// stopper could not run to the end; the counts say no component was stopped more than
// once. A future change that made the extra stop SUCCEED rather than be refused would slip
// past the first and be caught by the second.
func TestTheStopperStopsEachManagerExactlyOnce(t *testing.T) {
	host, port := startEmbeddedNats(t)

	prevMs, prevGql, prevNats, prevSvc := Microservice, GraphQLManager, NatsManager, Svc
	prevIep, prevRac, prevCallout := InboundEventsProcessor, RaiseAlarmConsumer, CalloutResponder
	t.Cleanup(func() {
		Microservice, GraphQLManager, NatsManager, Svc = prevMs, prevGql, prevNats, prevSvc
		InboundEventsProcessor, RaiseAlarmConsumer, CalloutResponder = prevIep, prevRac, prevCallout
	})

	area := fmt.Sprintf("device-management-shutdown-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{
		InstanceId:     area,
		FunctionalArea: area,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}

	var natsStops, graphqlStops int

	NatsManager = messaging.NewNatsManager(Microservice, countingStopper(&natsStops),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(context.Background()))
	require.NoError(t, NatsManager.Start(context.Background()))

	// Initialized but not started: ExecuteStart binds the service's fixed GraphQL port,
	// which a unit test has no business taking, and a stop from Initialized is legal
	// precisely so a half-started service still tears itself down.
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, countingStopper(&graphqlStops),
		parsed, map[gqlcore.ContextKey]interface{}{}, Microservice.Readiness)
	require.NoError(t, GraphQLManager.Initialize(context.Background()))

	// 🔑 NO Rdb MANAGER, DELIBERATELY. Initializing one needs a live Postgres, and an
	// uninitialized one cannot be stopped — which would make Svc.Stop fail and hide
	// everything after it, exactly as it does in the six order fixtures. A Spec may
	// legitimately omit a manager, and core/service skips a nil rather than failing, so
	// this is an ordinary shape rather than a hole cut for the test.
	Svc = service.FromManagers(Microservice, service.Managers{
		Nats: NatsManager, GraphQL: GraphQLManager,
	})

	// Both are stopped unconditionally by the stopper, so they have to be there and be
	// stoppable. Their collaborators are nil: this test never delivers a message.
	InboundEventsProcessor = processor.NewInboundEventsProcessor(Microservice, nil, nil, nil,
		core.NewNoOpLifecycleCallbacks(), nil, "", 0, processor.NewResolveMetrics(Microservice))
	require.NoError(t, InboundEventsProcessor.Initialize(context.Background()))
	RaiseAlarmConsumer = processor.NewRaiseAlarmConsumer(Microservice, nil,
		core.NewNoOpLifecycleCallbacks(), nil, nil, processor.NewRaiseAlarmMetrics(Microservice))
	require.NoError(t, RaiseAlarmConsumer.Initialize(context.Background()))

	// The callout responder is nil-guarded in the stopper; there is nothing to stop.
	CalloutResponder = nil

	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	err := beforeMicroserviceStopped(context.Background())

	require.NoError(t, err,
		"beforeMicroserviceStopped did not run to the end. A lifecycle refusal here means it "+
			"stopped something that core/service had already stopped — which in production fails "+
			"Stop, skips Terminate entirely, and exits the process 1 on an orderly SIGTERM")
	require.Equal(t, 1, natsStops, "the broker manager was stopped a number of times that is not once")
	require.Equal(t, 1, graphqlStops, "the GraphQL manager was stopped a number of times that is not once")
}
