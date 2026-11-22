// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A client that opens a connection to the control server and never finishes its request headers
// is cut off after the read-header bound, through the server main builds, not held forever.
func TestTheControlServerClosesASlowHeaderConnection(t *testing.T) {
	srv := newControlHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("the control server has no read-header or idle timeout: %+v", srv)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")); err != nil { // headers never end
		t.Fatal(err)
	}
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(controlReadHeaderTimeout + 10*time.Second))
	_, err = io.ReadAll(conn)
	if waited := time.Since(start); waited > controlReadHeaderTimeout+5*time.Second {
		t.Fatalf("the connection was held for %s (err %v), past the %s read-header bound", waited, err, controlReadHeaderTimeout)
	}
}
