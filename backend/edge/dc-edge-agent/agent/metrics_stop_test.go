// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"net"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// stop must release the metrics port before it returns, even when the serve goroutine
// start launched has not yet run.
//
// http.Server.Shutdown closes only the listeners Serve has already registered, and start
// hands its listener to Serve in a goroutine. This test builds that window directly: the
// endpoint holds a bound listener and a built server, exactly as start leaves them on the
// line before its goroutine is scheduled, and Serve has never been given the listener.
// Racing start for the window instead would catch a regression only in the runs that win
// the race. The fields are set directly because that is the only way to hold the endpoint
// in that state without adding a seam to production code.
func TestEdgeMetricsStopReleasesAListenerServeHasNotTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()
	m := &edgeMetrics{srv: &http.Server{Handler: http.NotFoundHandler()}, ln: ln, addr: addr}

	// Positive control: the port really is held. Without it, a successful rebind below
	// could mean the fixture never held the port rather than that stop released it.
	if probe, err := net.Listen("tcp", addr); err == nil {
		probe.Close()
		t.Fatalf("rebinding %s succeeded BEFORE stop; the fixture does not hold the port", addr)
	}

	m.stop()

	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebinding %s after stop returned: %v; stop left the metrics listener bound "+
			"because Serve had not yet taken it, so a restart on the same port would fail", addr, err)
	}
	rebound.Close()
}

// The end-to-end companion of the test above: start and an immediate stop, through the
// real start, and a rebind of the same port, many times over. Whether a round lands in
// the pre-Serve window is up to the scheduler, so this cannot be the gate for that window;
// it is here because it goes through start and Serve the way Run does.
func TestEdgeMetricsStartThenImmediateStopReleasesThePort(t *testing.T) {
	healthz := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	for i := 0; i < 300; i++ {
		m := &edgeMetrics{reg: prometheus.NewRegistry()}
		if err := m.start("127.0.0.1:0", healthz); err != nil {
			t.Fatalf("round %d: start: %v", i, err)
		}
		addr := m.addr
		m.stop()
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("round %d: rebinding %s after stop returned: %v", i, addr, err)
		}
		ln.Close()
	}
}
