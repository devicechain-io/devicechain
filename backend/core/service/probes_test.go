// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// ephemeralProbes points the probe server at a free port for one test, so no test in this
// package binds the chart's port and races whatever else holds it.
func ephemeralProbes(t *testing.T) {
	t.Helper()
	prev := ProbesPort
	ProbesPort = 0
	t.Cleanup(func() { ProbesPort = prev })
}

// routeFor reports the pattern the microservice's mux would route path to, or "" if none.
func routeFor(ms *core.Microservice, path string) string {
	_, pattern := ms.Mux().Handler(httptest.NewRequest(http.MethodGet, path, nil))
	return pattern
}

func get(t *testing.T, addr, path string) int {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	require.NoError(t, err, "GET %s", path)
	resp.Body.Close()
	return resp.StatusCode
}

// TestASpecWithNoGraphQLStillServesTheProbes is the contract the package now makes: every
// Service answers the chart's probes, whether or not it has a GraphQL plane.
//
// 🔴 IT GOES OVER A REAL SOCKET, because the failure it exists for is not a missing route
// but a pod with no HTTP surface at all — every probe failing and every pod unready. A
// test that only looked at the mux would pass over a server that was never started.
func TestASpecWithNoGraphQLStillServesTheProbes(t *testing.T) {
	ephemeralProbes(t)
	ms := testMicroservice(t)
	svc := New(ms, Spec{})
	ctx := context.Background()

	require.NoError(t, svc.Initialize(ctx))
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop(ctx) })
	addr := svc.probes.server.Addr()
	require.NotEmpty(t, addr, "the probe server was built but never bound")

	require.Equal(t, http.StatusOK, get(t, addr, "/healthz"))
	require.Equal(t, http.StatusOK, get(t, addr, "/metrics"))
	// The gate is not open yet, and not-ready must read as not-ready.
	require.Equal(t, http.StatusServiceUnavailable, get(t, addr, "/readyz"))
	ms.MarkReadyWithoutAuthSurface()
	require.Equal(t, http.StatusOK, get(t, addr, "/readyz"),
		"/readyz is not reading the microservice's own readiness gate")

	// And a stop really closes the listener. A Stop that returned nil and left the socket
	// bound would look identical to a clean one from every other assertion here.
	require.NoError(t, svc.Stop(ctx))
	_, err := http.Get("http://" + addr + "/healthz")
	require.Error(t, err, "the probe server still answers after Stop")
}

// TestTheProbeServerDefaultsToTheChartsPort pins the default, because a wrong one fails
// silently: the service binds, logs a successful start and reports healthy, while the
// chart's probes and ServiceMonitor address the container port by name and find nothing.
// Every test that changes ProbesPort restores it, so the value seen here is the default.
func TestTheProbeServerDefaultsToTheChartsPort(t *testing.T) {
	require.EqualValues(t, core.HttpPort, ProbesPort)
	require.EqualValues(t, 8080, core.HttpPort,
		"the chart's container port is 8080; moving it here without the chart strands every probe")
}

// TestAServiceWithGraphQLDoesNotAlsoBuildTheProbeServer pins that the two probe surfaces
// are exclusive. Both register /healthz on the same mux, and ServeMux panics on a
// duplicate pattern — so building the probe server alongside a GraphQL manager takes the
// service down at Initialize.
func TestAServiceWithGraphQLDoesNotAlsoBuildTheProbeServer(t *testing.T) {
	ms := testMicroservice(t)
	svc := New(ms, Spec{GraphQL: &GraphQLSpec{
		Schema:   "type Query { ok: Boolean }",
		Resolver: func() interface{} { return &okResolver{} },
	}})

	require.NotPanics(t, func() {
		require.NoError(t, svc.Initialize(context.Background()))
	}, "the probes were registered twice on one mux")
	require.Nil(t, svc.probes, "a Service with a GraphQL plane built a second probe surface")
	require.Equal(t, "/healthz", routeFor(ms, "/healthz"),
		"premise lost: the GraphQL manager no longer registers the probes")
}

type okResolver struct{}

func (*okResolver) Ok() *bool { v := true; return &v }

// TestTheProbeServerIsFirstUpAndLastDown pins the position. Start walks the sequence
// forwards and Stop walks it backwards, so first in the sequence is first up and last
// down — which is what keeps /metrics answering while the broker drains.
//
// It reads ordered() rather than timing a real shutdown because ordered() is the only
// place a position can be stated; Start, Stop and Terminate all derive from it.
func TestTheProbeServerIsFirstUpAndLastDown(t *testing.T) {
	ms := testMicroservice(t)
	nats := &messaging.NatsManager{}
	svc := &Service{Microservice: ms, probes: newProbeServer(ms), Managers: Managers{Nats: nats}}

	seq := svc.ordered()
	require.Len(t, seq, 2)
	require.Same(t, svc.probes, seq[0], "the probe server is not first up and last down")
	require.Same(t, nats, seq[1])
}

// TestARetriedStartAfterAFailedBindServes is the retry the lifecycle permits: a start that
// fails restores the component to Initialized, and the caller may enter it again. Both
// halves of the probe server's shape are needed for that to work — routes registered once
// at Initialize, so the retry does not panic on a duplicate pattern, and a fresh server per
// start, so the retry is not a server that binds and serves nothing.
func TestARetriedStartAfterAFailedBindServes(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()

	ephemeralProbes(t)
	ProbesPort = int32(taken.Addr().(*net.TCPAddr).Port)
	svc := New(testMicroservice(t), Spec{})
	ctx := context.Background()
	require.NoError(t, svc.Initialize(ctx))

	err = svc.Start(ctx)
	require.Error(t, err, "binding a port already in use must refuse the start")
	require.ErrorContains(t, err, "the probe server", "the error does not say what failed to start")

	ProbesPort = 0
	require.NotPanics(t, func() { require.NoError(t, svc.Start(ctx)) },
		"a retried start re-registered a route")
	t.Cleanup(func() { _ = svc.Stop(ctx) })
	require.Equal(t, http.StatusOK, get(t, svc.probes.server.Addr(), "/healthz"),
		"the retried start did not serve")
}

// leftBehindPath stands in for a route a service forgot to move off the default mux.
const leftBehindPath = "/service-left-behind"

// registerLeftBehind registers it ONCE per process: ServeMux panics on a duplicate pattern
// and `go test -count=2` runs every test twice in one binary.
var registerLeftBehind = sync.OnceFunc(func() {
	http.HandleFunc(leftBehindPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
})

// TestTheProbeServerServesTheMicroservicesOwnMuxAndNothingElse is the negative control for
// the mux switchover, carried here from the services that used to build this server
// themselves.
//
// Giving an http.Server an explicit Handler silently unmounts everything on
// http.DefaultServeMux: http.Handle keeps compiling and keeps registering, onto a mux
// nobody serves. So a route on the default mux must 404 here — and, as the counterweight,
// a route on the microservice's own mux must be served, because "serves nothing from the
// default mux" is also true of a server that serves nothing at all.
func TestTheProbeServerServesTheMicroservicesOwnMuxAndNothingElse(t *testing.T) {
	registerLeftBehind()
	ephemeralProbes(t)
	ms := testMicroservice(t)
	ms.Mux().HandleFunc("/own-route", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	svc := New(ms, Spec{})
	ctx := context.Background()
	require.NoError(t, svc.Initialize(ctx))
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop(ctx) })

	require.Equal(t, http.StatusNotFound, get(t, svc.HttpAddr(), leftBehindPath),
		"a route on http.DefaultServeMux is being served")
	require.Equal(t, http.StatusOK, get(t, svc.HttpAddr(), "/own-route"),
		"a route the service registered on its own mux before Initialize is not served")
	require.Equal(t, http.StatusOK, get(t, svc.HttpAddr(), "/healthz"))
}
