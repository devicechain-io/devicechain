// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tracing owns opt-in tracing. Disabled paths return before consulting OTel.
package tracing

import (
	"context"
	"net/url"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var enabled atomic.Bool

func Enabled() bool { return enabled.Load() }

// Setup installs nothing at all when disabled, including no propagator.
func Setup(ctx context.Context, cfg config.TracingConfiguration, area string) (func(context.Context) error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	// Route exporter and SDK diagnostics through the service's JSON logger.
	installOtelLogging()
	var exporter sdktrace.SpanExporter
	var err error
	if cfg.Protocol == "http" {
		opts, oerr := httpExporterOptions(cfg)
		if oerr != nil {
			return nil, oerr
		}
		exporter, err = otlptracehttp.New(ctx, opts...)
	} else {
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exporter, err = otlptracegrpc.New(ctx, opts...)
	}
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Ratio()))),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "devicechain-"+area))),
	)
	restore := Install(tp)
	return func(ctx context.Context) error {
		enabled.Store(false)
		err := tp.Shutdown(ctx)
		restore()
		return err
	}, nil
}

// Install activates an SDK provider and returns a function restoring the previous
// globals. It also allows an in-memory exporter in tests. Install and restoration
// are startup/shutdown operations, never per-request operations.
func Install(tp trace.TracerProvider) func() {
	previous, propagator, wasEnabled := otel.GetTracerProvider(), otel.GetTextMapPropagator(), Enabled()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	enabled.Store(true)
	return func() {
		enabled.Store(wasEnabled)
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(propagator)
	}
}

// Start returns a nil span when disabled; callers only defer End for a non-nil span.
// StartEdge starts a span at an UNTRUSTED boundary (a device's HTTP request, a GraphQL
// call). The caller's traceparent/tracestate is not adopted: a remote party must not choose
// whether this platform samples, nor the tracestate it carries onward. The span is a new
// local root, linked to the remote context so the two can still be correlated.
func StartEdge(ctx context.Context, name, transport string, remote trace.SpanContext) (context.Context, trace.Span) {
	if !Enabled() {
		return ctx, nil
	}
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer), trace.WithNewRoot()}
	if remote.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: remote.WithTraceState(trace.TraceState{})}))
	}
	ctx, span := otel.Tracer("devicechain").Start(ctx, name, opts...)
	span.SetAttributes(attribute.String("transport", transport))
	return ctx, span
}

func Start(ctx context.Context, name, transport, tenant string, kind trace.SpanKind) (context.Context, trace.Span) {
	if !Enabled() {
		return ctx, nil
	}
	ctx, span := otel.Tracer("devicechain").Start(ctx, name, trace.WithSpanKind(kind))
	span.SetAttributes(attribute.String("transport", transport))
	if tenant != "" {
		span.SetAttributes(attribute.String("tenant.id", tenant))
	}
	return ctx, span
}

// httpExporterOptions maps the configured URL onto the exporter. WithEndpointURL keeps a
// path-less URL's path as "/", which a collector answers 404 (the OTLP/HTTP path is
// /v1/traces), so a URL with no path is given the standard one. A URL that names a path
// (a collector behind a prefix) is used exactly as written.
//
// The OTEL_EXPORTER_OTLP_* environment variables are still read by the exporter and can
// override these settings; they are documented as such rather than cleared.
func httpExporterOptions(cfg config.TracingConfiguration) ([]otlptracehttp.Option, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	var opts []otlptracehttp.Option
	if u.Path == "" || u.Path == "/" {
		opts = append(opts, otlptracehttp.WithEndpoint(u.Host), otlptracehttp.WithURLPath("/v1/traces"))
	} else {
		opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	}
	if cfg.Insecure || u.Scheme == "http" {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return opts, nil
}
