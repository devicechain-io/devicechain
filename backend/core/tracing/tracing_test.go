// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestDisabledInstallsNothing(t *testing.T) {
	old := otel.GetTracerProvider()
	tp := noop.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(old) })
	beforeProp := otel.GetTextMapPropagator()
	before := runtime.NumGoroutine()
	shutdown, err := Setup(context.Background(), config.TracingConfiguration{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() != tp || !reflect.DeepEqual(otel.GetTextMapPropagator(), beforeProp) || Enabled() {
		t.Fatal("disabled setup modified global tracing state")
	}
	if after := runtime.NumGoroutine(); after != before {
		t.Fatalf("goroutines: before %d after %d", before, after)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, span := Start(context.Background(), "unused", "http", "t", trace.SpanKindServer)
	if span != nil || ctx != context.Background() {
		t.Fatal("disabled Start must return the original context and no span")
	}
}

func TestDisabledHTTPAddsZeroAllocations(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	w := httptest.NewRecorder()
	called := 0
	next := func(got http.ResponseWriter, req *http.Request) {
		if got != w || req != r {
			t.Fatal("disabled wrapper changed arguments")
		}
		called++
	}
	allocs := testing.AllocsPerRun(100, func() { HTTP(w, r, "graphql.request", next) })
	if allocs != 0 || called != 101 {
		t.Fatalf("allocations %g calls %d", allocs, called)
	}
}

func TestHTTPParentAndSafeAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
	t.Cleanup(Install(tp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true})
	r := httptest.NewRequest(http.MethodPost, "/graphql?token=secret", nil)
	propagation.TraceContext{}.Inject(trace.ContextWithSpanContext(r.Context(), parent), propagation.HeaderCarrier(r.Header))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	HTTP(w, r, "graphql.request", func(w http.ResponseWriter, r *http.Request) {
		Operation(r.Context(), "GetDevices", "tenant-a", true)
		w.WriteHeader(http.StatusBadRequest)
	})
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans %d, want sampled upstream even with zero root sampling", len(spans))
	}
	s := spans[0]
	if s.Name != "GetDevices" || s.Parent.SpanID() != parent.SpanID() || s.SpanContext.TraceID() != parent.TraceID() || s.SpanKind != trace.SpanKindServer {
		t.Fatalf("wrong HTTP span: %+v", s)
	}
	attrs := map[string]any{}
	for _, a := range s.Attributes {
		attrs[string(a.Key)] = a.Value.AsInterface()
	}
	if len(attrs) != 3 || attrs["tenant.id"] != "tenant-a" || attrs["transport"] != "http" || attrs["http.response.status_code"] != int64(400) {
		t.Fatalf("unsafe or missing attributes: %v", attrs)
	}
}

func TestSetupSamplerAndServiceName(t *testing.T) {
	zero := 0.0
	shutdown, err := Setup(context.Background(), config.TracingConfiguration{Enabled: true, Protocol: "grpc", Endpoint: "127.0.0.1:4317", Insecure: true, SamplingRatio: &zero}, "device-management")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = shutdown(ctx)
	})
	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok || !Enabled() {
		t.Fatal("setup did not install an SDK provider")
	}
	exporter := tracetest.NewInMemoryExporter()
	tp.RegisterSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter))
	_, root := Start(context.Background(), "root", "nats", "", trace.SpanKindServer)
	if root.IsRecording() {
		t.Fatal("explicit zero sampled a new root")
	}
	root.End()
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true})
	_, child := Start(trace.ContextWithSpanContext(context.Background(), parent), "child", "nats", "", trace.SpanKindConsumer)
	child.End()
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Parent.SpanID() != parent.SpanID() || spans[0].SpanContext.TraceID() != parent.TraceID() {
		t.Fatalf("setup lost sampled upstream: %+v", spans)
	}
	name := ""
	for _, a := range spans[0].Resource.Attributes() {
		if a.Key == "service.name" {
			name = a.Value.AsString()
		}
	}
	if name != "devicechain-device-management" {
		t.Fatalf("service.name %q", name)
	}
}
