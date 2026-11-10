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
	h := NewHttpHandler(MustParseSchema(`schema { query: Query } type Query { hello: String! }`, &tracingQuery{}), nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	measure := func(handler http.HandlerFunc) float64 {
		return testing.AllocsPerRun(100, func() {
			r.Body = io.NopCloser(strings.NewReader(`{"query":"{ hello }"}`))
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hello":"hello"`) {
				t.Fatal("GraphQL execution behavior changed")
			}
		})
	}
	base, actual := measure(h.serveHTTP), measure(h.ServeHTTP)
	if actual != base {
		t.Fatalf("GraphQL tracing adds %g allocations", actual-base)
	}
}
