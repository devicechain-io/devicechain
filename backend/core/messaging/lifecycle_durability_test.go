// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// These tests drive the NatsManager through the REAL constructor and the REAL
// lifecycle — NewNatsManager, then Initialize (which connects by URL), then Start
// (which invokes oncreate) — against an embedded JetStream server.
//
// That is deliberately not what newTestManager does. It builds a NatsManager struct
// literal with the js context injected, so it exercises readers, writers and streams
// but never the connect path and never the create-on-Start phase. Every other broker
// test in this package routes through it, which is why neither has ever run under
// test despite six files talking to a real broker.
//
// The gap is not hypothetical. The ADR-030 capture-stream wiring bug reached a live
// cluster because a dependency was BUILT during Initialize and POPULATED during
// Start, so it was permanently nil — a phase error no test could see, because no
// test ran the phases.

// areaSeq makes every FunctionalArea in this file unique within the test binary.
//
// NewNatsManager registers stream metrics through promauto against the GLOBAL
// default registry, so constructing two managers with the same area panics with
// "duplicate metrics collector registration". A fixed area would therefore work
// under `go test` and panic under `-count=2`, which is the kind of test that passes
// until someone reruns it. It is also the reason every other broker test in this
// package builds a struct literal instead of calling the constructor.
var areaSeq atomic.Int64

// uniqueArea returns a per-run area name. The durability test needs the SAME area
// across its two managers (the durable name is built from it), so callers that care
// take one name and reuse it rather than calling this twice.
func uniqueArea(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, areaSeq.Add(1))
}

// startEmbeddedServer runs an in-process JetStream server for the life of the test.
func startEmbeddedServer(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

// testMicroservice builds the minimal Microservice a NatsManager reads, pointed at
// the embedded server. NatsUrl rebuilds the connection string from a host and a port
// rather than accepting a URL, so the server's address is split rather than passed
// through as ClientURL.
func testMicroservice(t *testing.T, srv *natsserver.Server, area string) *core.Microservice {
	t.Helper()
	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("embedded server client addr is %T, want *net.TCPAddr", srv.Addr())
	}
	ms := &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: area,
		Readiness:      core.NewReadinessGate(),
	}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = addr.IP.String()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(addr.Port)
	// Readers gate reads on auth being live; nothing here exercises auth, so open the
	// gate rather than let every read block on a validator this test never installs.
	ms.Readiness.MarkReady(nil)
	return ms
}

// TestTheRealLifecycleConnectsThenCreates pins WHICH lifecycle phase does what: that
// Initialize reaches the broker, and that oncreate runs on Start and not before.
//
// The negative half is the load-bearing half. Anything oncreate builds does not exist
// for the whole of the Initialize phase, so a service that captures it by value while
// building its sources captures a zero value — which is exactly the bug that shipped.
// Asserting the phase boundary turns that from a fact somebody has to remember into
// one the build enforces.
func TestTheRealLifecycleConnectsThenCreates(t *testing.T) {
	srv := startEmbeddedServer(t)
	created := 0
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("lifecycle-phase")),
		core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error {
			created++
			return nil
		})
	ctx := context.Background()

	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if nmgr.Conn() == nil {
		t.Fatal("Initialize reported success but never connected")
	}
	if created != 0 {
		t.Fatalf("oncreate ran %d times during Initialize, want 0: anything it builds "+
			"is absent for the whole Initialize phase, so a consumer that reads it "+
			"there silently gets a zero value", created)
	}

	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if created != 1 {
		t.Fatalf("oncreate ran %d times on Start, want 1", created)
	}
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
}

// TestTrafficPublishedWhileTheConsumerIsDownSurvives is the durability property the
// ADR-030 capture stream exists to provide, stated as a test: a durable consumer that
// goes away and comes back loses nothing published in the gap, and re-reads nothing
// it already acked.
//
// ExecuteStop says in prose that unsubscribing "does NOT delete the durable (that is
// the whole point of the Bind attach)". That sentence is the entire basis for the
// claim that a rollout does not drop telemetry, and nothing checked it. This is a
// property of the durable-consumer contract in core, not of event-sources, so proving
// it here covers every service that binds one.
func TestTrafficPublishedWhileTheConsumerIsDownSurvives(t *testing.T) {
	const (
		suffix = streams.InboundEvents // a declared, tenant-shaped stream
		tenant = "acme"
	)
	srv := startEmbeddedServer(t)
	ctx := core.WithTenant(context.Background(), tenant)

	// A producer that stays up for the whole test, standing in for the broker still
	// accepting device traffic while a consumer is being replaced.
	var writer MessageWriter
	producer := NewNatsManager(testMicroservice(t, srv, uniqueArea("durability-producer")),
		core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
			w, err := n.NewWriter(suffix)
			writer = w
			return err
		})
	if err := producer.Initialize(ctx); err != nil {
		t.Fatalf("producer initialize: %v", err)
	}
	if err := producer.Start(ctx); err != nil {
		t.Fatalf("producer start: %v", err)
	}
	t.Cleanup(func() { _ = producer.Stop(ctx) })

	// The first consumer, built and started the way a service main does.
	var reader MessageReader
	// Taken once and reused: the replacement must land on the SAME durable, and
	// DurableName is built from InstanceId + FunctionalArea + suffix.
	consumerArea := uniqueArea("durability-consumer")
	consumer := NewNatsManager(testMicroservice(t, srv, consumerArea),
		core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
			r, err := n.NewReader(suffix)
			reader = r
			return err
		})
	if err := consumer.Initialize(ctx); err != nil {
		t.Fatalf("consumer initialize: %v", err)
	}
	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("consumer start: %v", err)
	}

	write := func(body string) {
		t.Helper()
		if err := writer.WriteMessages(ctx, Message{Value: []byte(body)}); err != nil {
			t.Fatalf("write %q: %v", body, err)
		}
	}
	read := func(who string) string {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		m, err := reader.ReadMessage(rctx)
		if err != nil {
			t.Fatalf("%s read: %v", who, err)
		}
		reader.HandleResponse(nil)
		return string(m.Value)
	}

	write("before")
	if got := read("first consumer"); got != "before" {
		t.Fatalf("first consumer read %q, want %q", got, "before")
	}

	// The consumer goes down — the rollout / crash / outage window.
	if err := consumer.Stop(ctx); err != nil {
		t.Fatalf("consumer stop: %v", err)
	}

	// Traffic the broker accepts while nothing is consuming. Before the capture
	// stream this was exactly the traffic that vanished: acknowledged to the device
	// and stored nowhere.
	const gapCount = 5
	for i := 0; i < gapCount; i++ {
		write(fmt.Sprintf("during-%d", i))
	}

	// The replacement binds the SAME durable, the way a replacement pod does — which
	// requires the same InstanceId, FunctionalArea and suffix, since those three are
	// what DurableName is built from.
	//
	// It is a struct literal rather than a NewNatsManager call for one reason: the
	// constructor registers stream metrics into the global promauto registry, so a
	// second manager on this area would panic. ExecuteInitialize is still called, so
	// the replacement connects over the real path; only the metrics are skipped.
	// NewReader is called directly in place of Start, because the sampler Start would
	// spawn needs the metrics this manager has none of.
	replacement := &NatsManager{Microservice: testMicroservice(t, srv, consumerArea)}
	if err := replacement.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("replacement initialize: %v", err)
	}
	t.Cleanup(func() {
		if replacement.nc != nil {
			replacement.nc.Close()
		}
	})
	r, err := replacement.NewReader(suffix)
	if err != nil {
		t.Fatalf("replacement reader: %v", err)
	}
	reader = r

	// Everything published during the gap is delivered, in order, exactly once.
	for i := 0; i < gapCount; i++ {
		want := fmt.Sprintf("during-%d", i)
		if got := read("replacement"); got != want {
			t.Fatalf("replacement read %q, want %q: traffic published while the "+
				"consumer was down was lost, reordered, or the durable restarted "+
				"from the wrong position", got, want)
		}
	}

	// The message the first consumer already acked is NOT redelivered. Without this
	// the test would also pass on a consumer that simply replays the stream from the
	// beginning, which loses nothing but duplicates everything.
	write("after")
	if got := read("replacement"); got != "after" {
		t.Fatalf("replacement read %q, want %q: an already-acked message came back, "+
			"so the durable's position did not survive the restart", got, "after")
	}
}
