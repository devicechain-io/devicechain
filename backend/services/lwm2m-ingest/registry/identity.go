// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/mux"
)

// authIdentity recovers the AUTHENTICATED DTLS PSK identity from a CoAP request's
// connection — the tenancy anchor (ADR-075 D1). On the server side pion stores the
// client's presented PSK identity into the connection state's IdentityHint once the PSK
// callback has authenticated it, and go-coap surfaces the underlying pion conn verbatim
// through mux.Conn.NetConn(). It returns the identity and whether it could be recovered;
// a false result MUST fail the request closed (a request on a connection that is not the
// DTLS-PSK conn we authenticated, or that carries no identity, has no trustworthy
// tenancy).
//
// This is a concrete-type assertion on a third-party connection, so it is deliberately
// pinned by a real-DTLS-handshake test (identity_integration_test) rather than trusted:
// if go-coap ever wraps the conn, that test reddens instead of tenancy silently breaking.
func authIdentity(conn mux.Conn) (string, bool) {
	dc, ok := conn.NetConn().(*piondtls.Conn)
	if !ok {
		return "", false
	}
	state, ok := dc.ConnectionState()
	if !ok {
		return "", false
	}
	if len(state.IdentityHint) == 0 {
		return "", false
	}
	return string(state.IdentityHint), true
}
