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

// The counterweight, and the lifetime the stored context gives a constructor called
// after start. A gate that proved only the early return would pass on an ensureStream
// that did nothing, so this pins that an UNCANCELLED start still provisions the stream
// fully, that a lazy constructor after start (a writer, and a reader with its durable)
// still works under the same live context, and that once ExecuteStop has cancelled it a
// late constructor or a consumer re-bind fails with the cancellation, promptly, rather
// than spending a retry budget or a request timeout on a manager that is going away.
func TestRunContextLivesFromStartUntilStop(t *testing.T) {
	srv := runTestServer(t)
	area := uniqueArea("ensure-ctx-live")
	nmgr := NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(m *NatsManager) error {
			_, err := m.NewWriter(streams.FailedEvents)
			return err
		})
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { nmgr.nc.Close() })

	if err := nmgr.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("an uncancelled start must provision its streams: %v", err)
	}
	instance := nmgr.Microservice.InstanceId
	if _, err := nmgr.js.StreamInfo(StreamName(instance, streams.FailedEvents)); err != nil {
		t.Fatalf("the stream oncreate asked for does not exist after an uncancelled start: %v", err)
	}

	// After start, before stop: a lazily built writer and reader provision under the live
	// run context.
	if _, err := nmgr.NewWriter(streams.EntityDeleted); err != nil {
		t.Fatalf("a writer built after start must still provision its stream: %v", err)
	}
	mr, err := nmgr.NewReader(streams.EntityDeleted)
	if err != nil {
		t.Fatalf("a reader built after start must still provision its stream and durable: %v", err)
	}
	durable := DurableName(instance, area, streams.EntityDeleted)
	if _, err := nmgr.js.ConsumerInfo(StreamName(instance, streams.EntityDeleted), durable); err != nil {
		t.Fatalf("the durable a reader built after start asked for does not exist: %v", err)
	}
	r, ok := mr.(*natsReader)
	if !ok {
		t.Fatalf("NewReader returned %T, want *natsReader", mr)
	}
	// The ack-floor durable has its own provisioning path (bindAckFloorLocked).
	mf, err := nmgr.NewReader(streams.FailedEvents,
		ReaderWithAckFloor(func(context.Context) (uint64, error) { return 1, nil }))
	if err != nil {
		t.Fatalf("an ack-floor reader built after start must still provision its durable: %v", err)
	}
	floor, ok := mf.(*natsReader)
	if !ok {
		t.Fatalf("NewReader returned %T, want *natsReader", mf)
	}

	if err := nmgr.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	bounded := func(what string, call func() error) {
		t.Helper()
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- call() }()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s after ExecuteStop must fail with the stop's cancellation; got %v", what, err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("%s after ExecuteStop took %s; the cancellation must end it at once", what, elapsed)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s after ExecuteStop did not return within 10s: nothing cancelled it", what)
		}
	}
	bounded("a writer built", func() error {
		_, err := nmgr.NewWriter(streams.DeadLetters)
		return err
	})
	bounded("a reader re-bind", r.bind)
	bounded("an ack-floor reader re-bind", floor.bind)
}

// lwm2m-ingest and sparkplug-ingest build their writers on the INITIALIZE path, before
// Start, because oncreate does not run until Start. So the initialize context must reach
// stream provisioning too: a shutdown during that initialize cancels it, and the writer
// it was building must stop retrying rather than ride context.Background(). The
// counterweight is the second half: a live initialize context still provisions.
func TestInitializeContextReachesProvisioningBeforeStart(t *testing.T) {
	srv := runTestServer(t)
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("ensure-ctx-init")), core.NewNoOpLifecycleCallbacks(),
		func(*NatsManager) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { nmgr.nc.Close() })

	if _, err := nmgr.NewWriter(streams.FailedEvents); err != nil {
		t.Fatalf("a writer built under a live initialize context must provision its stream: %v", err)
	}

	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := nmgr.NewWriter(streams.EntityDeleted)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a writer built after the initialize context was cancelled must fail with "+
				"the cancellation; got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a writer built after the initialize context was cancelled did not return within 10s: " +
			"stream provisioning before Start is not running under the initialize context")
	}
}
