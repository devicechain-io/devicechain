// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestDispatchShedLetterBudget(t *testing.T) {
	logger := log.Logger
	log.Logger = zerolog.Nop()
	t.Cleanup(func() { log.Logger = logger })
	dead, index := &fakeWriter{}, &fakeWriter{}
	c := newIndexingConsumer(dead, index)
	now := time.Now().UTC()
	c.letters.now = func() time.Time { return now }
	c.rate = core.NewTenantRateLimiter(core.StaticCeiling(1, 0))
	c.waitBudget = time.Millisecond
	ms := &core.Microservice{FunctionalArea: "outbound-connectors"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	c.metrics = NewDispatchMetrics(ms)
	vec := c.metrics.dispatched
	shed := func(tenant string, seq int) {
		t.Helper()
		ack := &fakeAck{}
		msg := withDispatchOrigin(wireMsg(t, "inst."+tenant+".connector-dispatch", 1,
			&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindHTTPCall, Tenant: tenant,
				HTTPCall: &connectorwire.HTTPCallDispatch{URL: "https://example.com"}}, ack), uint64(seq))
		c.handle(context.Background(), msg)
		if !ack.acked {
			t.Fatalf("shed %d for %s was left unacked", seq, tenant)
		}
	}
	// REACT defaults allow 60 letters initially. The next 50 must all be acked and
	// counted, without spending global tokens on the tenant's refused letters.
	for i := 0; i < 60; i++ {
		shed("acme", i+1)
	}
	for i := 0; i < 50; i++ {
		if i == 45 {
			shed("quiet", 1000)
			if dead.tenants[len(dead.tenants)-1] != "quiet" {
				t.Fatal("tenant refusals consumed the quiet tenant's global allowance")
			}
		}
		shed("acme", i+61)
	}
	if got := testutil.ToFloat64(vec.WithLabelValues("httpCall", outcomeRateLimited)); got != 111 {
		t.Fatalf("rate_limited = %v, want all 111 dispatches", got)
	}
	if len(dead.written()) != 61 || len(index.written()) != 61 {
		t.Fatalf("burst copied/indexed %d/%d dispatches, want 61/61", len(dead.written()), len(index.written()))
	}
	if got := testutil.ToFloat64(c.metrics.unlettered.WithLabelValues("httpCall", outcomeRateLimited)); got != 50 {
		t.Fatalf("unlettered = %v, want 50", got)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDispatchSummary(t, index, 61, "httpCall/rate_limited=50")
	_ = c.Stop(context.Background())
	if len(index.written()) != 62 || len(dead.written()) != 61 {
		t.Fatal("Stop duplicated the summary or copied an unlettered payload")
	}
}

func assertDispatchSummary(t *testing.T, index *fakeWriter, offset int, detail string) {
	t.Helper()
	messages := index.written()
	if len(messages) != offset+1 {
		t.Fatalf("index has %d entries, want %d including one summary", len(messages), offset+1)
	}
	e, err := deadletter.Unmarshal(messages[offset].Value)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != deadletter.KindConnectorDispatch || !strings.Contains(e.Detail, detail) ||
		!strings.Contains(e.Detail, "window=") || len(e.Payload) != 0 || e.Sequence != 0 {
		t.Fatalf("unexpected summary: %+v", e)
	}
}

func TestTerminalLetterBudgetAndPeriodicSummary(t *testing.T) {
	var logs bytes.Buffer
	logger := log.Logger
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() { log.Logger = logger })
	dead, index := &fakeWriter{}, &fakeWriter{}
	c := newIndexingConsumer(dead, index)
	now := time.Now().UTC()
	c.letters.now = func() time.Time { return now }
	c.metrics = NewDispatchMetrics(&core.Microservice{FunctionalArea: "outbound-connectors"})
	for i := 0; i < 110; i++ {
		ack := &fakeAck{}
		c.handle(context.Background(), withDispatchOrigin(wireMsg(t, dispatchSubject, 1,
			&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindPublish, Tenant: "acme",
				Publish: &connectorwire.PublishDispatch{ConnectorRef: "unsupported"}}, ack), uint64(i+1)))
		if !ack.acked {
			t.Fatalf("terminal dispatch %d left unacked", i)
		}
	}
	if len(dead.written()) != 60 || len(index.written()) != 60 ||
		strings.Count(logs.String(), "Connector dispatch is terminally undeliverable") != 60 {
		t.Fatal("terminal copy, index or Error log escaped the shared budget")
	}
	if got := testutil.ToFloat64(c.metrics.dispatched.WithLabelValues("publish", outcomeUnsupported)); got != 110 {
		t.Fatalf("unsupported count = %v", got)
	}
	if got := testutil.ToFloat64(c.metrics.unlettered.WithLabelValues("publish", outcomeUnsupported)); got != 50 {
		t.Fatalf("unlettered terminal count = %v", got)
	}
	c.letters.window = 5 * time.Millisecond
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	deadline := time.Now().Add(time.Second)
	for len(index.written()) == 60 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = c.Stop(context.Background())
	assertDispatchSummary(t, index, 60, "publish/unsupported=50")
}

func TestDispatchLetterGlobalBudgetAndRefill(t *testing.T) {
	s := newDispatchLetterBudget(testProducer(), nil)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		if !s.admit(fmt.Sprintf("tenant-%d", i), "httpCall", outcomeRateLimited) {
			t.Fatalf("global budget refused letter %d", i)
		}
	}
	if s.admit("quiet", "httpCall", outcomeRateLimited) {
		t.Fatal("global budget admitted a 101st letter")
	}
	now = now.Add(time.Second)
	if !s.admit("quiet", "httpCall", outcomeRateLimited) {
		t.Fatal("global allowance did not refill")
	}
}
