// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package session holds the small primitives shared between the two things that track a
// device's live CoAP connection by authenticated identity: the Observe/Notify telemetry
// manager (package observe, L2b) and the downlink command conn table (package downlink,
// L4a). Both must answer "is this connection still alive?" identically, and the honest
// answer depends on a subtle go-coap teardown ordering — so it lives here, in one place,
// rather than in two copies that could drift.
package session

import "github.com/plgd-dev/go-coap/v3/mux"

// ConnDead reports whether a CoAP connection is closed (or nil). It is non-blocking.
//
// It checks BOTH Done() and Context().Done() because go-coap tears a session down in an
// order that leaves a window where only one of them has fired: Close() runs cancel(ctx)
// FIRST, then the AddOnClose callbacks, then close(Done()) LAST (dtls/server/session.go).
// So a connection dying between "commit an entry pointing at it" and "register its
// AddOnClose reap" registers the reap into an already-consumed callback list (it never
// fires) while Done() is still open — yet Context().Done() has already closed. Checking the
// context lets a caller catch that window (e.g. reap inline) instead of leaking an entry
// pinned to a dead connection until the next session event.
func ConnDead(c mux.Conn) bool {
	if c == nil {
		return true
	}
	select {
	case <-c.Done():
		return true
	default:
	}
	select {
	case <-c.Context().Done():
		return true
	default:
		return false
	}
}
