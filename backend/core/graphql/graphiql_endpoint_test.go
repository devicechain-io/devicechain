// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/friendsofgo/graphiql"
)

// The endpoint is templated into the page as fetch('<endpoint>'). Pull it back out
// of the rendered HTML rather than asserting the constant directly, so the value
// is checked as the browser receives it — after the library's templating, which is
// where the escaping below comes from.
//
// This does NOT cover the wiring: it builds its own handler from the constant, so
// nothing here would notice if ExecuteStart stopped passing the constant to
// NewGraphiqlHandler. That seam is awkward to reach — ExecuteStart registers onto
// http.DefaultServeMux, so calling it twice panics — and is left uncovered
// knowingly rather than by oversight.
var fetchTarget = regexp.MustCompile(`fetch\('([^']*)'`)

func renderedGraphiqlEndpoint(t *testing.T) string {
	t.Helper()

	h, err := graphiql.NewGraphiqlHandler(graphiqlEndpoint)
	if err != nil {
		t.Fatalf("NewGraphiqlHandler(%q): %v", graphiqlEndpoint, err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/graphiql", nil))

	m := fetchTarget.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no fetch('...') call in the rendered GraphiQL page; the template changed shape and this test can no longer see the endpoint")
	}

	// html/template escapes "/" as "\/" inside a JS string literal. A JS engine
	// unescapes that before fetch() ever sees it, so undo it here — otherwise an
	// absolute endpoint is judged as a path containing literal backslashes, and
	// this test reports the wrong reason for the failure (or misses one entirely).
	return strings.ReplaceAll(m[1], `\/`, "/")
}

// The explorer is reached through two different path prefixes, and the endpoint it
// posts to has to land on a route the service actually serves in BOTH. A browser
// resolves the fetch() target against the document URL, so this walks the same
// resolution the browser performs. (The console's vite dev proxy strips the same
// /api/<area> prefix the ingress does, so the second case covers it too.)
//
// This is a regression test for an endpoint of "/<instance>/<tenant>/<area>/graphql",
// which is not served under either prefix — the page loaded and every query 404'd.
func TestGraphiqlEndpointResolvesToAServedRoute(t *testing.T) {
	endpoint := renderedGraphiqlEndpoint(t)

	cases := []struct {
		name string
		// Where the explorer page itself is served from.
		document string
		// The path the fetch() must land on. "/graphql" is the route ExecuteStart
		// registers; through the ingress the /api/<area> prefix is stripped before
		// the request reaches the service, so it arrives as "/graphql" there too.
		want string
	}{
		{
			name:     "straight at the pod (port-forward)",
			document: "http://localhost:8080/graphiql",
			want:     "/graphql",
		},
		{
			name:     "through the ingress",
			document: "https://dc.example.com/api/device-management/graphiql",
			want:     "/api/device-management/graphql",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := url.Parse(tc.document)
			if err != nil {
				t.Fatalf("parse document URL: %v", err)
			}
			ref, err := url.Parse(endpoint)
			if err != nil {
				t.Fatalf("parse endpoint %q: %v", endpoint, err)
			}

			if got := base.ResolveReference(ref).Path; got != tc.want {
				t.Errorf("GraphiQL at %s posts to %q, which resolves to %q; want %q — no such route is served, so every query from the explorer would 404",
					tc.document, endpoint, got, tc.want)
			}
		})
	}
}

// The relative form is what makes one value correct under both prefixes. An
// absolute endpoint necessarily breaks one of them, so guard the shape too — a
// future edit to a leading-slash path would still pass the pod-local case above
// while silently breaking the ingress one.
func TestGraphiqlEndpointIsRelative(t *testing.T) {
	endpoint := renderedGraphiqlEndpoint(t)

	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	if u.IsAbs() || len(endpoint) == 0 || endpoint[0] == '/' {
		t.Errorf("GraphiQL endpoint %q is absolute; it must be relative so it resolves correctly both at /graphiql and at /api/<area>/graphiql", endpoint)
	}
}
