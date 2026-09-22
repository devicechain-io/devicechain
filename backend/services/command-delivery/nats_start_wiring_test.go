// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// 🔴 WHAT THIS FILE EXISTS FOR: Api.Nudger is written during NatsManager.START, and the
// GraphQL server used to start BEFORE it.
//
// afterMicroserviceStarted started the managers Rdb → GraphQL → NATS. createNatsComponents
// is NatsManager.Start's create callback, and it is where Api.Nudger is bound — so between
// GraphQLManager.Start and NatsManager.Start this service was serving HTTP with a nil
// nudger. A createCommand mutation landing there takes the nil branch in model/nudge.go:
// the nudge is skipped and the command waits for the next sweep instead of dispatching
// promptly, which is the latency #917/#919 exist to remove.
//
// That window was REACHABLE, and the two facts that make it so are easy to miss
// separately:
//
//   - StartInstanceAuthGate runs from afterMicroserviceInitialized and opens the readiness
//     gate from a BACKGROUND goroutine, so /readyz can answer 200 part-way through the
//     start callback.
//   - /readyz is served by the GraphQL manager's own HTTP server (core/graphql registers
//     the probes, and the server is built in its ExecuteStart). So the window opens at
//     exactly the moment traffic can first arrive, and closes when NatsManager.Start
//     returns — a broker dial plus JetStream stream and consumer creation, with retries
//     on a cold broker. Seconds, not microseconds.
//
// The order is fixed now. This test pins the DEPENDENCY that makes the order load-bearing
// rather than the order itself: that Api.Nudger does not exist until the NATS manager has
// started. A future change that moves the binding earlier makes the ordering constraint
// moot and should make this test fail so the reasoning gets revisited — and one that moves
// it LATER, out of the create callback, must fail here rather than silently reopening the
// gap in some other shape.
//
// 🔴 THE ASSERTION BEFORE Start IS THE LOAD-BEARING HALF. "Non-nil after Start" alone
// passes for a binding that happened at construction, at Initialize, or anywhere else —
// including the afterMicroserviceInitialized placement that createNatsComponents' own
// comment warns would bind a nil. Only the nil-before/non-nil-after pair says the write
// happened INSIDE the start.

// startNatsWiringFixture brings the globals createNatsComponents touches into a state it
// can run against, pointed at an embedded broker, and returns the manager unstarted.
//
// The RdbManager is built but never initialized — that needs a live Postgres — which is
// fine here because model.NewApi only stores it, and nothing on this path reads through
// it. startEmbeddedNats lives next door in graphql_shutdown_order_test.go.
func startNatsWiringFixture(t *testing.T) *messaging.NatsManager {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevRdb, prevApi := Microservice, Configuration, NatsManager, RdbManager, Api
	prevProc, prevWriteback := CommandDeliveryProcessor, DeadLetterWriteback
	prevResponses, prevCommands := CommandResponsesReader, DeviceCommandsWriter
	prevDelivery, prevWritebackMetrics := DeliveryMetrics, WritebackMetrics
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, RdbManager, Api = prevMs, prevCfg, prevNats, prevRdb, prevApi
		CommandDeliveryProcessor, DeadLetterWriteback = prevProc, prevWriteback
		CommandResponsesReader, DeviceCommandsWriter = prevResponses, prevCommands
		DeliveryMetrics, WritebackMetrics = prevDelivery, prevWritebackMetrics
	})

	// A per-run functional area keeps the Prometheus collectors this builds from
	// colliding with another test's: metric names are derived from it, and a duplicate is
	// a MustRegister panic.
	area := fmt.Sprintf("command-delivery-natswiring-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{
		InstanceId:     area,
		FunctionalArea: area,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}
	Configuration = &config.CommandDeliveryConfiguration{
		SweepIntervalSeconds: config.DefaultSweepIntervalSeconds,
	}
	buildMetrics()

	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil,
		Microservice.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})
	Api = model.NewApi(RdbManager)

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return mgr
}

// TestTheNudgerIsBoundDuringTheNatsStart is why the GraphQL server must start after the
// NATS manager and not before it.
func TestTheNudgerIsBoundDuringTheNatsStart(t *testing.T) {
	mgr := startNatsWiringFixture(t)

	require.NoError(t, mgr.Initialize(context.Background()))
	require.Nil(t, Api.Nudger,
		"Api.Nudger was already bound before the NATS manager started. If the binding has moved "+
			"out of createNatsComponents, the ordering constraint in afterMicroserviceStarted — and "+
			"the comment explaining it — no longer describe why the order is what it is.")

	require.NoError(t, mgr.Start(context.Background()))
	require.NotNil(t, Api.Nudger,
		"the NATS manager started but Api.Nudger is still nil, so a command created now would take "+
			"the nil-nudger branch and wait for the sweep rather than dispatching")
}
