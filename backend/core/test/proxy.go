// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// TCPProxy is a relay that can delay or discard traffic in the CLIENT→SERVER
// direction, so a test can keep a protocol message a client believes it has sent off
// the server for as long as it needs.
//
// # Why only one direction
//
// Server→client is always copied straight through, which keeps the connection healthy
// in exactly the way a client's own liveness checks measure it. That is the point: the
// arrangement under test is a client that believes it is connected and subscribed, not
// a broken link. Delaying both directions would produce a connection that looks sick,
// which is a different and much less interesting failure.
//
// # What it exists for
//
// nats.go's Subscribe family returns as soon as the SUB is in the connection's write
// buffer, so a component can report itself ready while the server has never heard of
// the subscription. That window is normally microseconds wide — real often enough to
// break CI, rare enough that a fix looks like it worked whether or not it did. Hold()
// makes it as wide as a test wants, which turns the failure into a measurement.
type TCPProxy struct {
	ln    net.Listener
	delay atomic.Int64 // nanoseconds applied per chunk, client→server
	drop  atomic.Bool  // discard client→server entirely
}

// StartTCPProxy listens on an ephemeral port and relays to target ("host:port"),
// passing everything through until told otherwise. It stops with the test.
func StartTCPProxy(t *testing.T, target string) *TCPProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &TCPProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				// Drop THIS connection and keep accepting. Returning here would kill
				// the accept loop while the listener stayed open, so every later dial
				// would complete its TCP handshake from the kernel backlog and then
				// hang unserviced until the client's own connect timeout — a failure
				// that looks like a broker problem rather than a dead proxy.
				_ = client.Close()
				continue
			}
			go p.pump(client, server)
			go func() {
				_, _ = io.Copy(client, server)
				_ = client.Close()
			}()
		}
	}()
	return p
}

// pump copies client→server, applying the current delay/drop before each write. Both
// are read per chunk rather than captured once, so a test can open and close the
// window on a connection that is already live.
func (p *TCPProxy) pump(from, to net.Conn) {
	defer func() { _ = to.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := from.Read(buf)
		if n > 0 && !p.drop.Load() {
			if d := time.Duration(p.delay.Load()); d > 0 {
				time.Sleep(d)
			}
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Addr is the proxy's own "host:port", to point a client at.
func (p *TCPProxy) Addr() string { return p.ln.Addr().String() }

// Hold delays each subsequent client→server chunk by d.
func (p *TCPProxy) Hold(d time.Duration) { p.delay.Store(int64(d)) }

// Drop discards client→server traffic. The socket stays open and the server keeps
// talking, so the client sees a live connection that has silently stopped being heard
// — which is what makes a round trip time out rather than fail fast.
func (p *TCPProxy) Drop() { p.drop.Store(true) }

// Release restores straight-through relaying in both directions.
func (p *TCPProxy) Release() {
	p.drop.Store(false)
	p.delay.Store(0)
}
