// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// meteredConsumer is a one-worker consumer over rl with the given wait budget, whose metrics are
// registered on reg — built through the real constructors.
func meteredConsumer(t *testing.T, dead messaging.MessageWriter, rl *core.TenantRateLimiter, budget time.Duration) (*DispatchConsumer, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "outbound-connectors"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, loopbackClient(), 5*time.Second)
	return NewDispatchConsumer(&fakeReader{}, dead, nil, testProducer(), e, rl, budget, nil, 1,
		NewDispatchMetrics(ms), core.NewReadPacer(nil, "test")), reg
}

// timedDispatch is an httpCall dispatch for acme to url carrying triggeredAt, stored by the broker
// at appended.
func timedDispatch(t *testing.T, url, key string, triggeredAt, appended time.Time, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	m := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", RuleID: "acme/p@1/r1",
		TriggeredAt: triggeredAt, IdempotencyKey: key,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: url}}, ack)
	m.AppendTime = appended
	return m
}

func fallbackCount(t *testing.T, reg *prometheus.Registry, source string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "devicechain_outboundconnectors_rate_clock_fallback_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetLabel()[0].GetValue() == source {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("no rate_clock_fallback_total{source=%q}", source)
	return 0
}

// Metering on trigger time does not let a flood through: 2000 dispatches triggered within one
// second are admitted as a live flood would be — the burst, the rate over that second, and the
// rate over the wait budget — and every other one is shed to the dead-letter subject.
func TestTheSinkShedsAFloodAsLive(t *testing.T) {
	const n, rate, burst = 2000, 10.0, 20
	const budget = time.Second
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	dead := &fakeWriter{}
	c, _ := meteredConsumer(t, dead, core.NewTenantRateLimiter(core.StaticCeiling(rate, burst)), budget)

	start := time.Now().Add(-time.Second)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * time.Second / n)
		c.handle(context.Background(), timedDispatch(t, srv.URL, fmt.Sprintf("k%04d", i), at, time.Time{}, &fakeAck{}))
	}
	admitted, shed := int(hits.Load()), len(dead.written())
	// burst + rate·1s at no wait, then up to rate·budget more that each waited inside the budget.
	lo, hi := burst+int(rate), burst+int(rate*(1+budget.Seconds()))+2
	if admitted < lo || admitted > hi {
		t.Fatalf("admitted %d, want %d..%d (burst + r·(1s + budget))", admitted, lo, hi)
	}
	if admitted+shed != n {
		t.Fatalf("admitted %d + shed %d != %d", admitted, shed, n)
	}
}

// The sink meters on the same time REACT did, chosen by the same reader: the stamp; the broker
// time when there is no stamp or the stamp is later than it; now when there is neither. Each
// fallback is counted under its own cause. The choice is shown by what it admits: a bucket whose one token was spent
// ten seconds ago sheds a dispatch metered ten seconds ago, and admits one metered now.
func TestSinkMeteringFallsBack(t *testing.T) {
	srv, hits := countingServer(t)
	dead := &fakeWriter{}
	c, reg := meteredConsumer(t, dead, core.NewTenantRateLimiter(core.StaticCeiling(1, 1)), 50*time.Millisecond)
	now := time.Now()

	// A stamped dispatch ten seconds ago spends the one token then.
	c.handle(context.Background(), timedDispatch(t, srv.URL, "a", now.Add(-10*time.Second), time.Time{}, &fakeAck{}))
	// No stamp: metered at its broker time, a tenth of a second later — no token yet: shed.
	c.handle(context.Background(), timedDispatch(t, srv.URL, "b", time.Time{}, now.Add(-9900*time.Millisecond), &fakeAck{}))
	// A stamp later than its broker time is capped at the broker time: shed too.
	c.handle(context.Background(), timedDispatch(t, srv.URL, "c", now, now.Add(-9800*time.Millisecond), &fakeAck{}))
	// Neither: metered now, ten seconds on — admitted.
	c.handle(context.Background(), timedDispatch(t, srv.URL, "d", time.Time{}, time.Time{}, &fakeAck{}))

	if got := hits.Load(); got != 2 {
		t.Fatalf("sent %d, want 2 (the stamped one and the one metered now)", got)
	}
	if got := len(dead.written()); got != 2 {
		t.Fatalf("shed %d, want 2 (the two metered at their broker time)", got)
	}
	if got := fallbackCount(t, reg, "append"); got != 1 {
		t.Errorf("append fallbacks = %v, want 1 (the missing stamp)", got)
	}
	if got := fallbackCount(t, reg, "capped"); got != 1 {
		t.Errorf("capped fallbacks = %v, want 1 (the stamp after the broker time)", got)
	}
	if got := fallbackCount(t, reg, "now"); got != 1 {
		t.Errorf("now fallbacks = %v, want 1", got)
	}
}
