// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"net"
	"testing"
)

// UseFreeTCPPort sets port to an unused, positive TCP port and restores it at cleanup.
// Call after initialization and before starting the server, and register server cleanup
// afterwards so the server stops before the port setting is restored.
//
// This listen-and-close workaround is for servers whose test seam cannot use zero
// (GraphQL treats zero as 8080). The port is not reserved after the listener closes;
// servers that support binding port zero directly should prefer that instead.
func UseFreeTCPPort(t testing.TB, port *int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free TCP port: %v", err)
	}
	freePort := int32(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatalf("close the TCP port probe: %v", err)
	}
	previous := *port
	*port = freePort
	t.Cleanup(func() { *port = previous })
}
