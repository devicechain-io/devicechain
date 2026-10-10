// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
)

// TestWithQueryTimeoutMovesOnlyTheQueryBound pins the split: the option re-bounds Query
// and leaves the mint at the default, both still on the package's shared transport.
func TestWithQueryTimeoutMovesOnlyTheQueryBound(t *testing.T) {
	c := New(config.UserManagementConfiguration{Hostname: "user-management", Port: 8080},
		"secret", "test", nil, WithQueryTimeout(3*time.Minute))
	if c.http.Timeout != 3*time.Minute {
		t.Fatalf("query timeout = %v, want 3m", c.http.Timeout)
	}
	if c.mintHTTP.Timeout != requestTimeout {
		t.Fatalf("mint timeout = %v, want the default %v", c.mintHTTP.Timeout, requestTimeout)
	}
	if c.http.Transport != sharedTransport || c.mintHTTP.Transport != sharedTransport {
		t.Fatal("a client built with WithQueryTimeout left the shared transport")
	}
}

// TestWithQueryTimeoutBoundsTheQuery checks the option by behaviour: a peer slower than
// the configured bound is cut off, one inside it answers.
func TestWithQueryTimeoutBoundsTheQuery(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d, err := time.ParseDuration(r.URL.Query().Get("delay")); err == nil {
			time.Sleep(d)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer target.Close()

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	c := New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)},
		"shh", "test", nil, WithQueryTimeout(200*time.Millisecond))

	if err := c.Query(context.Background(), target.URL+"?delay=10ms", "t", "query{x}", nil, nil); err != nil {
		t.Fatalf("a peer inside the bound failed: %v", err)
	}
	if err := c.Query(context.Background(), target.URL+"?delay=1s", "t", "query{x}", nil, nil); err == nil {
		t.Fatal("a peer slower than the bound was waited for")
	}
}

func TestWithQueryTimeoutRefusesNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithQueryTimeout(0) did not panic; http.Client reads 0 as no timeout at all")
		}
	}()
	WithQueryTimeout(0)
}

// The mint keeps the DEFAULT bound when the query bound is raised. Checked by behaviour,
// not by field: a mint routed through the long query client would still leave both
// fields set correctly. A hung user-management must fail the call at the mint's bound,
// typed as a token failure — not wait out a bound sized for a slow peer.
func TestWithQueryTimeoutLeavesTheMintOnTheDefaultBound(t *testing.T) {
	prev := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = prev })

	// The handler holds the mint until released. Released BEFORE the server closes (the
	// deferred order matters): Close waits for in-flight handlers, and a handler that has
	// not read its body is not told its client went away.
	release := make(chan struct{})
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	defer mint.Close()
	defer close(release)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the target was called although no token was minted")
	}))
	defer target.Close()

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	c := New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)},
		"shh", "test", nil, WithQueryTimeout(time.Minute))

	start := time.Now()
	err := c.Query(context.Background(), target.URL, "t", "query{x}", nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a hung mint did not fail the call")
	}
	if !errors.Is(err, ErrServiceToken) {
		t.Fatalf("a mint failure is not typed as one: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the mint waited %v: it ran on the query's 1m bound, not the default", elapsed)
	}
}
