// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-outbound-connectors/config"
	"github.com/devicechain-io/dc-outbound-connectors/processor"
)

// slowEndpoint is a webhook that takes a while to answer and counts every request it receives
// per idempotency key — the header every dispatch carries, so a duplicate send is visible as a
// key seen twice.
type slowEndpoint struct {
	delay time.Duration

	mu     sync.Mutex
	counts map[string]int
	total  int
}

func (e *slowEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	time.Sleep(e.delay)
	e.mu.Lock()
	e.counts[r.Header.Get("X-DC-Idempotency-Key")]++
	e.total++
	e.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (e *slowEndpoint) snapshot() (map[string]int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.counts))
	for k, v := range e.counts {
		out[k] = v
	}
	return out, e.total
}

// A burst of dispatches queued behind a slow endpoint must reach it ONCE each.
//
// 🔴 THE DEFECT IS THE READER FETCHING AHEAD OF ITS WORKERS. The broker starts a message's
// redelivery clock (AckWait) when it hands the message out, not when a worker picks it up. A
// reader that fetched a 64-message batch in front of a small pool held the tail of that batch in
// process for longer than the clock, the broker redelivered it while its first copy was still
// waiting, and both copies were sent — a duplicate outbound call for every message past the
// pool's reach, which an endpoint that ignores the idempotency key executes twice.
//
// Everything is the service's own: the reader comes from newDispatchReader (the function main
// wires) with MaxConcurrentSends from the configuration, the consumer is the real
// DispatchConsumer with its real pool and executor, and the broker is a real embedded
// JetStream, because the clock that expires is the broker's. The only substitutions are the
// AckWait (8s rather than 60s, through the manager's single seam) and an endpoint that takes
// 1s and counts. 8s is the smallest round AckWait that still leaves a send its full second
// once the send is capped to end sendMargin (5s) before the message's AckDeadline; a shorter one
// would cut every send off before it started and measure nothing.
//
// The assertion is on VALUES: every key was sent exactly once, and the total is the number
// published.
func TestDispatchBacklogIsSentOnce(t *testing.T) {
	testCtx, cancelTest := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancelTest()

	const dispatches = 60
	host, port := startEmbeddedNats(t)

	endpoint := &slowEndpoint{delay: time.Second, counts: map[string]int{}}
	srv := httptest.NewServer(endpoint)
	t.Cleanup(srv.Close)

	area := fmt.Sprintf("connectors-backlog-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: area, FunctionalArea: "outbound-connectors"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	cfg := config.NewOutboundConnectorsConfiguration()
	cfg.MaxConcurrentSends = 4
	require.NoError(t, cfg.Validate())

	var reader messaging.MessageReader
	var dead messaging.MessageWriter
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		var err error
		if dead, err = m.NewWriter(streams.ConnectorDispatchDead); err != nil {
			return err
		}
		reader, err = newDispatchReader(m, cfg)
		return err
	})
	nmgr.SetAckWaitForTesting(t, 8*time.Second)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NotNil(t, reader, "the manager's start did not build the connector-dispatch reader")

	// The executor goes out through an egress guard that allows loopback and nothing else, so the
	// test uses the same dial path production does.
	client := &http.Client{Transport: egress.NewGuard([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	}).Transport()}
	executor := processor.NewExecutor(processor.NewSecretResolver(nil), nil, client,
		time.Duration(cfg.SendTimeoutMs)*time.Millisecond)
	consumer := newTestDispatchConsumer(reader, dead, deadletter.NewProducer(ms), executor, cfg)
	require.NoError(t, consumer.Start(context.Background()))
	t.Cleanup(func() { _ = consumer.Stop(context.Background()) })

	// Published AFTER the durable exists: it is DeliverNew, so anything earlier is never read.
	writer, err := nmgr.NewWriter(streams.ConnectorDispatch)
	require.NoError(t, err)
	tenantCtx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < dispatches; i++ {
		body, err := connectorwire.MarshalConnectorDispatchRequest(&connectorwire.ConnectorDispatchRequest{
			Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", RuleID: "r1",
			IdempotencyKey: fmt.Sprintf("key-%03d", i), OccurredTime: time.Now(),
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL},
		})
		require.NoError(t, err)
		require.NoError(t, writer.WriteMessages(tenantCtx, messaging.Message{Value: body}))
	}

	// Wait until every dispatch has reached the endpoint at least once.
	for {
		counts, _ := endpoint.snapshot()
		if len(counts) == dispatches {
			break
		}
		select {
		case <-testCtx.Done():
			t.Fatalf("only %d of %d dispatches reached the endpoint before the test deadline", len(counts), dispatches)
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Then outlast one more AckWait, so a redelivery of anything still held has time to be sent
	// and counted rather than escaping the assertion by arriving late.
	select {
	case <-testCtx.Done():
		t.Fatal("the test deadline passed while waiting out the settle window")
	case <-time.After(9 * time.Second):
	}

	counts, total := endpoint.snapshot()
	var twice []string
	for key, n := range counts {
		if n != 1 {
			twice = append(twice, fmt.Sprintf("%s×%d", key, n))
		}
	}
	require.Empty(t, twice, "these dispatches reached the endpoint more than once: the reader held them "+
		"in process past AckWait, the broker redelivered them, and both copies were sent")
	require.Equal(t, dispatches, total, "total sends")
}

// newTestDispatchConsumer builds the consumer through its real constructor with the pool width
// the configuration names, egress rate limiting and the tenant gate off, and no metrics.
func newTestDispatchConsumer(reader messaging.MessageReader, dead messaging.MessageWriter,
	producer *deadletter.Producer, executor *processor.Executor,
	cfg *config.OutboundConnectorsConfiguration) *processor.DispatchConsumer {
	return processor.NewDispatchConsumer(reader, dead, nil, producer, executor, nil,
		time.Duration(cfg.EgressWaitBudgetMs)*time.Millisecond, nil, cfg.MaxConcurrentSends,
		nil, core.NewReadPacer(nil, "test"))
}
