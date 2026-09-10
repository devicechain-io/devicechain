// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-processing/graphql"
	"github.com/devicechain-io/dc-event-processing/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
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

	// detectStoppedAt is where the DETECT processor's stop landed in the sequence. It
	// is the lease question: see TestDetectStopsBeforeEitherManager.
	detectStoppedAt int

	// inFlight is the service's OWN broker path, run at the same instant. See
	// TestGraphQLServerStopsBeforeTheNatsConnection.
	inFlight    func() error
	inFlightErr error
}

// observeDetectStop returns callbacks that record where the DETECT processor's stop fell
// in the sequence.
func (p *shutdownProbe) observeDetectStop() core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Postprocess = func(context.Context) error {
		p.seq++
		p.detectStoppedAt = p.seq
		return nil
	}
	return cb
}

// observeGraphQLStop returns callbacks that record the GraphQL stop and, once it has
// finished, try to use the broker.
func (p *shutdownProbe) observeGraphQLStop(natsMgr func() *messaging.NatsManager, subject string) core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Postprocess = func(context.Context) error {
		p.seq++
		p.graphqlStoppedAt = p.seq

		// The service's own broker path first: it is the thing the ordering exists to
		// protect, and it must run even if the generic probe below cannot get started.
		if p.inFlight != nil {
			p.inFlightErr = p.inFlight()
		}

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
	require.NoError(t, p.inFlightErr,
		"a subscriber whose socket was still open when shutdown began could not attach to the "+
			"tenant's live detection feed: the NATS manager was stopped before the HTTP server "+
			"drained, so the stream the detections subscription reads was already gone")
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

// startShutdownFixture brings the globals beforeMicroserviceStopped touches into a state
// it can run against, and returns the probe watching the stops that matter.
//
// The RdbManager is built but deliberately NEVER initialized: doing so needs a live
// Postgres. Its stop is therefore refused by the lifecycle state machine, and that
// refusal is the fixture's end marker — see the assertion in the test.
func startShutdownFixture(t *testing.T) (*shutdownProbe, string) {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevGql, prevNats, prevRdb := Microservice, GraphQLManager, NatsManager, RdbManager
	prevDetect, prevReact, prevPurge := ResolvedEventsProcessor, ReactDispatcher, TenantPurgeResponder
	t.Cleanup(func() {
		Microservice, GraphQLManager, NatsManager, RdbManager = prevMs, prevGql, prevNats, prevRdb
		ResolvedEventsProcessor, ReactDispatcher, TenantPurgeResponder = prevDetect, prevReact, prevPurge
	})

	area := fmt.Sprintf("event-processing-shutdown-%d", time.Now().UnixNano())
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

	// The exact call the detections subscription resolver makes, on the real connection.
	probe.inFlight = func() error {
		_, err := NatsManager.SubscribeLive(context.Background(), "acme", streams.DerivedEvents)
		return err
	}

	// Initialized but not started: ExecuteStart binds the service's fixed GraphQL port,
	// which a unit test has no business taking, and ExecuteStop is what this measures.
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice,
		probe.observeGraphQLStop(func() *messaging.NatsManager { return NatsManager }, subject),
		parsed, map[gqlcore.ContextKey]interface{}{}, Microservice.Readiness)
	require.NoError(t, GraphQLManager.Initialize(context.Background()))

	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil,
		Microservice.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})

	// The DETECT processor is stopped unconditionally by the stopper, so it has to be
	// there and it has to be stoppable. Initialized-not-started is enough: this test is
	// about WHERE its stop falls, and a stop from Initialized is legal precisely so a
	// half-started service still tears itself down.
	ResolvedEventsProcessor = processor.NewResolvedEventsProcessor(Microservice, nil, nil, nil, nil,
		nil, nil, processor.Config{}, probe.observeDetectStop(), processor.NewDetectMetrics(Microservice))
	require.NoError(t, ResolvedEventsProcessor.Initialize(context.Background()))

	// Both of these are nil-guarded in the stopper; there is nothing to stop.
	ReactDispatcher, TenantPurgeResponder = nil, nil

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
// For this service the request that is still inside a resolver is most often a
// SUBSCRIPTION: the detections subscription reads the tenant's derived-event stream over
// this connection for as long as its socket is open, and it attaches to that stream by
// calling SubscribeLive. So the probe makes that exact call at the instant the HTTP
// server finishes draining.
//
// core/core/http.go records that this order is a per-service decision and that
// lwm2m-ingest reached the opposite answer for a real reason: its shutdown RELEASES a
// leadership lease over the connection, so its NATS stop must come LAST. This service
// holds a lease too — the DETECT partition lease — and the reason the same argument does
// not apply here is pinned separately, by TestDetectStopsBeforeEitherManager.
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

// ResolvedEventsProcessor.Stop RETURNS before either manager stops, which is why the
// ordering above is a free choice rather than the lwm2m-ingest trap in disguise.
//
// The hazard it is standing between: lwm2m-ingest stops its NATS manager LAST because
// its leadership unwind ends in a KV write that releases the lease, and a release on a
// draining connection fails — leaving a standby to wait out a full lease TTL. This
// service holds a lease of the same kind, and its release is preceded by a final
// checkpoint that must also land, so the same hazard would apply if the DETECT teardown
// ran anywhere near the two stops the test above reorders.
//
// 🔴 BE EXACT ABOUT WHAT THIS PROVES AND WHAT IT ASSUMES, because the gap is where a
// future change gets through. What is MEASURED is stop ORDER and nothing more: the
// DETECT processor's stop returned before both manager stops. That the final checkpoint
// and the lease release happen INSIDE that stop is read from the code, not observed
// here — ExecuteStop cancels the supervisor and joins supWG, and the goroutine in that
// group runs runTerms, whose exit path is endTerm (flush the checkpoint, leave the term
// gate, release the lease).
//
// And the assumption has a name: this fixture leaves Lease and Gate nil, so
// leadershipEnabled is false and ExecuteStop takes the OTHER branch — pcancel then
// readerWG.Wait. Nothing here drives endTerm at all. So a change that moved the release
// onto a detached goroutine, or into Terminate, would keep this test green while
// reintroducing exactly the lwm2m-ingest failure. Covering that needs a test that holds
// a real lease through a real term, which this is not.
//
// It is still worth having as it is: the thing most likely to break the reasoning above
// is someone moving the DETECT stop down the function, and that is precisely what this
// catches.
func TestDetectStopsBeforeEitherManager(t *testing.T) {
	probe, _ := startShutdownFixture(t)

	_ = beforeMicroserviceStopped(context.Background())

	require.NotZero(t, probe.detectStoppedAt, "the DETECT processor was never stopped at all")
	require.NotZero(t, probe.graphqlStoppedAt, "the GraphQL manager was never stopped at all")
	require.NotZero(t, probe.natsStoppedAt, "the NATS manager was never stopped at all")
	require.Less(t, probe.detectStoppedAt, probe.natsStoppedAt,
		"the DETECT processor is stopped after the NATS manager, so its final checkpoint and its "+
			"partition-lease release run against a connection that is already draining: a failed "+
			"release leaves a standby waiting out the whole lease TTL before it can take over")
	require.Less(t, probe.detectStoppedAt, probe.graphqlStoppedAt,
		"the DETECT processor is stopped after the GraphQL server, so the ordering the test above "+
			"pins is no longer upstream of the DETECT teardown and no longer free to choose")
}
