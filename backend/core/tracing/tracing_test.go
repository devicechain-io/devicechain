// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
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
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
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
		t.Fatalf("spans %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "GetDevices" || s.Parent.IsValid() || s.SpanContext.TraceID() == parent.TraceID() || len(s.Links) != 1 || s.Links[0].SpanContext.TraceID() != parent.TraceID() || s.SpanKind != trace.SpanKindServer {
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

// With the sampler at ratio 0, a device that sends a sampled traceparent must not get a
// sampled span, and its context is linked, never adopted.
func TestUntrustedEdgeIgnoresRemoteSampledFlag(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0))))
	t.Cleanup(Install(tp))
	const remoteTrace = "0af7651916cd43dd8448eb211c80319c"
	var inner trace.SpanContext
	r := httptest.NewRequest(http.MethodPost, "/events", nil)
	r.Header.Set("traceparent", "00-"+remoteTrace+"-b7ad6b7169203331-01")
	r.Header.Set("tracestate", "vendor=huge")
	HTTP(httptest.NewRecorder(), r, "ingest.http", func(w http.ResponseWriter, req *http.Request) {
		inner = trace.SpanContextFromContext(req.Context())
	})
	if inner.IsSampled() || inner.TraceID().String() == remoteTrace || inner.TraceState().Len() != 0 {
		t.Fatalf("remote context was adopted: %+v", inner)
	}
	if n := len(exporter.GetSpans()); n != 0 {
		t.Fatalf("ratio 0 recorded %d spans for a device-sampled request", n)
	}

	// At ratio 1 the span exists, is a new root, and links the remote context.
	exporter.Reset()
	tp2 := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(Install(tp2))
	HTTP(httptest.NewRecorder(), r, "ingest.http", func(http.ResponseWriter, *http.Request) {})
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Parent.IsValid() || len(spans[0].Links) != 1 ||
		spans[0].Links[0].SpanContext.TraceID().String() != remoteTrace {
		t.Fatalf("want one root span linking the remote trace, got %+v", spans)
	}
}

func TestOperationNameIsBounded(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(Install(tp))
	ctx, span := Start(context.Background(), "graphql.request", "http", "", trace.SpanKindServer)
	Operation(ctx, strings.Repeat("x", 10000), "", false)
	span.End()
	if got := exporter.GetSpans()[0].Name; len(got) != maxOperationNameBytes {
		t.Fatalf("span name is %d bytes", len(got))
	}
}

// The OTLP/HTTP exporter given a path-less URL must post to /v1/traces, not "/".
func TestHTTPExporterPostsToTracesPath(t *testing.T) {
	one := 1.0
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	for url, want := range map[string]string{srv.URL: "POST /v1/traces", srv.URL + "/": "POST /v1/traces", srv.URL + "/prefix/v1/traces": "POST /prefix/v1/traces"} {
		shutdown, err := Setup(context.Background(), config.TracingConfiguration{Enabled: true, Protocol: "http", Endpoint: url, Insecure: true, SamplingRatio: &one}, "test")
		if err != nil {
			t.Fatal(err)
		}
		_, span := Start(context.Background(), "probe", "test", "", trace.SpanKindInternal)
		span.End()
		if err := shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("%s: collector saw %q, want %q", url, g, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: collector saw nothing", url)
		}
	}
}
