// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"net"
	"testing"
	"time"
)

// A sim run against an ingress where nothing listens must surface as an error, not as the
// "shedding" warning a governed run gets: refused connects are the ingress being
// unreachable. They are counted apart from sheds and backpressure and set the status
// lastError; the shed signal stays quiet.
func TestADeadIngressSurfacesAsAnErrorNotAShed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	rt := ingressRuntime(addr, 4)
	rt.Load = Load{EmitInterval: 20 * time.Millisecond}
	lc := NewLifecycle(&emittingSim{}, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = lc.Stop() }()

	waitFor(t, func() bool {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		return lc.lastError == "ingress unreachable (connection refused)"
	}, "a sim against a dead ingress never set lastError")
	if n := rt.Stats.ConnectRefused.Load(); n == 0 {
		t.Error("no refused connects counted")
	}
	if s, b := rt.Stats.Shed.Load(), rt.Stats.Backpressured.Load(); s != 0 || b != 0 {
		t.Errorf("shed %d, backpressured %d: an unreachable ingress is neither", s, b)
	}
	if shed := rt.Stats.LastTickShed.Load(); shed != 0 {
		t.Errorf("LastTickShed = %d: refused connects must not raise the shedding signal", shed)
	}
}
