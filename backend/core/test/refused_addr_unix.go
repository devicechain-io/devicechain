// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package test

import "testing"

// RefusingAddr returns a loopback "host:port" that REFUSES connections and that no other
// socket can bind while the test runs: a socket bound to it and never listening, held until
// tb ends.
//
// It is the address for a test that needs a dial to fail with "connection refused". The
// alternative — bind a listener, read its port, close it — hands the test a port that is
// free for anything else on the machine to take before the dial, and a test that expects a
// refusal then connects to someone else's listener and fails (or, worse, passes against the
// wrong thing). The cluster fixture holds its refusing route addresses the same way.
func RefusingAddr(tb testing.TB) string {
	tb.Helper()
	r, err := holdRefusingAddr()
	if err != nil {
		tb.Fatalf("hold a refusing address: %v", err)
	}
	tb.Cleanup(r.close)
	return r.addr
}
