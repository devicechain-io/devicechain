// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/graphql"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

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

// shutdownProbe is how a test observes an ordering that the two managers involved
// report nothing about. Neither GraphQLManager nor NatsManager knows the other exists,
// so there is no state after the fact that says which stopped first — but both are
// built with LifecycleCallbacks, and those run inside the stop they belong to. Hanging
// the observations there turns "which ran first" into recorded data.
//
// Every field is written from inside beforeMicroserviceStopped, on that one goroutine,
// which is why none of this is synchronized.
type shutdownProbe struct {
	seq int

	graphqlStoppedAt int
	natsStoppedAt    int

	// publishErr and roundTrip are a REAL publish and a real round trip through the
	// broker, performed at the instant the GraphQL server has finished draining. They
	// stand in for the last in-flight request of a rolling restart: whatever it does
	// with the broker, it does it at that moment or earlier.
	publishErr error
	roundTrip  error

	// natsConnClosed records that the NATS stop really closed the connection, so a
	// clean run cannot be a run in which the NATS manager was never stopped at all.
	natsConnClosed bool
}

// observeGraphQLStop returns callbacks that record the GraphQL stop and, once it has
// finished, try to use the broker.
func (p *shutdownProbe) observeGraphQLStop(natsMgr func() *messaging.NatsManager, subject string) core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Postprocess = func(context.Context) error {
		p.seq++
		p.graphqlStoppedAt = p.seq

		mgr := natsMgr()
		if mgr == nil || mgr.Conn() == nil {
			p.publishErr = errors.New("the NATS manager had no connection")
			return nil
		}
		nc := mgr.Conn()
		sub, err := nc.SubscribeSync(subject)
		if err != nil {
			p.publishErr = err
			return nil
		}
		if err := nc.Publish(subject, []byte("in-flight")); err != nil {
			p.publishErr = err
			return nil
		}
		if err := nc.Flush(); err != nil {
			p.publishErr = err
			return nil
		}
		// Bounded: a message that never arrives must fail the test rather than hang it.
		if _, err := sub.NextMsg(10 * time.Second); err != nil {
			p.roundTrip = err
		}
		return nil
	}
	return cb
}

// observeNatsStop returns callbacks that record the NATS stop and then WAIT for the
// connection to actually close.
//
// 🔴 THE WAIT IS NOT WHAT MAKES A REORDERED STOPPER FAIL, and it is worth being exact
// about which mechanism does what, because the two are easy to swap and only one of them
// is load-bearing for the failure.
//
// What makes a reordered stopper fail is the DRAIN STATE, with no wait involved.
// NatsManager.ExecuteStop calls Conn.Drain, which sets DRAINING_SUBS synchronously under
// the connection lock BEFORE it returns and then drains in a goroutine; the connection
// never leaves a drain state afterwards. nats.go refuses a SUBSCRIBE in either drain
// state (subscribe checks isDraining) but a PUBLISH only in DRAINING_PUBS (publish checks
// isDrainingPubs). So every probe below is refused the moment the NATS stop has returned,
// and with the wait removed the errors are visibly the two different mechanisms: a
// subscribe reports "nats: connection draining", a publish "nats: connection closed" —
// the latter because a connection carrying no subscriptions finishes draining in
// microseconds, well inside the lifecycle transition that separates the two stops.
//
// What the wait is FOR is natsConnClosed, which is a required-presence check: without it
// a run in which the NATS manager was never really stopped would satisfy every assertion
// below by doing nothing. It also removes the publish path's dependence on how far the
// drain has progressed, which is a real but secondary benefit — a race that reliably wins
// is still a race.
//
// 🔑 THE CONSEQUENCE FOR WHOEVER EDITS THE PROBE: a probe reduced to a publish alone
// would rest entirely on that secondary benefit, and this comment would no longer explain
// why it passes. Keep an operation a drain refuses OUTRIGHT — a subscribe — in the probe.
func (p *shutdownProbe) observeNatsStop(mgr func() *messaging.NatsManager) core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Preprocess = func(context.Context) error {
		p.seq++
		p.natsStoppedAt = p.seq
		return nil
	}
	cb.Stopper.Postprocess = func(context.Context) error {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			nc := mgr().Conn()
			if nc == nil || nc.IsClosed() {
				p.natsConnClosed = true
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	}
	return cb
}

// assertGraphQLStoppedFirst is the shared verdict.
func (p *shutdownProbe) assertGraphQLStoppedFirst(t *testing.T) {
	t.Helper()
	require.NotZero(t, p.graphqlStoppedAt, "the GraphQL manager was never stopped at all")
	require.NotZero(t, p.natsStoppedAt, "the NATS manager was never stopped at all")
	require.True(t, p.natsConnClosed,
		"the NATS connection was still open after the NATS manager's stop returned, so this run "+
			"proves nothing about the order of two stops")

	// The consequence first, the mechanism second. A reordered stopper fails both, and
	// the one worth reading is what a request would have got.
	require.NoError(t, p.publishErr,
		"at the moment the GraphQL server finished draining, the broker connection could no longer "+
			"be published on: the NATS manager was stopped first, so the last request of a rolling "+
			"restart fails on a connection that is already going away")
	require.NoError(t, p.roundTrip,
		"at the moment the GraphQL server finished draining, a publish was accepted but never came "+
			"back from the broker: the connection was already going away")
	require.Less(t, p.graphqlStoppedAt, p.natsStoppedAt,
		"beforeMicroserviceStopped stopped the NATS manager BEFORE the GraphQL server")
}

// idleReader is a messaging.MessageReader that is never read.
//
// The dead-letter write-back REFUSES to be built without a reader — a consumer with no
// reader reads every letter and writes nothing — and this fixture only ever initializes
// and stops it, so what the reader would return does not arise. Returning io.EOF rather
// than blocking keeps that true even if something does read it: a loop ends instead of
// hanging, and a hang is neither a pass nor a fail.
type idleReader struct{}

func (idleReader) ReadMessage(context.Context) (messaging.Message, error) {
	return messaging.Message{}, io.EOF
}

func (idleReader) HandleResponse(error) {}

// startShutdownFixture brings the globals beforeMicroserviceStopped touches into a state
// it can run against, and returns the probe watching the two stops that matter.
//
// The RdbManager is built but deliberately NEVER initialized: doing so needs a live
// Postgres. Its stop is therefore refused by the lifecycle state machine, and that
// refusal is the fixture's end marker — see the assertion in the test.
func startShutdownFixture(t *testing.T) (*shutdownProbe, string) {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevGql, prevNats, prevRdb := Microservice, GraphQLManager, NatsManager, RdbManager
	prevProc, prevWriteback, prevApi := CommandDeliveryProcessor, DeadLetterWriteback, Api
	t.Cleanup(func() {
		Microservice, GraphQLManager, NatsManager, RdbManager = prevMs, prevGql, prevNats, prevRdb
		CommandDeliveryProcessor, DeadLetterWriteback, Api = prevProc, prevWriteback, prevApi
	})

	area := fmt.Sprintf("command-delivery-shutdown-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{
		InstanceId:     area,
		FunctionalArea: area,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}

	probe := &shutdownProbe{}
	subject := area + ".in-flight"

	NatsManager = messaging.NewNatsManager(Microservice, probe.observeNatsStop(func() *messaging.NatsManager { return NatsManager }),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(context.Background()))
	require.NoError(t, NatsManager.Start(context.Background()))
	require.NotNil(t, NatsManager.Conn())
	require.False(t, NatsManager.Conn().IsClosed(), "connection should be live before shutdown")

	// Initialized but not started: ExecuteStart binds the service's fixed GraphQL port,
	// which a unit test has no business taking, and ExecuteStop is what this measures.
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice,
		probe.observeGraphQLStop(func() *messaging.NatsManager { return NatsManager }, subject),
		parsed, map[gqlcore.ContextKey]interface{}{}, Microservice.Readiness)
	require.NoError(t, GraphQLManager.Initialize(context.Background()))

	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil,
		Microservice.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})
	Api = model.NewApi(RdbManager)

	// Both of these are stopped unconditionally by the stopper, so they have to be there
	// and they have to be stoppable. Initialized-not-started is enough: this test is
	// about what happens AFTER they stop, and a stop from Initialized is legal precisely
	// so a half-started service still tears itself down.
	CommandDeliveryProcessor = processor.NewCommandDeliveryProcessor(Microservice, nil, nil,
		core.NewNoOpLifecycleCallbacks(), Api, nil, nil, nil, processor.NewDeliveryMetrics(Microservice))
	require.NoError(t, CommandDeliveryProcessor.Initialize(context.Background()))

	writeback, err := processor.NewDeadLetterWriteback(Microservice, idleReader{}, Api,
		core.NewNoOpLifecycleCallbacks(), processor.NewWritebackMetrics(Microservice))
	require.NoError(t, err)
	DeadLetterWriteback = writeback
	require.NoError(t, DeadLetterWriteback.Initialize(context.Background()))

	// Leave nothing behind for the rest of the package: a failed require unwinds through
	// Cleanup without ever closing this connection, and the client reconnects forever.
	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return probe, fmt.Sprintf("%s-rdb", area)
}

// The GraphQL server is stopped BEFORE the NATS manager, and that order is a contract
// rather than an accident of how the file reads.
//
// The hazard it exists for is one that leaves no trace: while the HTTP server is still
// accepting, a request already inside a resolver can still reach the broker, and the
// service's own connection is the one it reaches it on. Stopping NATS first therefore
// hands the last request of every rolling restart a connection that is on its way out —
// a failure the caller sees, on a request the platform accepted, at the one moment
// nobody is watching a single replica.
//
// Nothing in this service's GraphQL plane publishes on the caller's own goroutine
// today — CreateCommand's dispatch nudge hands the device to the processor's bounded
// queue, and the processor is stopped above — so the probe below stands in for the
// in-flight request rather than being one. That is the point of pinning it: the enqueue
// path is the one that grows a publish, and this test is what makes the order it needs
// survive the change that adds one.
//
// core/core/http.go records that this order is a per-service decision and that
// lwm2m-ingest reached the opposite answer for a real reason (its shutdown RELEASES a
// leadership lease over the connection, so its NATS stop must come last). This service
// has no such reason: everything of its own that touches the broker is stopped above,
// and the two lines under test are adjacent.
func TestGraphQLServerStopsBeforeTheNatsConnection(t *testing.T) {
	probe, rdbName := startShutdownFixture(t)

	err := beforeMicroserviceStopped(context.Background())

	probe.assertGraphQLStoppedFirst(t)

	// Required presence: the RdbManager is the last thing the stopper touches and this
	// fixture cannot initialize it, so its refusal is what proves the stopper ran to the
	// END rather than returning early somewhere in the middle with the ordering
	// assertions above vacuously satisfied.
	require.ErrorContains(t, err, rdbName,
		"beforeMicroserviceStopped did not reach the RdbManager, so it returned early and the "+
			"order recorded above is not the order a real shutdown produces")
}
