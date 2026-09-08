// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// leftBehindPath stands in for a route that a switchover forgot to move: registered
// on http.DefaultServeMux, exactly as this service's probes used to be.
const leftBehindPath = "/lwm2m-left-behind"

// registerLeftBehind puts that route on the default mux ONCE per process.
//
// ServeMux panics on a duplicate pattern and `go test -count=2` runs every test twice
// in one binary, so a plain registration inside the test would be the shape this
// repository keeps having to fix: green under `go test`, a panic on a rerun.
var registerLeftBehind = sync.OnceFunc(func() {
	http.HandleFunc(leftBehindPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
})

// newTestMicroservice installs a Microservice with its own mux and metrics registry,
// restoring the package globals afterwards. Each call gets a fresh one, so the probe
// registrations of one test cannot collide with another's.
func newTestMicroservice(t *testing.T) {
	t.Helper()

	prevMs, prevSrv := Microservice, httpServer
	t.Cleanup(func() { Microservice, httpServer = prevMs, prevSrv })

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "lwm2m-ingest",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	// No validator, matching production: this service authenticates devices at DTLS-PSK and
	// verifies no JWT, so the gate is opened without an auth surface rather than left
	// closed (which would make every /readyz below a 503 for the wrong reason).
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	httpServer = nil
}

// get returns the status code for a path on the running server.
func get(t *testing.T, path string) int {
	t.Helper()
	resp, err := http.Get("http://" + httpServer.Addr() + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// A stop-then-start cycle must not panic.
//
// 🔴 THIS IS THE TRAP THE SWITCHOVER MAKES EASY TO WALK INTO. The probe routes are
// registered through ServeMux.Handle, which panics on a duplicate pattern, and
// LifecycleComponent's own contract says ExecuteStart "may happen on startup or after
// stop". Register from the start path and a lifecycle restart is a crash — not at the
// next deploy, but only for whoever stops and starts a service in place.
//
// It is not a regression the switchover introduces: http.HandleFunc on
// http.DefaultServeMux panicked on a duplicate too. That is exactly why it is worth a
// test — the shape carries across unchanged and nothing complains until the day
// something restarts.
//
// The assertion is that startHttpServer registers NOTHING: it is called twice here,
// with the routes registered once beforehand as afterMicroserviceInitialized does.
func TestHttpServerRestartDoesNotPanic(t *testing.T) {
	newTestMicroservice(t)
	// The SERVICE's registration, not a copy of it: a test that called RegisterProbes
	// itself would keep passing if this went back to http.Handle on the default mux.
	registerHttpRoutes()

	require.NoError(t, startHttpServer(0))
	t.Cleanup(func() { _ = httpServer.Shutdown(context.Background()) })
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "first start does not serve")

	require.NoError(t, httpServer.Shutdown(context.Background()))

	// Reaching this line at all is half the assertion: a RegisterProbes moved in here
	// panics, and a panic fails the binary rather than this test.
	require.NoError(t, startHttpServer(0), "restart refused")

	// And the other half, over the wire: a restart that binds but serves nothing is the
	// failure http.Server's latched shuttingDown flag produces, and it is invisible to
	// a nil error.
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "restarted server does not serve")
}

// The server must serve the microservice's own mux and NOTHING ELSE.
//
// This is the negative control for the switchover itself. Giving an http.Server an
// explicit Handler silently unmounts everything still on http.DefaultServeMux:
// http.Handle keeps compiling and keeps registering, onto a mux nobody serves, with no
// error and no log line. A route left behind by the move would therefore look exactly
// like a route that was never written.
//
// So a route IS left behind here, deliberately, and it must 404. If this service's
// server ever goes back to the default mux — by omitting Handler, or by anyone
// "fixing" a route with http.Handle — that route starts answering 200 and this fails.
func TestServerDoesNotServeTheDefaultMux(t *testing.T) {
	newTestMicroservice(t)
	// The SERVICE's registration, not a copy of it: a test that called RegisterProbes
	// itself would keep passing if this went back to http.Handle on the default mux.
	registerHttpRoutes()
	registerLeftBehind()

	require.NoError(t, startHttpServer(0))
	t.Cleanup(func() { _ = httpServer.Shutdown(context.Background()) })

	require.Equal(t, http.StatusNotFound, get(t, leftBehindPath),
		"a route on http.DefaultServeMux is being served; this server is not serving the microservice's own mux")

	// The counterweight, and it is not decoration: "serves nothing from the default
	// mux" is also satisfied by a server that serves nothing at all, which is the very
	// failure this whole change is about.
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "the owned probe routes are not served")
	require.Equal(t, http.StatusOK, get(t, "/readyz"))
	require.Equal(t, http.StatusOK, get(t, "/metrics"))
}
