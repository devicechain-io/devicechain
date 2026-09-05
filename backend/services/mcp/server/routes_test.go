// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// routesResource is the resource identifier the chart derives for a real instance:
// the public origin with the ingress's /api/<area> prefix. Every path assertion below
// is derived from it rather than spelled out, so moving the prefix moves the
// expectations with it.
const routesResource = "https://iot.example.com/api/mcp"

// sidecarPatterns are the patterns main.go registers beside Routes. They are listed
// here rather than imported because main is a package no test can reach.
//
// 🔑 WHAT THIS PROVES AND WHAT IT DOES NOT. It proves ServeMux's precedence: a
// catch-all at "/" does not swallow these patterns. It does NOT prove main.go
// registers them — nothing can, from here — so a pattern deleted there is still
// invisible. That is a smaller and more honest claim than "the probes work", and it is
// the one worth having: mounting the MCP endpoint at the root is the change that could
// have broken them, and precedence is the property that change depends on.
var sidecarPatterns = []string{"/healthz", "/readyz", "/metrics"}

// routesMux builds the mux this service actually serves, plus the patterns main.go
// adds beside it, so the precedence between them is exercised too.
func routesMux(t *testing.T, resource string) *http.ServeMux {
	t.Helper()
	_, validator := mustIssuerValidator(t)
	mux := http.NewServeMux()
	Routes(mux, resource, "https://iot.example.com/api/user-management", validator)
	for _, p := range sidecarPatterns {
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	return mux
}

// 🔴 THE END-TO-END DISCOVERY WALK, DRIVEN AS A CLIENT DRIVES IT: POST the endpoint,
// read the challenge, fetch the URL the challenge names, and require a metadata
// document back. Every previous test in this package handed a handler its own request,
// which is why both routing defects survived a package full of green tests — the
// handlers were always right, and neither was reachable at the URL that was published.
//
// The one thing this cannot see is the ingress, which rewrites the path between the
// client and this mux. hack/check-mcp-routing.sh renders the chart and asserts that
// half; the two are split because neither instrument can reach the other's evidence.
func TestDiscoveryWalkReachesTheMetadataDocument(t *testing.T) {
	mux := routesMux(t, routesResource)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 1. A client POSTs the resource identifier it was configured with. The ingress
	// strips /api/mcp, so what arrives here is "/". It must be the MCP endpoint —
	// before this fix it was an unauthenticated 404, which does not look like an auth
	// problem and gives a client nothing to follow.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("posting the resource identifier: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST to the advertised endpoint: status = %d, want 401. A 404 here is the "+
			"defect this test exists for: the endpoint was mounted at /mcp while the ingress "+
			"delivered the advertised URL as \"/\"", resp.StatusCode)
	}

	// 2. The challenge names the metadata URL. Parsed the way a client parses it —
	// through the SDK's own header parser, not a substring match — because a challenge
	// that merely CONTAINS the string "resource_metadata" told us nothing about where
	// it pointed, which is exactly how the wrong URL stayed advertised.
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		t.Fatalf("parsing WWW-Authenticate %q: %v", resp.Header.Values("WWW-Authenticate"), err)
	}
	advertised := ""
	for _, c := range challenges {
		if u := c.Params["resource_metadata"]; u != "" {
			advertised = u
		}
	}
	if advertised == "" {
		t.Fatalf("no resource_metadata in %q", resp.Header.Values("WWW-Authenticate"))
	}

	// It must be the RFC 9728 §3.1 location: the suffix INSERTED between host and the
	// identifier's path. Asserted as a whole URL because the origin matters too — the
	// document is discovered on the resource's own host, not the mux's.
	if want := "https://iot.example.com" + ProtectedResourceMetadataPath + "/api/mcp"; advertised != want {
		t.Errorf("advertised metadata URL = %q, want %q (RFC 9728 §3.1 inserts the "+
			"well-known suffix between the host and the resource's path)", advertised, want)
	}

	// 3. Fetch it. The chart routes this path WITHOUT a rewrite, so the path a client
	// asks for is the path that arrives — which is what lets this assertion stand in
	// for the real fetch at all.
	u, err := url.Parse(advertised)
	if err != nil {
		t.Fatalf("the advertised metadata URL does not parse: %v", err)
	}
	assertServesMetadata(t, ts.URL+u.EscapedPath(), routesResource)
}

// The §3.1 location is not the only one that has to answer. The /api/mcp ingress rule
// strips its prefix, so a lenient client that APPENDS the well-known suffix to the
// resource identifier arrives here at the bare suffix; and a path-less resource
// identifier makes the bare suffix the §3.1 location itself. Both are served.
func TestTheBareWellKnownPathIsAlsoServed(t *testing.T) {
	mux := routesMux(t, routesResource)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	assertServesMetadata(t, ts.URL+ProtectedResourceMetadataPath, routesResource)
}

// A path-less resource identifier collapses to one location, and it must still be the
// one advertised. Without this the §3.1 base case is only ever asserted through the
// path-carrying case, where a bug that always appended the path would look identical.
func TestAPathlessResourceIdentifierUsesTheBareLocation(t *testing.T) {
	const resource = "https://mcp.example.com"
	mux := routesMux(t, resource)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if got := metadataURL(resource); got != resource+ProtectedResourceMetadataPath {
		t.Errorf("metadataURL(%q) = %q", resource, got)
	}
	assertServesMetadata(t, ts.URL+ProtectedResourceMetadataPath, resource)
}

// The catch-all endpoint must not swallow what main.go registers beside it: a
// readiness probe answered with a 401 challenge marks every pod unready forever, and a
// scrape answered with one takes the area's metrics off every dashboard and alert.
func TestTheRootMountDoesNotSwallowTheSidecarPatterns(t *testing.T) {
	mux := routesMux(t, routesResource)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	for _, path := range sidecarPatterns {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200 — the root-mounted MCP endpoint is "+
				"answering it", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// The whole well-known subtree is routed here by the ingress — that is what lets the
// chart route a constant suffix instead of computing the RFC 9728 insertion itself —
// so this service is the thing that decides which path under it is its document.
//
// 🔴 THE 404 IS THE POINT, NOT AN AFTERTHOUGHT. Without it the subtree falls through to
// the root-mounted MCP endpoint, and a client that built a slightly wrong metadata URL
// gets a 401 bearer challenge in answer to a request for a PUBLIC discovery document —
// which sends it back round the same loop instead of telling it the path is wrong.
func TestTheWellKnownSubtreeIsRefusedExceptAtTheDocumentsOwnPath(t *testing.T) {
	mux := routesMux(t, routesResource)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// The appended form a lenient client might build under the subtree, the identifier
	// of some other resource, and a near-miss on this one.
	for _, path := range []string{
		ProtectedResourceMetadataPath + "/api/other",
		ProtectedResourceMetadataPath + "/api/mcp/extra",
		ProtectedResourceMetadataPath + "/",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, resp.StatusCode)
			continue
		}
		// A 404 that does not say where the document IS leaves the client no better
		// off than the fall-through did.
		if want := ProtectedResourceMetadataPathFor(routesResource); !strings.Contains(string(body), want) {
			t.Errorf("GET %s: the refusal does not name %q: %q", path, want, string(body))
		}
	}
}

// assertServesMetadata requires a real RFC 9728 document at url, identifying resource.
// The `resource` check is what a client performs (§3.3) before trusting the document,
// so a location that answers with the WRONG document fails here rather than passing as
// "a 200".
func assertServesMetadata(t *testing.T, url, resource string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	var md protectedResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		t.Fatalf("GET %s: body is not the metadata document: %v", url, err)
	}
	if md.Resource != resource {
		t.Errorf("GET %s: resource = %q, want %q — a client rejects a document that does "+
			"not identify the resource it asked about", url, md.Resource, resource)
	}
	if len(md.AuthorizationServers) == 0 {
		t.Errorf("GET %s: no authorization_servers, which the MCP specification requires", url)
	}
}
