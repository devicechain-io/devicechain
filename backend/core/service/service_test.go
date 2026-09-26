// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

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
		StoreDir:  dctest.JetStreamStoreDir(t),
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

// 🔑 WHAT IS TESTED WHERE, because this file deliberately does not test the main thing.
//
// The ORDER is what this package exists for, and it is pinned by the adopting services'
// graphql_shutdown_order_test.go: those fixtures wrap their three real managers with
// FromManagers and drive beforeMicroserviceStopped, so they exercise this package's
// sequence rather than a copy. Reversing the walk in reverse() fails them.
//
// It is not re-tested here because Managers holds the three CONCRETE manager types, so
// there is no seam to inject ordered fakes through — and widening those fields to
// interfaces to create one would cost every caller the concrete methods it needs
// (NatsManager.NewWriter and friends). The seam not existing is a deliberate trade, so
// the test for the order lives where real managers already do.
//
// What this file covers is everything the service-level fixtures cannot see: a Spec that
// asks for fewer than three managers, and what an error on the way down actually says.

func testMicroservice(t *testing.T) *core.Microservice {
	t.Helper()
	return &core.Microservice{
		InstanceId:     "svc-test",
		FunctionalArea: "svc-test",
		Readiness:      core.NewReadinessGate(),
	}
}

// TestAServiceWithNoManagersIsDrivable is the degenerate case, and it is worth pinning
// because the walk is written over a slice that may legitimately be empty.
//
// Two services in the tree hold no relational database and two hold no broker, so "fewer
// than three" is an ordinary shape rather than a misconfiguration. A walk that treated an
// absent manager as an error — or that indexed into the sequence assuming three — would
// take those services down at the first phase.
func TestAServiceWithNoManagersIsDrivable(t *testing.T) {
	ephemeralProbes(t)
	svc := New(testMicroservice(t), Spec{})
	ctx := context.Background()

	require.NoError(t, svc.Initialize(ctx), "initialize with an empty Spec")
	require.NoError(t, svc.Start(ctx), "start with no managers")
	require.NoError(t, svc.Stop(ctx), "stop with no managers")
	require.NoError(t, svc.Terminate(ctx), "terminate with no managers")

	require.Nil(t, svc.Rdb)
	require.Nil(t, svc.Nats)
	require.Nil(t, svc.GraphQL)
}

// TestAFailedStepSaysWhichManagerFailed is about the message, not the failure.
//
// 🔴 THE POINT IS THAT THE ERROR NAMES THE MANAGER. Now that four ordered sequences live
// in one place, a bare error surfacing from a walk says only that "a manager" would not
// stop — and the whole reason a service reads that line is to find out WHICH. The
// lifecycle refusal underneath does carry a name, but it is the component's own
// (area-rdb), which is a string this package composed elsewhere rather than one a reader
// of this error would recognize.
//
// The refusal is induced honestly: an rdb manager that was never initialized cannot be
// stopped, which is the same state a real service reaches when its startup died early.
func TestAFailedStepSaysWhichManagerFailed(t *testing.T) {
	ms := testMicroservice(t)
	uninitialized := rdb.NewRdbManager(ms, core.NewNoOpLifecycleCallbacks(), nil,
		ms.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})

	svc := FromManagers(ms, Managers{Rdb: uninitialized})

	err := svc.Stop(context.Background())
	require.Error(t, err, "stopping a manager that was never initialized must fail")
	require.ErrorContains(t, err, "the relational database manager",
		"the error does not say which manager would not stop, which is the one thing a "+
			"reader of it needs now that the sequence lives in one place")
	require.ErrorContains(t, err, "Uninitialized",
		"the wrapping swallowed the lifecycle refusal underneath, so the reason is gone")
}

// TestFromManagersNeedsNoSpec pins the contract FromManagers advertises: it wraps managers
// somebody else built, so Initialize has nothing to do and must not claim otherwise.
//
// Without this, a future Initialize that started assuming a Spec would fail every fixture
// that uses FromManagers — and it would fail them at setup, where the cause reads as a
// broken test rather than a broken contract.
func TestFromManagersNeedsNoSpec(t *testing.T) {
	ms := testMicroservice(t)
	built := rdb.NewRdbManager(ms, core.NewNoOpLifecycleCallbacks(), nil,
		ms.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})

	svc := FromManagers(ms, Managers{Rdb: built})

	require.NoError(t, svc.Initialize(context.Background()),
		"Initialize on a FromManagers service must be a no-op, not an attempt to build")
	require.Same(t, built, svc.Rdb, "Initialize replaced a manager it was not given a Spec for")

	// 🔴 NOR A PROBE SURFACE, which is the half a zero Spec would otherwise get. The
	// managers handed over may already have registered /healthz on this mux, and a second
	// registration panics — so FromManagers and Spec{} must not be the same thing.
	require.Nil(t, svc.probes, "a FromManagers service built a probe server nobody asked for")
	require.Empty(t, routeFor(ms, "/healthz"),
		"a FromManagers service registered probes on a mux it was not given a Spec for")
}

// TestTheRdbSpecChoosesWhichInstanceStoreIsOpened is here because the obvious
// simplification is wrong, and wrong in a way that would not show up until a deployment.
//
// 🔴 THE SPEC NAMES THE INSTANCE DATASTORE; THIS PACKAGE MUST NOT PICK ONE. An earlier
// version read InstanceConfiguration.Persistence.Rdb itself, on the reading that every
// service opens the relational store. event-management does not: its manager opens
// Persistence.TSDB, the instance's event store, which is a different cluster. Defaulting
// would have created its schema in the relational database and left the hypertables it
// depends on being absent from the store it actually queries.
//
// The microservice below carries a DIFFERENT relational store from the one the Spec asks
// for, so this fails if the field is ignored rather than passing for free on a zero value.
// The refusal is induced honestly — an unsupported type is rejected before any connection
// is attempted — and it is read twice: through the manager the walk kept, and through the
// error, which names the type it was given.
func TestTheRdbSpecChoosesWhichInstanceStoreIsOpened(t *testing.T) {
	ms := testMicroservice(t)
	ms.InstanceConfiguration.Persistence.Rdb = mscfg.DatastoreConfiguration{Type: "the-relational-store"}

	svc := New(ms, Spec{Rdb: &RdbSpec{
		Instance: mscfg.DatastoreConfiguration{Type: "the-store-the-service-asked-for"},
	}})

	err := svc.Initialize(context.Background())
	require.Error(t, err, "an unsupported datastore type must be refused, not connected to")
	require.ErrorContains(t, err, "the-store-the-service-asked-for",
		"the manager was opened against a store the Spec did not name")
	require.NotContains(t, err.Error(), "the-relational-store",
		"the Spec's datastore was ignored in favour of the instance's relational store, which "+
			"is the wrong cluster for any service whose tables are hypertables")

	require.NotNil(t, svc.Rdb, "the manager is published even when its initialize fails")
	require.Equal(t, "the-store-the-service-asked-for", svc.Rdb.InstanceConfig.Type)
}

// TestTheHooksRunInTheGapsBetweenConstructions is the contract the two hooks exist for,
// and the reason it is pinned HERE is that nothing else would catch it breaking.
//
// 🔴 A SERVICE'S main.go IS NOT EXERCISED BY ANY TEST. If Initialize ran AfterNats before
// it built the broker manager, device-management would hand a nil manager to
// model.InitializeCaches and user-management would ask a nil manager for a KV bucket —
// both panics, both at startup, and both invisible until something actually starts the
// process. The assertions below are what stands in for that.
//
// Each hook is asked what it can SEE rather than merely recorded, because the ordering is
// only worth anything if the manager is there by the time the hook that needs it runs.
// A sequence counter alone would pass for a pair of hooks called back to back at the end.
func TestTheHooksRunInTheGapsBetweenConstructions(t *testing.T) {
	host, port := startEmbeddedNats(t)

	ms := testMicroservice(t)
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	var seq []string
	var rdbSawNats, natsSawNats bool

	svc := New(ms, Spec{
		AfterRdb: func(_ context.Context, m *Managers) error {
			seq = append(seq, "afterRdb")
			rdbSawNats = m.Nats != nil
			return nil
		},
		Nats: &NatsSpec{OnCreate: func(*messaging.NatsManager) error { return nil }},
		AfterNats: func(_ context.Context, m *Managers) error {
			seq = append(seq, "afterNats")
			natsSawNats = m.Nats != nil
			return nil
		},
	})

	require.NoError(t, svc.Initialize(context.Background()))
	t.Cleanup(func() {
		if c := svc.Nats.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	require.Equal(t, []string{"afterRdb", "afterNats"}, seq,
		"the hooks did not both run, or ran in the wrong order")
	require.False(t, rdbSawNats,
		"AfterRdb was handed a broker manager, so it no longer runs in the gap BEFORE the "+
			"broker is built and a service cannot rely on ordering its own work against it")
	require.True(t, natsSawNats,
		"AfterNats was handed no broker manager: device-management's JetStream KV caches and "+
			"user-management's identity manager are built here and would take a nil")
}

// freePort asks the operating system for a port nothing is listening on.
//
// The GraphQL manager's own ephemeral-port sentinel is unexported on purpose, so a test
// outside that package cannot name it. Binding a real free port is the honest way to drive
// ExecuteStart here: if the port is taken between the close and the bind, the start fails
// loudly rather than the test passing over a server that never came up.
func freePort(t *testing.T) int32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return int32(port)
}

// recordingPhases returns callbacks that append to a shared log, so the ORDER of a walk is
// recorded data rather than something inferred afterwards from the managers' states.
func recordingPhases(log *[]string, name string) core.LifecycleCallbacks {
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Starter.Postprocess = func(context.Context) error { *log = append(*log, "start "+name); return nil }
	cb.Stopper.Postprocess = func(context.Context) error { *log = append(*log, "stop "+name); return nil }
	cb.Terminator.Postprocess = func(context.Context) error { *log = append(*log, "terminate "+name); return nil }
	return cb
}

// TestTheWalkGoesForwardToComeUpAndBackwardToGoDown is the package's main purpose, and
// until it existed only ONE of the three walks was covered anywhere in the tree.
//
// 🔴 WHAT THE ADOPTERS' FIXTURES DO AND DO NOT REACH. Six services have a
// graphql_shutdown_order_test.go driving beforeMicroserviceStopped, which is why the file
// header above says the order is pinned there. That is true of STOP only. None of them
// calls Svc.Start or Svc.Terminate, so `forward` had no test at all, and a Terminate wired
// to the forward helper instead of the reverse one would have been caught by nothing.
//
// Two real managers is enough to observe direction, which is what makes this testable here
// despite Managers holding concrete types with no seam for fakes: the broker manager runs
// against an embedded server, and the GraphQL manager binds a port the OS just handed us
// rather than the service's fixed one.
func TestTheWalkGoesForwardToComeUpAndBackwardToGoDown(t *testing.T) {
	host, port := startEmbeddedNats(t)

	ms := testMicroservice(t)
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	var log []string

	nats := messaging.NewNatsManager(ms, recordingPhases(&log, "nats"),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nats.Initialize(context.Background()))

	gql := gqlcore.NewGraphQLManager(ms, recordingPhases(&log, "graphql"),
		gqlcore.MustParseSchema(probeSchema, &probeResolver{}),
		map[gqlcore.ContextKey]interface{}{}, ms.Readiness)
	gql.Port = freePort(t)
	require.NoError(t, gql.Initialize(context.Background()))

	svc := FromManagers(ms, Managers{Nats: nats, GraphQL: gql})
	t.Cleanup(func() {
		if c := nats.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	require.NoError(t, svc.Start(context.Background()))
	require.NoError(t, svc.Stop(context.Background()))
	require.NoError(t, svc.Terminate(context.Background()))

	require.Equal(t, []string{
		"start nats", "start graphql",
		"stop graphql", "stop nats",
		"terminate graphql", "terminate nats",
	}, log,
		"the broker must come up before the GraphQL server and go down after it. Starting the "+
			"HTTP server first serves traffic through resolvers whose broker wiring the oncreate "+
			"callback has not built yet; stopping it last leaves an in-flight request publishing "+
			"on a connection that is already draining")
}

// probeSchema is the smallest schema graphql-go will accept, so that this package can build
// a real GraphQL manager without depending on any service's SDL.
const probeSchema = `schema { query: Query }
type Query { ping: String! }`

type probeResolver struct{}

func (*probeResolver) Ping() string { return "pong" }

// TestAFailedRelationalInitializeDoesNotRunTheHook pins the half of the hook contract that
// TestTheHooksRunInTheGapsBetweenConstructions structurally cannot reach.
//
// 🔴 THAT TEST DECLARES NO Rdb, SO IT ONLY OBSERVES THE SECOND GAP. Moving the AfterRdb
// call ABOVE the relational construction passes it — and every adopter does
// `RdbManager = m.Rdb` as the first line of that hook, so all seven would silently take a
// nil, build an Api around it, initialize cleanly and nil-deref on the first query.
//
// The gap cannot be observed directly here, because AfterRdb runs only when the manager's
// initialize SUCCEEDED and that needs a live Postgres. What can be observed is the
// consequence that follows from the same ordering: a hook placed before the construction
// also runs when the construction is about to fail. So this asks for a store that cannot be
// opened and asserts the hook stayed out of it.
func TestAFailedRelationalInitializeDoesNotRunTheHook(t *testing.T) {
	ran := false
	svc := New(testMicroservice(t), Spec{
		Rdb: &RdbSpec{Instance: mscfg.DatastoreConfiguration{Type: "a-store-that-cannot-be-opened"}},
		AfterRdb: func(_ context.Context, m *Managers) error {
			ran = true
			return nil
		},
	})

	err := svc.Initialize(context.Background())

	require.Error(t, err, "an unsupported datastore type must be refused")
	require.False(t, ran,
		"AfterRdb ran although the relational manager never initialized, which means it no "+
			"longer runs AFTER that construction — every adopter reads m.Rdb as its first line "+
			"and would take a nil")
}
