// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// helloHandler answers anything with 200, so these tests measure the SERVER's bounds
// rather than a handler's behaviour.
func helloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// A caller that overrides the header bound gets the bound it asked for, over a real
// connection.
//
// The constant this package applies by default was chosen for management surfaces, and
// a device-facing listener now shares it. Overriding it is only meaningful if the value
// actually reaches the listener, which a struct field on its own does not prove.
func TestHttpServerOptionsApplyTheConfiguredHeaderBound(t *testing.T) {
	srv := NewHttpServerForHandlerWithOptions(0, helloHandler(), HttpServerOptions{
		ReadHeaderTimeout: 300 * time.Millisecond,
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dialling the listener: %v", err)
	}
	defer conn.Close()

	// Headers begun and never finished.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: example\r\n")); err != nil {
		t.Fatalf("writing partial headers: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("setting a read deadline: %v", err)
	}
	started := time.Now()
	_, err = conn.Read(make([]byte, 256))
	elapsed := time.Since(started)

	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("the server still held the connection after %v; the configured bound never reached it", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the connection was closed after %v, which is the package default rather than the configured 300ms", elapsed)
	}
}

// The counterweight: a listener that closed every connection would pass the test above.
// A prompt request must still be served, and the plain constructor must still behave as
// it always has — a header bound applied, and no whole-request bound, which the GraphQL
// subscription endpoint depends on.
func TestHttpServerDefaultsAreUnchangedAndStillServe(t *testing.T) {
	srv := NewHttpServerForHandler(0, helloHandler())
	if got := srv.server.ReadHeaderTimeout; got != httpReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want the package default %v", got, httpReadHeaderTimeout)
	}
	if got := srv.server.ReadTimeout; got != 0 {
		t.Errorf("ReadTimeout = %v, want it unset: a whole-request bound would sever a hijacked subscription", got)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	resp, err := http.Get("http://" + srv.Addr() + "/")
	if err != nil {
		t.Fatalf("GET on the live listener: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET = %d, want 200", resp.StatusCode)
	}
}

// The connection hooks are wired to net/http rather than merely stored: a request must
// arrive carrying the connection its context was given, and the state hook must report
// that connection's close. Without both, a caller cannot tell a connection that served a
// request from one that was cut off before making one.
func TestHttpServerOptionsWireTheConnectionHooks(t *testing.T) {
	type ctxKey struct{}
	sawConn := make(chan bool, 1)
	closed := make(chan struct{}, 1)

	srv := NewHttpServerForHandlerWithOptions(0, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 🔴 Reported through a channel, never asserted here: this runs on the server's
		// own goroutine, where a failed assertion would unwind that goroutine alone and
		// the test would pass regardless.
		_, ok := r.Context().Value(ctxKey{}).(net.Conn)
		sawConn <- ok
		w.WriteHeader(http.StatusOK)
	}), HttpServerOptions{
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, ctxKey{}, c)
		},
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				select {
				case closed <- struct{}{}:
				default:
				}
			}
		},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + srv.Addr() + "/")
	if err != nil {
		t.Fatalf("GET on the live listener: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	client.CloseIdleConnections()

	select {
	case ok := <-sawConn:
		if !ok {
			t.Error("the request carried no connection; ConnContext was not applied")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("the state hook never reported the connection closing; ConnState was not applied")
	}
}
