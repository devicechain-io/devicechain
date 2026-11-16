// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-mcp/server"
	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/service"
)

const (
	// testResource is the resource identifier the chart derives from an ingress: it
	// CARRIES A PATH, which is the shipped shape and the one the RFC 9728 §3.1
	// insertion actually has work to do for.
	testResource = "https://iot.example.com/api/mcp"
	testIssuer   = "https://iot.example.com/api/user-management"
)

// leftBehindWellKnown is a real well-known path that a switchover could strand on
// http.DefaultServeMux. It is deliberately INSIDE the subtree this service claims, so
// leaving it behind is the plausible mistake rather than an invented one.
const leftBehindWellKnown = server.ProtectedResourceMetadataPath + "/left-behind"

// registerLeftBehind puts that route on the default mux ONCE per process. ServeMux
// panics on a duplicate pattern and `go test -count=2` runs every test twice in one
// binary, so a plain registration inside a test is green under `go test` and a panic on
// a rerun.
var registerLeftBehind = sync.OnceFunc(func() {
	http.HandleFunc(leftBehindWellKnown, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
})

// serveMcp installs a Microservice and drives THIS SERVICE'S registerHttpRoutes,
// initializeService and start callback, on an ephemeral port. It returns the base URL.
//
// It drives the production wiring rather than calling server.Routes and assembling a
// Service itself: a test built from its own copy of the wiring keeps passing when the
// wiring moves, which is how a switchover ships probes that 404.
func serveMcp(t *testing.T) string {
	t.Helper()

	prevMs, prevSvc, prevPort := Microservice, Svc, service.ProbesPort
	t.Cleanup(func() { Microservice, Svc, service.ProbesPort = prevMs, prevSvc, prevPort })
	service.ProbesPort = 0

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "mcp",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	// A validator that exists but trusts a key nothing here signs with: every bearer
	// fails, which is what makes the MCP endpoint answer its 401 challenge rather than
	// the 503 an unopened gate would produce. The gate is opened for the same reason —
	// /readyz has to be able to report 200.
	key, err := coreauth.GenerateKeyPair()
	require.NoError(t, err)
	Microservice.Readiness.MarkReady(coreauth.NewValidator(&key.PublicKey))

	// Read back through the gate, exactly as afterMicroserviceInitialized does, rather
	// than closing over a second validator built here.
	registerHttpRoutes(testResource, testIssuer, Microservice.Readiness.Validator,
		server.NewGraphQLClient(), server.DefaultLimits())
	ctx := context.Background()
	require.NoError(t, initializeService(ctx))
	require.NoError(t, afterMicroserviceStarted(ctx))
	t.Cleanup(func() { _ = beforeMicroserviceStopped(ctx) })

	return "http://" + Svc.HttpAddr()
}

// nonFollowingClient reports a redirect rather than following it, so a test can assert
// on the redirect itself.
//
// ⚠️ That is NOT how a real client behaves, and the difference matters — see
// TestTheExactWellKnownPathIsServedDirectly.
var nonFollowingClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func status(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	require.NoError(t, err)
	resp, err := nonFollowingClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// Every public MCP pattern must resolve on the public listener, including both
// RFC 9728 metadata locations. Probes use the separate ops listener.
func TestEveryRegisteredPatternResolves(t *testing.T) {
	base := serveMcp(t)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   int
		why    string
	}{
		{
			name: "the MCP endpoint at the catch-all root", method: http.MethodPost, path: "/",
			want: http.StatusUnauthorized,
			why:  "401 is the endpoint working: it is the bearer challenge that carries the metadata URL. A 404 here means the endpoint is not mounted at all",
		},
		{
			name: "the protected-resource metadata document", method: http.MethodGet,
			path: server.ProtectedResourceMetadataPath, want: http.StatusOK,
			why: "this is the document the 401 challenge points a client at; it is public and unauthenticated",
		},
		{
			name: "the metadata document at its RFC 9728 inserted path", method: http.MethodGet,
			path: server.ProtectedResourceMetadataPathFor(testResource), want: http.StatusOK,
			why: "the path a spec-following client CONSTRUCTS for a path-carrying resource identifier, which is what the chart derives from an ingress",
		},
		{
			name: "a stray path in the claimed well-known subtree", method: http.MethodGet,
			path: server.ProtectedResourceMetadataPath + "/not-this-one", want: http.StatusNotFound,
			why: "the subtree pattern must answer here rather than falling through to the root-mounted MCP endpoint, which would return a bearer challenge to a request for a public document",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, status(t, tc.method, base+tc.path), tc.why)
		})
	}
}

func TestOperationalEndpointsUseTheOpsListener(t *testing.T) {
	public := serveMcp(t)
	ops := "http://" + Svc.OpsAddr()
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		require.Equal(t, http.StatusOK, status(t, http.MethodGet, ops+path))
		// The public root is the MCP handler, so these paths get its bearer
		// challenge rather than the operational handler's successful response.
		require.Equal(t, http.StatusUnauthorized, status(t, http.MethodGet, public+path))
	}
	require.Equal(t, http.StatusNotFound, status(t, http.MethodGet, ops+server.ProtectedResourceMetadataPath))
}

// The public catch-all must still reach the MCP handler for unmatched paths.
func TestTheCatchAllStillTakesEverythingElse(t *testing.T) {
	base := serveMcp(t)

	for _, path := range []string{"/", "/anything", "/deeply/nested/path", "/healthzz"} {
		require.Equal(t, http.StatusUnauthorized, status(t, http.MethodPost, base+path),
			"POST %s should reach the root-mounted MCP endpoint and get its bearer challenge", path)
	}
}

// The exact well-known path and its subtree form are two distinct patterns, and both
// are registered.
//
// Register only the subtree form and ServeMux answers the exact path with an automatic
// redirect to the trailing-slash form instead — a 307, StatusTemporaryRedirect, which is
// what net/http emits here.
//
// 🔴 THAT REDIRECT DOES NOT MERELY INCONVENIENCE A CLIENT, IT BREAKS DISCOVERY, AND THE
// REASON IS NOT "CLIENTS DO NOT FOLLOW REDIRECTS" — THEY DO. The MCP go-sdk fetches this
// document with http.DefaultClient, which follows redirects, so it would follow the 307
// to the trailing-slash path, land on the subtree guard, and get the guard's 404 —
// correctly, because that path is not where this server's document lives. The sdk fails
// the fetch on any non-200. The document is unreachable either way; only the error the
// client reports differs.
//
// So asserting 200 at the exact path is what says both patterns are present.
func TestTheExactWellKnownPathIsServedDirectly(t *testing.T) {
	base := serveMcp(t)

	require.Equal(t, http.StatusOK, status(t, http.MethodGet, base+server.ProtectedResourceMetadataPath),
		"the exact metadata path must be served directly; a 307 here means only the subtree pattern is "+
			"registered, and a client that follows it lands on the subtree guard's 404")
}

// A route left behind on http.DefaultServeMux must not be served.
//
// 🔴 READ WHICH ASSERTION CATCHES WHAT, because the tests here fail for different
// regressions:
//
//   - The 404 below fires if the SERVER stops serving the owned mux — if NewHttpServer
//     stopped setting Handler, say. It ALSO fires if the well-known subtree guard is
//     dropped, since this path sits inside the claimed subtree and would then reach the
//     catch-all and be answered 401. What it does NOT catch is this service leaving one
//     of its own routes on the default mux: such a route keeps 404ing either way,
//     because it was never on the owned mux to begin with.
//   - The route-set cases in TestEveryRegisteredPatternResolves are what catch that:
//     the abandoned route stops being served and its case turns 401 at the catch-all.
//
// Both are worth having; conflating them would leave the reader expecting the wrong one
// to go red.
func TestServerDoesNotServeTheDefaultMux(t *testing.T) {
	registerLeftBehind()
	base := serveMcp(t)

	require.Equal(t, http.StatusNotFound, status(t, http.MethodGet, base+leftBehindWellKnown),
		"a route on http.DefaultServeMux is being served; this server is not serving the microservice's own mux")
}
