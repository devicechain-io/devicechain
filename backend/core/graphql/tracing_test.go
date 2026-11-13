// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
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
		return mallocsPerRequest(1000, func() {
			request(handler)
		})
	}
	// Under -race, sync.Pool drops a random share of what is put back, so the misses make
	// the per-request count a non-integer that moves by a few tenths between trials.
	// AllocsPerRun floors it, so with the mean near an integer each handler's floor fell on
	// either side of it independently: the test failed in both directions, and taking the
	// minimum of several trials could not help. So: unfloored means, trials interleaved
	// so drift over the run reaches both handlers alike.
	var base, actual float64
	for i := 0; i < 10; i++ {
		base += measure(h.serveHTTP)
		actual += measure(h.ServeHTTP)
	}
	base /= 10
	actual /= 10
	// An allocation the disabled path makes on every request adds about 1.0 (measured:
	// 0.998 without -race, 0.91 to 0.95 with it); the noise moves the difference by
	// about 0.1. Half an allocation separates the two.
	if actual-base >= 0.5 {
		t.Fatalf("disabled GraphQL tracing adds %.3f allocations per request (bare=%.3f, wrapped=%.3f)", actual-base, base, actual)
	}
}

func mallocsPerRequest(n int, f func()) float64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	f()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	before := stats.Mallocs
	for i := 0; i < n; i++ {
		f()
	}
	runtime.ReadMemStats(&stats)
	return float64(stats.Mallocs-before) / float64(n)
}
