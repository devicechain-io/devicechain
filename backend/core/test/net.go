// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"net"
	"testing"
)

// FreeTCPPort returns a TCP port nothing is listening on, by binding one and
// letting it go.
//
// It exists because an embedded nats-server's MQTT listener has to be given a
// FIXED port: unlike the client listener, the server exposes no accessor for an
// ephemerally-bound one, so a test that passes 0 has no way to learn what it got.
//
// 🔴 IT IS INHERENTLY RACY AND THERE IS NO NON-RACY VERSION. Between the close
// here and the server's bind, anything on the machine may take the port. Nothing
// in the standard library closes that window — handing the listener over is not
// possible either, because the server opens its own. A collision surfaces as a
// bind failure at startup, which is loud, so the failure mode is a flaky test
// rather than a silently wrong one.
func FreeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release the probe listener: %v", err)
	}
	return port
}
