// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"
	"testing"
)

// The JWKS fetch dials through this package's own transport, not the process default.
//
// The assertion is on the client the fetch actually builds — jwksHTTPClient is the one
// the fetch calls — rather than on what the constructor looks like, so a client that
// arrives at http.DefaultTransport by any route fails here.
func TestJWKSClientDoesNotUseTheProcessDefaultTransport(t *testing.T) {
	c := jwksHTTPClient()

	if c.Transport == nil {
		t.Fatal("Transport is nil, which resolves to http.DefaultTransport: the JWKS fetch would " +
			"honour the environment's proxy settings and share the process-wide connection pool")
	}
	if c.Transport == http.DefaultTransport {
		t.Fatal("Transport is http.DefaultTransport")
	}
	if c.Timeout != jwksRequestTimeout {
		t.Fatalf("client timeout = %v, want %v", c.Timeout, jwksRequestTimeout)
	}

	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is a %T, not an *http.Transport", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("Proxy is set: the JWKS endpoint is an in-cluster address and must not be " +
			"reached through an operator-supplied proxy")
	}
	// Every fetch must share one pool. A per-call transport would open a fresh
	// connection every time and leave the previous one idle until it expired.
	if other := jwksHTTPClient(); other.Transport != c.Transport {
		t.Fatal("two JWKS clients carry different transports; a transport is a connection pool " +
			"and this package must share one")
	}
}
