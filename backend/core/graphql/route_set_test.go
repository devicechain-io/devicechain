// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-microservice/core"
)

// managerOnItsOwnMux builds a GraphQLManager over a fresh Microservice and runs the
// REAL registration — the same ExecuteInitialize the lifecycle calls.
//
// Each call gets its own Microservice, hence its own mux, so two shapes in one test
// binary cannot collide on a duplicate pattern.
func managerOnItsOwnMux(t *testing.T, area string) (*GraphQLManager, *core.Microservice) {
	t.Helper()

	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := core.NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()

	gql := &GraphQLManager{
		Microservice: ms,
		Schema:       MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{}),
		Gate:         gate,
	}
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	return gql, ms
}

func muxStatus(t *testing.T, ms *core.Microservice, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

// The route set this manager puts on the microservice's mux, with dev tools OFF —
// the shipped default, since DevToolsEnabled is secure-by-default.
//
// 🔴 A ROUTE MISSING HERE IS NOT AN ERROR ANYWHERE. Every server in this repository now
// serves an explicit handler, so a registration that stays on http.DefaultServeMux
// still compiles and still runs, onto a mux no listener consults. None of these
// services registers a "/" catch-all — the only one in the tree is mcp's, which serves
// its own mux — so the symptom is a plain 404 at a path the chart, the ingress or a
// peer service is asking for.
func TestRouteSetWithDevToolsDisabled(t *testing.T) {
	t.Setenv(EnvGraphQLDevTools, "")
	_, ms := managerOnItsOwnMux(t, "device-management")

	for _, tc := range []struct {
		path string
		want int
		why  string
	}{
		// 🔴 400, NOT 200, AND NOT "ANYTHING BUT 404". A bodyless GET reaches the
		// GraphQL handler and is rejected as a malformed request — which is the
		// handler answering, and therefore proof the route is mounted. An unmounted
		// route answers 404. Asserting the exact code rather than "not 404" keeps the
		// case honest: it says what this route does, so a change in that behaviour is
		// visible here rather than absorbed by a loose matcher.
		{"/graphql", http.StatusBadRequest, "a bodyless GET reaches the handler and is refused; a 404 would mean the route is not mounted at all"},
		{"/healthz", http.StatusOK, "the chart's liveness probe addresses this by name"},
		{"/readyz", http.StatusOK, "the chart's readiness and startup probes address this by name"},
		{"/metrics", http.StatusOK, "the ServiceMonitor scrapes this path"},
		{"/graphiql", http.StatusNotFound, "dev tools are off, so the explorer must NOT be mounted — secure by default"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := muxStatus(t, ms, http.MethodGet, tc.path); got != tc.want {
				t.Errorf("GET %s = %d, want %d — %s", tc.path, got, tc.want, tc.why)
			}
		})
	}
}

// The same set with dev tools ON, which is the axis that makes /graphiql a route at
// all. Without this case, /graphiql is covered only by its absence, and a registration
// that never fires would look exactly like the secure default.
func TestRouteSetWithDevToolsEnabled(t *testing.T) {
	t.Setenv(EnvGraphQLDevTools, "true")
	_, ms := managerOnItsOwnMux(t, "device-management")

	if got := muxStatus(t, ms, http.MethodGet, "/graphiql"); got != http.StatusOK {
		t.Errorf("GET /graphiql = %d with dev tools enabled, want 200", got)
	}
	// The counterweight: enabling the explorer must not disturb the rest. The bar is
	// "still mounted", so /graphql keeps its 400 (see the sibling test) and the probes
	// keep their 200.
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		if got := muxStatus(t, ms, http.MethodGet, p); got != http.StatusOK {
			t.Errorf("GET %s = %d with dev tools enabled, want 200", p, got)
		}
	}
	if got := muxStatus(t, ms, http.MethodGet, "/graphql"); got != http.StatusBadRequest {
		t.Errorf("GET /graphql = %d with dev tools enabled, want 400 (mounted and refusing a bodyless GET)", got)
	}
}

// A stop-then-start cycle must not panic.
//
// 🔴 THIS IS THE TRAP THE SWITCHOVER MADE REACHABLE. The routes were registered in
// ExecuteStart until this change, and LifecycleComponent's own contract says
// ExecuteStart "may happen on startup or after stop" — so a second start re-ran every
// registration. ServeMux.Handle panics on a duplicate pattern, and so does
// http.DefaultServeMux, which is why the hazard is not new: it was simply never
// reachable, because nothing restarted a GraphQL server in a test.
//
// Registration now lives in ExecuteInitialize, which runs once. This asserts that
// ExecuteStart registers nothing by running it twice.
func TestRestartDoesNotPanic(t *testing.T) {
	gql, _ := managerOnItsOwnMux(t, "restart-probe")

	// 🔴 ExecuteStart ITSELF, NOT A SERVER THIS TEST BUILDS. An earlier draft
	// constructed the HttpServer here and called Start on it, which exercised the
	// test's own wiring — so a registration moved back into ExecuteStart went entirely
	// undetected, because ExecuteStart never ran. Port 0 is what makes driving the real
	// one possible without racing whatever holds 8080.
	gql.Port = ephemeralPort
	if err := gql.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("first start: %v", err)
	}
	first := gql.Server.Addr()
	if err := gql.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Reaching this line at all is half the assertion: a registration moved back into
	// the start path panics here, and a panic fails the binary rather than this test.
	if err := gql.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("restart refused: %v", err)
	}
	t.Cleanup(func() { _ = gql.ExecuteStop(context.Background()) })

	if gql.Server.Addr() == first {
		t.Error("the restarted server reports the first server's address; it was reused rather than rebuilt")
	}
	// And over the wire, because a restart that binds but serves nothing is exactly what
	// a reused http.Server produces and a nil error cannot see.
	resp, err := http.Get("http://" + gql.Server.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz after restart: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz after restart = %d, want 200", resp.StatusCode)
	}
}

// The constructor must set Port to the chart's port.
//
// 🔴 EVERY OTHER TEST IN THIS PACKAGE BUILDS A GraphQLManager AS A LITERAL, SO NOTHING
// ELSE HERE RUNS THE CONSTRUCTOR AT ALL. Deleting the default from NewGraphQLManager
// left core/graphql, user-management and ai-inference every one of them green — the
// single production line this change introduced was the one line nothing pinned.
//
// listenPort now makes the zero value safe, so a dropped default yields GRAPHQL_PORT
// rather than a random port. This asserts the constructor's own contract as well,
// because the two failures differ: listenPort protects against the value being LOST,
// this protects against it being set to the WRONG one.
func TestNewGraphQLManagerBindsTheChartsPort(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "ctor-probe"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gql := NewGraphQLManager(ms, core.NewNoOpLifecycleCallbacks(),
		MustParseSchema(`schema { query: Query } type Query { whoAmI: String! }`, &echoRoot{}),
		map[ContextKey]interface{}{}, core.NewReadinessGate())

	if gql.Port != GRAPHQL_PORT {
		t.Errorf("NewGraphQLManager set Port = %d, want %d — the chart addresses this container "+
			"port by name in its probes and its ServiceMonitor", gql.Port, GRAPHQL_PORT)
	}
	if got := gql.listenPort(); got != GRAPHQL_PORT {
		t.Errorf("listenPort() = %d, want %d", got, GRAPHQL_PORT)
	}
}

// A Port nobody set must still bind the chart's port.
//
// This is what listenPort exists for. net.Listen reads 0 as "any free port", so a lost
// default would have all eleven services bind a random one, log a successful start and
// report healthy, while the probes went on addressing a port with nothing on it.
func TestAZeroPortMeansTheChartsPortNotAnEphemeralOne(t *testing.T) {
	gql := &GraphQLManager{} // Port at its zero value, which is what a dropped default leaves
	if got := gql.listenPort(); got != GRAPHQL_PORT {
		t.Errorf("listenPort() with an unset Port = %d, want %d; 0 must never reach net.Listen "+
			"as a production port", got, GRAPHQL_PORT)
	}
	// And the test-only sentinel still resolves to an ephemeral bind, or every test in
	// this package would fight over 8080.
	if got := (&GraphQLManager{Port: ephemeralPort}).listenPort(); got != 0 {
		t.Errorf("listenPort() with the ephemeral sentinel = %d, want 0", got)
	}
}

// ExecuteStop must tolerate a server that never started: a teardown still runs after a
// startup that refused partway through, and a panic there replaces the real error.
func TestStopWithoutStartIsANoOp(t *testing.T) {
	gql, _ := managerOnItsOwnMux(t, "never-started")
	if err := gql.ExecuteStop(context.Background()); err != nil {
		t.Errorf("ExecuteStop before any start = %v, want nil", err)
	}
}
