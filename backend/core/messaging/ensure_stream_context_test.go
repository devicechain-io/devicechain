// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// ensureStream used to retry under context.Background(), so a start the caller had
// given up on (shutdown, a cancelled start context) still spent the whole retry budget
// - about a minute - against a broker that would not provision the stream. These pin
// that the context ExecuteStart is handed reaches the stream provisioning oncreate
// triggers, and that cancelling it ends the retry at once.

// jetStreamlessConn returns a JetStream context over a server with JetStream switched
// off: every stream API call fails at once with no responders, which is the retryable
// "broker is up but will not provision" state ensureStream backs off on.
func jetStreamlessConn(t *testing.T) nats.JetStreamContext {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return js
}

func TestExecuteStartCancellationAbortsStreamProvisioning(t *testing.T) {
	srv := runTestServer(t)
	var created error
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("ensure-ctx")), core.NewNoOpLifecycleCallbacks(),
		func(m *NatsManager) error {
			_, created = m.NewWriter(streams.FailedEvents)
			return created
		})
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(nmgr.nc.Close)
	nmgr.js = jetStreamlessConn(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- nmgr.ExecuteStart(ctx) }()

	// Let the first attempt fail and the retry back off, then give up on the start.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled start must surface the cancellation; got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStart kept retrying stream creation after its context was cancelled; " +
			"ensureStream is not running under the caller's context")
	}
}
