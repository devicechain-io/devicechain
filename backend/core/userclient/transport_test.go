// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The client this package supplies when a caller passes none dials through the package's
// own transport, not the process default. The traffic is a login carrying a password and
// every call carrying the tenant access token.
func TestDefaultClientDoesNotUseTheProcessDefaultTransport(t *testing.T) {
	c := defaultHTTP(nil)

	if c.Transport == nil {
		t.Fatal("Transport is nil, which resolves to http.DefaultTransport: the login exchange " +
			"and every token-carrying call would honour the environment's proxy settings and " +
			"share the process-wide connection pool")
	}
	if c.Transport == http.DefaultTransport {
		t.Fatal("Transport is http.DefaultTransport")
	}
	if c.Timeout != requestTimeout {
		t.Fatalf("client timeout = %v, want %v", c.Timeout, requestTimeout)
	}

	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is a %T, not an *http.Transport", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("Proxy is set: a caller that must proxy passes its own client, so the default " +
			"must not pick one up from the environment")
	}

	// graphqlPost resolves the client on every request, so a fresh one per call would be
	// a fresh pool per call and nothing would ever be reused.
	if other := defaultHTTP(nil); other != c {
		t.Fatal("defaultHTTP(nil) returned a different client on a second call; it is resolved " +
			"per request, so this would open a connection per request")
	}
}

// A caller-supplied client is used as it stands when it states both a transport and a
// timeout, and is otherwise copied with the unstated field filled in. The caller's own
// client is never mutated.
func TestDefaultHTTPFillsInWhatACallerLeftUnstated(t *testing.T) {
	callerTransport := &http.Transport{}

	t.Run("fully stated is passed through untouched", func(t *testing.T) {
		in := &http.Client{Transport: callerTransport, Timeout: 3 * time.Second}
		if got := defaultHTTP(in); got != in {
			t.Fatal("a client stating both a transport and a timeout must be used as it stands, " +
				"so the caller keeps whatever dialer and TLS settings it configured")
		}
	})

	t.Run("no transport gets the package transport", func(t *testing.T) {
		in := &http.Client{Timeout: 3 * time.Second}
		got := defaultHTTP(in)
		if got == in {
			t.Fatal("the caller's client was returned unchanged, so it still dials through " +
				"http.DefaultTransport")
		}
		if got.Transport != sharedTransport {
			t.Fatalf("Transport = %v, want this package's shared transport", got.Transport)
		}
		if got.Timeout != 3*time.Second {
			t.Fatalf("timeout = %v, want the caller's 3s kept", got.Timeout)
		}
		if in.Transport != nil {
			t.Fatal("the caller's own client was mutated")
		}
	})

	t.Run("no timeout gets the package timeout", func(t *testing.T) {
		in := &http.Client{Transport: callerTransport}
		got := defaultHTTP(in)
		if got == in {
			t.Fatal("the caller's client was returned unchanged, so a login on it has no deadline; " +
				"the sessions renew on a detached context, so there is no cancellation path either")
		}
		if got.Timeout != requestTimeout {
			t.Fatalf("timeout = %v, want %v", got.Timeout, requestTimeout)
		}
		if got.Transport != callerTransport {
			t.Fatal("the caller's transport must be kept; only the unstated field is filled in")
		}
		if in.Timeout != 0 {
			t.Fatal("the caller's own client was mutated")
		}
	})
}

// A client from HTTPClient wraps a real base transport rather than falling through to the
// process default. The host pin it carries lives in the wrapper and in CheckRedirect, and
// is unaffected by which base is underneath — the pin's own arms are in httpclient_test.go
// and this asserts only what the base resolves to.
func TestSessionHTTPClientWrapsANonDefaultBaseTransport(t *testing.T) {
	s := NewTenantSession(nil, "http://user-management:8080/graphql", "u@x", "pw", "acme")
	c := s.HTTPClient("api.example.com")

	bt, ok := c.Transport.(*bearerTransport)
	if !ok {
		t.Fatalf("Transport is a %T, not the bearer-pinning transport", c.Transport)
	}
	if bt.base == nil {
		t.Fatal("the bearer transport has no base, so RoundTrip falls back rather than using " +
			"a transport this package chose")
	}
	if bt.base == http.DefaultTransport {
		t.Fatal("the bearer transport's base is http.DefaultTransport")
	}
	if c.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil: the host pin's redirect half is gone")
	}
	// Supplying CheckRedirect replaces the default, which is also what caps a redirect
	// chain, and the cap it carries is enforced against the client Timeout. A zero
	// timeout here would mean a same-host redirect loop with nothing to stop it.
	if c.Timeout == 0 {
		t.Fatal("the session client has no timeout")
	}
}

// The host pin still holds over the client this change hands the session.
//
// The pin's own arms (httpclient_test.go) drive a caller-supplied client that states
// both a transport and a timeout, which defaultHTTP passes through untouched — so they
// exercise the path that existed before this change and would not notice it either way.
// These arms use a caller client with Timeout 0, which is what makes defaultHTTP hand
// the session a COPY, and so exercise the pin over the client this change constructs.
//
// What these arms do NOT cover, stated because it is easy to assume otherwise: the
// caller here supplies a transport (the fixture's routing dialer), so the Transport
// fill-in never runs and deleting it leaves these arms green. That branch is covered by
// TestDefaultHTTPFillsInWhatACallerLeftUnstated instead, which is what fails when it
// goes. Removing either half of the pin, or the timeout fill-in, does fail here.
func TestHostPinHoldsOverTheClientDefaultHTTPBuilds(t *testing.T) {
	t.Run("the bearer reaches the pinned host and no other", func(t *testing.T) {
		pinned, other := newHostRecorder(), newHostRecorder()
		client := pinnedFixtureWithTimeout(t, 0,
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
	})

	t.Run("a redirect off the pinned host is refused", func(t *testing.T) {
		pinned, other := newHostRecorder(), newHostRecorder()
		client := pinnedFixtureWithTimeout(t, 0,
			func(w http.ResponseWriter, r *http.Request) {
				pinned.note(r)
				http.Redirect(w, r, "http://"+otherHost+"/graphql", http.StatusFound)
			},
			func(w http.ResponseWriter, r *http.Request) { other.note(r); writeData(w, `{"ok":true}`) })

		resp, err := client.Get("http://" + pinnedHost + "/graphql")
		if err == nil {
			resp.Body.Close()
			t.Fatal("a redirect off the pinned host must be refused")
		}
		if !strings.Contains(err.Error(), "refusing to follow a redirect") {
			t.Fatalf("the error must name the redirect refusal, got %v", err)
		}
		if got := other.count(); got != 0 {
			t.Fatalf("the unpinned host was contacted %d times; it must never be reached", got)
		}
	})

	// The client is also bounded even though the caller stated no timeout, which is what
	// the redirect-chain cap is enforced against: supplying CheckRedirect replaces
	// net/http's default, and that default is also what caps a chain at ten hops.
	t.Run("the client is bounded even though the caller stated no timeout", func(t *testing.T) {
		client := pinnedFixtureWithTimeout(t, 0,
			func(w http.ResponseWriter, r *http.Request) { writeData(w, `{"ok":true}`) },
			func(w http.ResponseWriter, r *http.Request) { writeData(w, `{"ok":true}`) })
		if client.Timeout != requestTimeout {
			t.Fatalf("client timeout = %v, want %v", client.Timeout, requestTimeout)
		}
	})
}
