// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-outbound-connectors/config"
	"github.com/devicechain-io/dc-outbound-connectors/processor"
)

// receiptEndpoint answers at once and records when each idempotency key first arrived, per
// tenant (the tenant is the request path).
type receiptEndpoint struct {
	mu       sync.Mutex
	received map[string]time.Time
	byTenant map[string]int
}

func (e *receiptEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	e.mu.Lock()
	key := r.Header.Get("X-DC-Idempotency-Key")
	if _, seen := e.received[key]; !seen {
		e.received[key] = now
	}
	e.byTenant[strings.TrimPrefix(r.URL.Path, "/")]++
	e.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (e *receiptEndpoint) counts() (map[string]int, map[string]time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	byTenant := make(map[string]int, len(e.byTenant))
	for k, v := range e.byTenant {
		byTenant[k] = v
	}
	received := make(map[string]time.Time, len(e.received))
	for k, v := range e.received {
		received[k] = v
	}
	return byTenant, received
}

// rateLimited reads connector_dispatch_total{outcome="rate_limited"} summed over actions.
func rateLimited(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, mf := range mfs {
		if mf.GetName() != "devicechain_outboundconnectors_connector_dispatch_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == "rate_limited" {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

// A tenant draining a compliant backlog must neither be shed nor hold the workers other
// tenants need.
//
// 🔴 THE DEFECT WAS THE SINK'S CLOCK. The egress limiter charged every dispatch at ARRIVAL, so
// a backlog REACT had metered as compliant — 2000 actions spread over 200 seconds at a
// 10-a-second ceiling — reached the sink back to back and was charged at one instant: workers
// sat in the limiter's wait for up to its budget, most of the backlog was shed to the dead
// letter subject, and another tenant's dispatches queued behind those waits. The sink now
// meters the same trigger time REACT did, carried on the wire as triggeredAt, so a backlog
// that was compliant when it happened passes at drain speed.
//
// The wire messages are raw JSON, so the test builds against a tree that has no triggeredAt
// field at all; everything else is the service's own — the reader main wires, the real
// consumer and executor, a real core limiter, and an embedded JetStream.
func TestABacklogDrainDoesNotHoldOtherTenantsWorkers(t *testing.T) {
	testCtx, cancelTest := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelTest()

	const backlog, other = 2000, 10
	host, port := startEmbeddedNats(t)

	endpoint := &receiptEndpoint{received: map[string]time.Time{}, byTenant: map[string]int{}}
	srv := httptest.NewServer(endpoint)
	t.Cleanup(srv.Close)

	area := fmt.Sprintf("connectors-metering-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: area, FunctionalArea: "outbound-connectors"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	cfg := config.NewOutboundConnectorsConfiguration()
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
	producer := deadletter.NewProducer(ms)
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(producer))
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	guard := egress.NewGuard([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	executor := processor.NewExecutor(processor.NewSecretResolver(nil), nil, guard,
		time.Duration(cfg.SendTimeoutMs)*time.Millisecond)
	// 10 a second, with a burst of 20 to absorb the few positions the worker pool reorders.
	limiter := core.NewTenantRateLimiter(core.StaticCeiling(10, 20))
	consumer := processor.NewDispatchConsumer(reader, dead, nil, producer, executor, limiter,
		time.Duration(cfg.EgressWaitBudgetMs)*time.Millisecond, nil, cfg.MaxConcurrentSends,
		processor.NewDispatchMetrics(ms), core.NewReadPacer(nil, "test"))
	require.NoError(t, consumer.Start(context.Background()))
	t.Cleanup(func() { _ = consumer.Stop(context.Background()) })

	writer, err := nmgr.NewWriter(streams.ConnectorDispatch)
	require.NoError(t, err)
	publish := func(tenant, key string, triggeredAt time.Time) time.Time {
		body := fmt.Sprintf(`{"kind":"httpCall","tenant":%q,"deviceToken":"d1","ruleId":%q,`+
			`"occurredTime":%q,"triggeredAt":%q,"idempotencyKey":%q,"httpCall":{"url":%q}}`,
			tenant, tenant+"/p@1/r1", triggeredAt.UTC().Format(time.RFC3339Nano),
			triggeredAt.UTC().Format(time.RFC3339Nano), key, srv.URL+"/"+tenant)
		sent := time.Now()
		require.NoError(t, writer.WriteMessages(core.WithTenant(context.Background(), tenant),
			messaging.Message{Value: []byte(body)}))
		return sent
	}

	// Tenant A's backlog: triggered at 10 a second over the last 200 seconds. Tenant B's ten
	// dispatches are spread through it, each triggered now.
	start := time.Now().Add(-backlog * 100 * time.Millisecond)
	publishedB := map[string]time.Time{}
	for i := 0; i < backlog; i++ {
		publish("acme", fmt.Sprintf("a-%04d", i), start.Add(time.Duration(i)*100*time.Millisecond))
		if i%(backlog/other) == backlog/other-1 {
			key := fmt.Sprintf("b-%02d", len(publishedB))
			publishedB[key] = publish("beta", key, time.Now())
		}
	}

	// Done when every dispatch has been disposed of: delivered, or shed by the limiter.
	for {
		byTenant, _ := endpoint.counts()
		if float64(byTenant["acme"]+byTenant["beta"])+rateLimited(t, reg) >= backlog+other {
			break
		}
		select {
		case <-testCtx.Done():
			t.Fatalf("not every dispatch was disposed of before the deadline: delivered %v, shed %v",
				byTenant, rateLimited(t, reg))
		case <-time.After(50 * time.Millisecond):
		}
	}

	byTenant, received := endpoint.counts()
	var latencies []time.Duration
	for key, sent := range publishedB {
		if got, ok := received[key]; ok {
			latencies = append(latencies, got.Sub(sent))
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	shed := rateLimited(t, reg)
	t.Logf("delivered %v, shed %v, beta latencies %v", byTenant, shed, latencies)

	require.Zero(t, shed, "a compliant backlog was shed by the egress limiter")
	require.Equal(t, backlog, byTenant["acme"], "the compliant tenant's backlog was not fully delivered")
	require.Len(t, latencies, other, "the other tenant's dispatches were not all delivered")
	require.Less(t, latencies[len(latencies)-1], time.Second,
		"the other tenant waited behind the backlog's rate waits")
}
