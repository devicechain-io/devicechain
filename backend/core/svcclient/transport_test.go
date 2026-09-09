// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"net/http"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// A Client mints and queries over this package's own transport, not the process default.
//
// This traffic is the service-token mint and every cross-service query that carries the
// minted bearer, so the transport it runs on decides both where that token can be sent
// and whose connection pool the calls compete for.
func TestClientDoesNotUseTheProcessDefaultTransport(t *testing.T) {
	c := New(config.UserManagementConfiguration{Hostname: "user-management", Port: 8080},
		"secret", "test", nil)

	if c.http.Transport == nil {
		t.Fatal("Transport is nil, which resolves to http.DefaultTransport: the mint and every " +
			"bearer-carrying query would honour the environment's proxy settings and share the " +
			"process-wide connection pool")
	}
	if c.http.Transport == http.DefaultTransport {
		t.Fatal("Transport is http.DefaultTransport")
	}
	if c.http.Timeout != requestTimeout {
		t.Fatalf("client timeout = %v, want %v", c.http.Timeout, requestTimeout)
	}

	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is a %T, not an *http.Transport", c.http.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("Proxy is set: the mint endpoint and every peer are in-cluster addresses and " +
			"must not be reached through an operator-supplied proxy")
	}

	// Several Clients per process is the normal case (one per calling subject), and they
	// all mint from the same endpoint, so they must share the pool rather than each
	// opening their own.
	other := New(config.UserManagementConfiguration{Hostname: "user-management", Port: 8080},
		"secret", "other", nil)
	if other.http.Transport != c.http.Transport {
		t.Fatal("two Clients carry different transports; a transport is a connection pool and " +
			"this package must share one")
	}
}
