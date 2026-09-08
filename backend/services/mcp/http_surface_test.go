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

// serveMcp installs a Microservice, drives THIS SERVICE'S registerHttpRoutes, and
// starts a real listener on an ephemeral port. It returns the base URL.
//
// It drives the production registrar rather than calling server.Routes and
// RegisterProbes itself: a test built from its own copy of the wiring keeps passing
// when the wiring moves, which is how a switchover ships probes that 404.
func serveMcp(t *testing.T) string {
	t.Helper()

	prevMs, prevSrv := Microservice, httpServer
	t.Cleanup(func() { Microservice, httpServer = prevMs, prevSrv })

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "mcp",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	httpServer = nil

	// A validator that exists but trusts a key nothing here signs with: every bearer
	// fails, which is what makes the MCP endpoint answer its 401 challenge rather than
	// the 503 an unopened gate would produce. The gate is opened for the same reason —
	// /readyz has to be able to report 200.
	key, err := coreauth.GenerateKeyPair()
	require.NoError(t, err)
	Microservice.Readiness.MarkReady(coreauth.NewValidator(&key.PublicKey))

	// Read back through the gate, exactly as afterMicroserviceInitialized does, rather
	// than closing over a second validator built here.
	registerHttpRoutes(testResource, testIssuer, Microservice.Readiness.Validator)
	require.NoError(t, startHttpServer(0))
	t.Cleanup(func() { _ = httpServer.Shutdown(context.Background()) })

	return "http://" + httpServer.Addr()
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

// Every pattern this service registers, asserted ONE AT A TIME.
//
// 🔴 THIS ROUTE SET IS A PROTOCOL CONTRACT. A client discovers this server by spec —
// POST the resource identifier, read the WWW-Authenticate challenge, fetch the metadata
// URL it names — so dropping any one of these is not a degraded service, it is a broken
// contract that clients hit at discovery time.
//
// 🔴 AND ON THIS SERVICE AN UNMOUNTED ROUTE DOES NOT ANSWER 404. The MCP endpoint is
// mounted at "/", so anything that stops being registered falls through to the catch-all
// and is answered with a 401 BEARER CHALLENGE instead. For a probe that is the worse of
// the two failures by some way: /readyz answering 401 is never 200, so every pod stays
// unready forever and the whole area leaves its Service endpoints — and the symptom
// points at authentication rather than at routing. (The ingest services have no
// catch-all, so there the same mistake really does surface as a 404. Copying the wording
// across is what made it wrong here.)
//
// Seven cases for six patterns: the well-known subtree gets two, one for the document
// it does serve and one for a path it must refuse, because the guard inside it is a
// behaviour of its own rather than a second spelling of the same route.
//
// A case per pattern, not one case for the set: a table that asserted "none of these is
// 404" would pass with the catch-all answering all of them, which is both the precedence
// failure this most needs to exclude and — per the paragraph above — not even the status
// code it would produce.
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
		{
			name: "liveness", method: http.MethodGet, path: "/healthz", want: http.StatusOK,
			why: "an exact pattern must beat the catch-all at /",
		},
		{
			name: "readiness", method: http.MethodGet, path: "/readyz", want: http.StatusOK,
			why: "an exact pattern must beat the catch-all at /",
		},
		{
			name: "metrics", method: http.MethodGet, path: "/metrics", want: http.StatusOK,
			why: "an exact pattern must beat the catch-all at /",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, status(t, tc.method, base+tc.path), tc.why)
		})
	}
}

// The catch-all must still take everything the specific patterns do not.
//
// The counterweight to the precedence cases above: "the probes beat /" is also
// satisfied by a mux where / matches nothing at all, which would break every MCP
// request while leaving all three probe assertions green.
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

// A stop-then-start cycle must not panic.
//
// Both registrars go through ServeMux.Handle, which panics on a duplicate pattern, and
// ExecuteStart may run after a stop — so this pins that startHttpServer registers
// nothing. It is not a regression the switchover introduces: http.HandleFunc on the
// default mux panicked on a duplicate too, which is why the shape carries across
// unnoticed.
func TestHttpServerRestartDoesNotPanic(t *testing.T) {
	base := serveMcp(t)
	require.Equal(t, http.StatusOK, status(t, http.MethodGet, base+"/healthz"))

	require.NoError(t, httpServer.Shutdown(context.Background()))

	// Reaching this line at all is half the assertion: a registrar moved into the start
	// path panics, and a panic fails the binary rather than this test.
	require.NoError(t, startHttpServer(0), "restart refused")

	// The other half, over the wire: a restart that binds but serves nothing is what
	// http.Server's latched shuttingDown flag produces, and a nil error cannot see it.
	require.Equal(t, http.StatusOK, status(t, http.MethodGet, "http://"+httpServer.Addr()+"/healthz"),
		"restarted server does not serve")
}
