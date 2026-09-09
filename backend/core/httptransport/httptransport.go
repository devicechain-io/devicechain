// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package httptransport builds the *http.Transport that core's platform-internal HTTP
// clients dial through: the JWKS fetch, the service-token mint and cross-service
// GraphQL calls, and the user-identity login/refresh sessions.
//
// It exists because leaving http.Client.Transport nil does not mean "no transport" — it
// means http.DefaultTransport, a process-wide singleton, and that singleton carries two
// properties this traffic should not inherit.
//
//   - Proxy. http.DefaultTransport resolves HTTP_PROXY/HTTPS_PROXY/NO_PROXY from the
//     environment, and the Helm chart renders per-functional-area environment variables.
//     A proxy set there would carry the service-token mint and every bearer-bearing
//     cross-service query to an operator-supplied intermediary, with nothing in any log
//     to say so. See the Proxy note on New.
//   - The connection pool. One pool, shared by every user of the default transport in
//     the process, with MaxIdleConnsPerHost = 2. A burst anywhere in the process evicts
//     the idle connections authentication was about to reuse, so a subsystem that has
//     nothing to do with auth can make auth re-dial. Anything that wraps or replaces
//     http.DefaultTransport — a tracing library, a test helper — reconfigures how auth
//     dials, at a distance.
//
// # How to use it
//
// New returns a NEW transport each call, and a transport IS a connection pool: call it
// once per package and share the result across that package's clients, rather than once
// per client. One pool per package is deliberate — it keeps the pool shared among
// clients that dial the same peers for the same reason, while a pool problem in one of
// them still cannot reach another.
//
//	var sharedTransport = httptransport.New()
//	...
//	c := &http.Client{Timeout: requestTimeout, Transport: sharedTransport}
//
// # What this package is not
//
// It is not an egress boundary. It applies no address policy, so it must never be used
// for a URL a tenant supplied; egress.Guard.Transport builds a guarded transport for
// that, and httpsink dials through one. The two overlap in the Proxy answer and in
// nothing else: this transport dials private ClusterIPs on purpose.
//
// And it is never installed on http.DefaultTransport. Replacing the process default
// would put these settings on every library in the process that never asked for them,
// which is the same action-at-a-distance the package exists to remove.
package httptransport

import (
	"net"
	"net/http"
	"time"
)

// Timeouts and pool sizes for a platform-internal transport. Every one of these is
// stated rather than defaulted: an http.Transport zero value has NO dial timeout, no
// TLS handshake timeout and no idle-connection expiry, so a transport written as
// &http.Transport{} to escape the process default trades a shared-global problem for a
// no-timeouts one.
const (
	// DialTimeout bounds establishing a TCP connection. The peers here are in-cluster
	// Services that connect in milliseconds, and the clients built on this transport
	// carry a 10s overall timeout, so a longer dial deadline could never fire on its
	// own. Shorter than the client timeout by design: a dial that is going to fail
	// should report the connect failure rather than surface as the whole request
	// deadline expiring.
	DialTimeout = 5 * time.Second

	// KeepAlive is the TCP keep-alive probe interval, as in http.DefaultTransport. It
	// lets a connection idling in the pool notice a peer that went away (a rescheduled
	// pod) rather than being handed to a request that then fails.
	KeepAlive = 30 * time.Second

	// TLSHandshakeTimeout bounds the TLS handshake, as in http.DefaultTransport.
	TLSHandshakeTimeout = 10 * time.Second

	// ResponseHeaderTimeout bounds how long a peer may hold a connection after
	// accepting it without sending response headers. http.DefaultTransport leaves this
	// unset and relies on the client's Timeout, which is exactly the case that fails
	// here: userclient accepts a caller-supplied *http.Client, and one built with
	// Timeout 0 would otherwise have no deadline at all. None of these clients read a
	// streaming or long-polled response, so bounding the headers is safe.
	ResponseHeaderTimeout = 30 * time.Second

	// ExpectContinueTimeout matches http.DefaultTransport.
	ExpectContinueTimeout = 1 * time.Second

	// IdleConnTimeout expires a pooled connection, as in http.DefaultTransport.
	IdleConnTimeout = 90 * time.Second

	// MaxIdleConns caps pooled connections across all hosts, as in
	// http.DefaultTransport.
	MaxIdleConns = 100

	// MaxIdleConnsPerHost is the one sizing that is NOT the standard library's. The
	// default is 2 (http.DefaultMaxIdleConnsPerHost), and these clients concentrate on
	// a very small number of hosts — one mint endpoint plus a peer or two — so 2 is the
	// binding constraint: a burst of concurrent cross-service checks re-dials instead
	// of reusing. Idle connections only exist after real traffic and expire after
	// IdleConnTimeout, so the ceiling costs nothing when the traffic is not there.
	MaxIdleConnsPerHost = 16
)

// New returns a transport for core's platform-internal HTTP clients.
//
// 🔴 Proxy is nil, EXPLICITLY, and the choice is uniform across every client core
// builds. These clients dial in-cluster Service addresses to mint and present service
// and user tokens; an in-cluster address should never leave the cluster, and honouring
// the environment would make routing that traffic through a third party the DEFAULT,
// with NO_PROXY an opt-out an operator has to remember. Refusing the environment makes
// it an opt-in instead: a caller that genuinely must proxy — an out-of-cluster
// integration or dcctl behind a corporate proxy — passes its own *http.Client, which
// userclient already accepts, and states the choice at its own call site.
//
// Each call returns a fresh transport, and a transport is a connection pool. Call it
// once per package and share the result; see the package comment.
func New() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          MaxIdleConns,
		MaxIdleConnsPerHost:   MaxIdleConnsPerHost,
		IdleConnTimeout:       IdleConnTimeout,
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
		ExpectContinueTimeout: ExpectContinueTimeout,
	}
}

// dialer builds the net.Dialer New dials through. It is separate so a test can assert
// the dial deadline, which is the one timeout that is invisible on the built transport:
// http.Transport exposes only the DialContext func, and a func value cannot be compared
// or inspected.
//
// An http.Transport with a nil DialContext dials through a ZERO net.Dialer, which has
// no timeout at all — so "no DialContext" is not a smaller version of this, it is the
// absence of the deadline.
func dialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   DialTimeout,
		KeepAlive: KeepAlive,
	}
}
