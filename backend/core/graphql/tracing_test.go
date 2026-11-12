// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type tracingQuery struct{}

func (*tracingQuery) Hello() string { return "hello" }

func TestGraphQLRequestProducesNamedSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(tracing.Install(tp))
	h := NewHttpHandler(MustParseSchema(`schema { query: Query } type Query { hello: String! }`, &tracingQuery{}), nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query ReadHello { hello }","operationName":"ReadHello","variables":{"secret":"do not record"}}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hello":"hello"`) {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "ReadHello" {
		t.Fatalf("missing named GraphQL span: %+v", spans)
	}
	for _, a := range spans[0].Attributes {
		if string(a.Key) != "transport" && string(a.Key) != "http.response.status_code" {
			t.Fatalf("unexpected GraphQL attribute: %v", a)
		}
	}
	_ = tp.Shutdown(context.Background())
}

func TestDisabledGraphQLTracingAddsZeroAllocations(t *testing.T) {
	if tracing.Enabled() {
		t.Fatal("allocation comparison requires disabled tracing")
	}
	h := NewHttpHandler(MustParseSchema(`schema { query: Query } type Query { hello: String! }`, &tracingQuery{}), nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request := func(handler http.HandlerFunc) {
		r.Body = io.NopCloser(strings.NewReader(`{"query":"{ hello }"}`))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hello":"hello"`) {
			t.Fatal("GraphQL execution behavior changed")
		}
	}
	// Warm schema execution and lazy initialization on both paths before measuring.
	request(h.serveHTTP)
	request(h.ServeHTTP)
	measure := func(handler http.HandlerFunc) float64 {
		return testing.AllocsPerRun(1000, func() {
			request(handler)
		})
	}
	// Pool misses and runtime activity can inflate individual samples, especially
	// under -race. Amortize them over many requests and compare the minimum of
	// several trials in the same process, rather than pinning an absolute count.
	least := func(handler http.HandlerFunc) float64 {
		best := measure(handler)
		for i := 1; i < 10; i++ {
			if v := measure(handler); v < best {
				best = v
			}
		}
		return best
	}
	base, actual := least(h.serveHTTP), least(h.ServeHTTP)
	if actual != base {
		t.Fatalf("disabled GraphQL tracing adds %g allocations per request (bare=%g, wrapped=%g)", actual-base, base, actual)
	}
}
