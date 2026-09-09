// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two hostnames these tests drive. They must be genuinely different names: net/http
// decides whether Authorization survives a redirect by comparing URL.Hostname(), so two
// httptest servers are the same host to it — both are 127.0.0.1 — and a test built that
// way would report a pass no matter what the client does with the header.
const (
	pinnedHost = "pinned.example"
	otherHost  = "other.example"
)

// hostRouter dials a named host to the loopback address of the test server standing in
// for it, which is what lets the tests use distinct hostnames without touching DNS. An
// unmapped host (the auth stub, on 127.0.0.1) is dialled as given.
type hostRouter map[string]string // hostname -> "127.0.0.1:port"

func (r hostRouter) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if mapped, ok := r[host]; ok {
		addr = mapped
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// hostRecorder captures what one of those hosts was actually sent.
type hostRecorder struct {
	mu    sync.Mutex
	calls int
	auth  map[string]string // request path -> the Authorization header as received
}

func newHostRecorder() *hostRecorder { return &hostRecorder{auth: map[string]string{}} }

func (h *hostRecorder) note(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.auth[r.URL.Path] = r.Header.Get("Authorization")
}

func (h *hostRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func (h *hostRecorder) authFor(path string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.auth[path]
}

// pinnedFixture stands up an auth stub plus a server for each of the two hostnames, and
// returns a client from HTTPClient pinned to pinnedHost.
func pinnedFixture(t *testing.T, pinnedH, otherH http.HandlerFunc) *http.Client {
	t.Helper()
	return pinnedFixtureWithTimeout(t, 10*time.Second, pinnedH, otherH)
}

// pinnedFixtureWithTimeout is pinnedFixture with the caller-supplied client's Timeout
// under the test's control. A zero timeout is not a variation for its own sake: it is
// the one input that makes defaultHTTP hand the session a COPY of the caller's client
// rather than the client itself, so it is what exercises the pin over that copy.
func pinnedFixtureWithTimeout(t *testing.T, timeout time.Duration, pinnedH, otherH http.HandlerFunc) *http.Client {
	t.Helper()
	stub := &authStub{selectExp: farFuture()}
	auth := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(auth.Close)
	pinnedSrv := httptest.NewServer(pinnedH)
	t.Cleanup(pinnedSrv.Close)
	otherSrv := httptest.NewServer(otherH)
	t.Cleanup(otherSrv.Close)

	router := hostRouter{
		pinnedHost: pinnedSrv.Listener.Addr().String(),
		otherHost:  otherSrv.Listener.Addr().String(),
	}
	httpc := &http.Client{
		Transport: &http.Transport{DialContext: router.dial},
		Timeout:   timeout,
	}
	s := NewTenantSession(httpc, auth.URL, "u@x", "pw", "acme")
	return s.HTTPClient(pinnedHost)
}

// The bearer goes to the host the client is pinned to, and to no other host — even
// without a redirect, since the token is attached per hop by a RoundTripper and a
// caller can point the same client anywhere.
func TestHTTPClientAttachesBearerOnlyToPinnedHost(t *testing.T) {
	pinned, other := newHostRecorder(), newHostRecorder()
	client := pinnedFixture(t,
		func(w http.ResponseWriter, r *http.Request) { pinned.note(r); writeData(w, `{"ok":true}`) },
		func(w http.ResponseWriter, r *http.Request) { other.note(r); writeData(w, `{"ok":true}`) })

	resp, err := client.Get("http://" + pinnedHost + "/graphql")
	if err != nil {
		t.Fatalf("pinned host: %v", err)
	}
	resp.Body.Close()
	if got := pinned.authFor("/graphql"); !strings.HasPrefix(got, "Bearer access-") {
		t.Fatalf("pinned host must receive the tenant bearer, got %q", got)
	}

	resp, err = client.Get("http://" + otherHost + "/graphql")
	if err != nil {
		t.Fatalf("other host: %v", err)
	}
	resp.Body.Close()
	if got := other.authFor("/graphql"); got != "" {
		t.Fatalf("a host other than the pinned one must receive no Authorization header, got %q", got)
	}
}

// A redirect off the pinned host is not followed at all, so the other host is never even
// contacted by a client carrying this session's token.
func TestHTTPClientRefusesCrossHostRedirect(t *testing.T) {
	pinned, other := newHostRecorder(), newHostRecorder()
	client := pinnedFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			pinned.note(r)
			http.Redirect(w, r, "http://"+otherHost+"/leak", http.StatusFound)
		},
		func(w http.ResponseWriter, r *http.Request) { other.note(r); writeData(w, `{"ok":true}`) })

	resp, err := client.Get("http://" + pinnedHost + "/graphql")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("a redirect off the pinned host must not be followed")
	}
	if !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Fatalf("expected the redirect to be refused by the pin, got: %v", err)
	}
	if n := other.count(); n != 0 {
		t.Fatalf("the redirect target must not be contacted at all, got %d request(s)", n)
	}
	if got := other.authFor("/leak"); got != "" {
		t.Fatalf("the redirect target must never see an Authorization header, got %q", got)
	}
}

// The counterweight: pinning must not break the redirects it is meant to allow. A hop
// that stays on the pinned host is followed and still carries the bearer, and a changed
// port does not make it a different host — the same rule net/http itself applies.
func TestHTTPClientKeepsBearerAcrossSameHostRedirect(t *testing.T) {
	pinned, other := newHostRecorder(), newHostRecorder()
	client := pinnedFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			pinned.note(r)
			if r.URL.Path == "/graphql" {
				http.Redirect(w, r, "http://"+pinnedHost+":8443/moved", http.StatusFound)
				return
			}
			writeData(w, `{"ok":true}`)
		},
		func(w http.ResponseWriter, r *http.Request) { other.note(r); writeData(w, `{"ok":true}`) })

	resp, err := client.Get("http://" + pinnedHost + "/graphql")
	if err != nil {
		t.Fatalf("a redirect that stays on the pinned host must be followed: %v", err)
	}
	resp.Body.Close()
	if pinned.count() != 2 {
		t.Fatalf("expected both hops to reach the pinned host, got %d", pinned.count())
	}
	if got := pinned.authFor("/moved"); !strings.HasPrefix(got, "Bearer access-") {
		t.Fatalf("the redirected hop must still carry the tenant bearer, got %q", got)
	}
}

// Supplying CheckRedirect replaces net/http's default, and the default is also what caps
// a redirect chain — so the cap is now this package's to enforce. Without it a same-host
// loop is followed until the client's Timeout, and a caller-supplied client with no
// Timeout would loop forever.
func TestHTTPClientStopsASameHostRedirectLoop(t *testing.T) {
	pinned, other := newHostRecorder(), newHostRecorder()
	client := pinnedFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			pinned.note(r)
			http.Redirect(w, r, "http://"+pinnedHost+"/loop", http.StatusFound)
		},
		func(w http.ResponseWriter, r *http.Request) { other.note(r); writeData(w, `{"ok":true}`) })

	resp, err := client.Get("http://" + pinnedHost + "/loop")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("a same-host redirect loop must be stopped")
	}
	want := fmt.Sprintf("stopped after %d redirects", maxRedirects)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected the loop to be stopped by the redirect cap (%q), got: %v", want, err)
	}
	// The cap fires when via already holds maxRedirects requests, so the server sees
	// exactly that many — the hop that would have been the 11th is never made.
	if n := pinned.count(); n != maxRedirects {
		t.Fatalf("expected exactly %d requests before the cap, got %d", maxRedirects, n)
	}
}

// The pin accepts the forms a caller has at hand — a bare hostname, a host:port, or the
// endpoint URL itself — and rejects a host that merely resembles it, or a string that
// names no host at all.
func TestNormalizeHostAndMatch(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"api.example.com", "api.example.com"},
		{"API.Example.com", "api.example.com"},
		{" api.example.com:8443 ", "api.example.com"},
		{"https://api.example.com/graphql", "api.example.com"},
		{"https://api.example.com:8443/graphql", "api.example.com"},
		{"[::1]:8443", "::1"},
		{"::1", "::1"},
		{"", ""},
		// A URL naming no host must pin nothing. Read as a host:port these would
		// yield the scheme, "http" — a name a Kubernetes Service can have, so the
		// client would be pinned to a real and wrong host rather than to nothing.
		{"http:///graphql", ""},
		{"http://", ""},
		// Userinfo is not a host:port. Kept whole, it matches no URL hostname; read
		// as a host:port it would yield "user".
		{"user:pw@api.example.com", "user:pw@api.example.com"},
	} {
		if got := normalizeHost(tc.in); got != tc.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}
	for _, tc := range []struct {
		pin, url string
		want     bool
	}{
		{"api.example.com", "https://api.example.com/graphql", true},
		{"api.example.com", "https://API.example.com/graphql", true},
		{"api.example.com", "https://api.example.com:8443/graphql", true},
		{"api.example.com", "https://evil.example.com/graphql", false},
		{"api.example.com", "https://api.example.com.evil.net/graphql", false},
		{"api.example.com", "https://sub.api.example.com/graphql", false},
		{"", "https://api.example.com/graphql", false},
		// The blank-pin guard's own input class: a URL that also names no host.
		// Without the guard both sides are "" and the pin would match — the row
		// above cannot show that, since a non-empty hostname never equals "".
		{"", "http:///graphql", false},
	} {
		if got := hostMatches(tc.pin, mustParse(tc.url)); got != tc.want {
			t.Errorf("hostMatches(%q, %q) = %v, want %v", tc.pin, tc.url, got, tc.want)
		}
	}
}
