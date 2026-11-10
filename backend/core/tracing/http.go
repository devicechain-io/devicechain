// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// HTTP passes through with no allocations when disabled. No URL, headers, body,
// document, variables or credentials are recorded.
func HTTP(w http.ResponseWriter, r *http.Request, name string, next http.HandlerFunc) {
	if !Enabled() {
		next(w, r)
		return
	}
	ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := Start(ctx, name, "http", "", trace.SpanKindServer)
	defer span.End()
	rw := &responseWriter{ResponseWriter: w}
	defer func() {
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 400 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}()
	next(rw, r.WithContext(ctx))
}

// Operation is called after the normal decode; it never parses a document for tracing.
func Operation(ctx context.Context, name, tenant string, failed bool) {
	if !Enabled() {
		return
	}
	span := trace.SpanFromContext(ctx)
	if name != "" {
		span.SetName(name)
	}
	if tenant != "" {
		span.SetAttributes(attribute.String("tenant.id", tenant))
	}
	if failed {
		span.SetStatus(codes.Error, "GraphQL execution failed")
	}
}
