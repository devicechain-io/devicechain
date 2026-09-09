// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httptransport

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The transport New builds is not the process-wide default, and no two calls share one.
//
// The assertion is on the VALUE, not on what the constructor's source says: a transport
// that ended up as http.DefaultTransport by any route — a nil field, an assignment, a
// helper that returns it — fails here.
func TestNewIsNotTheProcessDefault(t *testing.T) {
	tr := New()

	if tr == nil {
		t.Fatal("New returned nil; a nil transport on an http.Client IS http.DefaultTransport")
	}
	if http.RoundTripper(tr) == http.DefaultTransport {
		t.Fatal("New returned http.DefaultTransport itself; the point of this package is not to be it")
	}
	if def, ok := http.DefaultTransport.(*http.Transport); ok && tr == def {
		t.Fatal("New returned the same *http.Transport as http.DefaultTransport")
	}
	if other := New(); tr == other {
		t.Fatal("two calls to New returned the same transport; a transport is a connection pool, " +
			"and the package contract is that each call returns a fresh one")
	}
}

// Proxy is nil, so no environment proxy setting can redirect this traffic.
//
// The counterweight in the same test is what gives the assertion meaning: the process
// default DOES resolve the environment, so nil here is a deliberate difference rather
// than a value that happened to match.
//
// This is asserted on the field rather than driven through a request on purpose.
// http.ProxyFromEnvironment never proxies a loopback address, so a request to an
// httptest server is unproxied whatever the environment says — a behavioural arm built
// that way would pass with the environment proxy restored, and prove nothing.
func TestProxyIsDisabled(t *testing.T) {
	if got := New().Proxy; got != nil {
		t.Fatal("Proxy must be nil: these clients dial in-cluster addresses, and an " +
			"operator-set HTTP_PROXY/HTTPS_PROXY must not carry token traffic off-cluster")
	}
	def, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("http.DefaultTransport is not an *http.Transport; this test's comparison is void")
	}
	if def.Proxy == nil {
		t.Fatal("http.DefaultTransport.Proxy is nil, so nil Proxy is no longer a difference " +
			"from the default and this test no longer asserts anything")
	}
}

// Every timeout and pool bound is stated, with the value it is stated as.
//
// A zero http.Transport is NOT a smaller version of http.DefaultTransport: it has no
// dial deadline, no TLS handshake deadline and no idle-connection expiry. So escaping
// the process default by writing &http.Transport{} would trade a shared-global problem
// for a no-timeouts one, and these assertions are what stops that.
func TestTimeoutsAndPoolBoundsAreExplicit(t *testing.T) {
	tr := New()

	if tr.DialContext == nil {
		t.Fatal("DialContext is nil, so dials run through a ZERO net.Dialer, which has no timeout")
	}
	// The dial deadline itself is invisible on the built transport — http.Transport
	// exposes only the func — so it is asserted on the dialer New composes with.
	d := dialer()
	if d.Timeout != DialTimeout {
		t.Fatalf("dial timeout = %v, want %v", d.Timeout, DialTimeout)
	}
	if d.KeepAlive != KeepAlive {
		t.Fatalf("dial keep-alive = %v, want %v", d.KeepAlive, KeepAlive)
	}

	for _, c := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"TLSHandshakeTimeout", tr.TLSHandshakeTimeout, 10 * time.Second},
		{"ResponseHeaderTimeout", tr.ResponseHeaderTimeout, 30 * time.Second},
		{"ExpectContinueTimeout", tr.ExpectContinueTimeout, 1 * time.Second},
		{"IdleConnTimeout", tr.IdleConnTimeout, 90 * time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
		if c.got == 0 {
			t.Errorf("%s is unset, which means no deadline at all", c.name)
		}
	}

	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 16 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 16", tr.MaxIdleConnsPerHost)
	}
	// The per-host cap is the reason the pool is worth owning at all: unset, it is
	// http.DefaultMaxIdleConnsPerHost, which is 2.
	if tr.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, which is no better than the standard library's %d",
			tr.MaxIdleConnsPerHost, http.DefaultMaxIdleConnsPerHost)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false; http.DefaultTransport sets it, and a transport that " +
			"drops it silently loses HTTP/2 to peers that offer it")
	}
}

// The counterweight to every assertion above: a transport that carries no traffic would
// satisfy all of them. Plain HTTP and TLS both work, and a second request reuses the
// first request's connection — which is the pooling the sizing above exists to serve.
func TestRequestsSucceedOverPlainHTTPAndTLS(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  bool
	}{{"plain", false}, {"tls", true}} {
		t.Run(tc.name, func(t *testing.T) {
			conns := &connCounter{}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "pong")
			})

			var srv *httptest.Server
			if tc.tls {
				srv = httptest.NewUnstartedServer(h)
				srv.Config.ConnState = conns.note
				srv.StartTLS()
			} else {
				srv = httptest.NewUnstartedServer(h)
				srv.Config.ConnState = conns.note
				srv.Start()
			}
			defer srv.Close()

			tr := New()
			if tc.tls {
				pool := x509.NewCertPool()
				pool.AddCert(srv.Certificate())
				tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			}
			client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
			defer tr.CloseIdleConnections()

			for i := 0; i < 2; i++ {
				resp, err := client.Get(srv.URL)
				if err != nil {
					t.Fatalf("request %d: %v", i+1, err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatalf("request %d: read body: %v", i+1, err)
				}
				if resp.StatusCode != http.StatusOK || string(body) != "pong" {
					t.Fatalf("request %d: got %d %q, want 200 \"pong\"", i+1, resp.StatusCode, body)
				}
				if tc.tls && resp.TLS == nil {
					t.Fatalf("request %d: response carries no TLS state, so the TLS arm proved nothing", i+1)
				}
			}

			if got := conns.opened(); got != 1 {
				t.Fatalf("the server accepted %d connections for two sequential requests; the second "+
					"must reuse the pooled connection", got)
			}
		})
	}
}

// connCounter counts accepted server connections, which is how the reuse assertion above
// tells a pooled second request from a re-dial.
type connCounter struct {
	mu sync.Mutex
	n  int
}

func (c *connCounter) note(_ net.Conn, state http.ConnState) {
	if state != http.StateNew {
		return
	}
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *connCounter) opened() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
