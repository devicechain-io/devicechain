// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdlog "log"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/go-logr/logr"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
)

func captureOtelLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	previousLogger := otelLogger
	previousHandler := otel.GetErrorHandler()
	otelLogger = func() *zerolog.Logger { return &logger }
	t.Cleanup(func() {
		otelLogger = previousLogger
		otel.SetErrorHandler(previousHandler)
	})
	// OTel has no logger getter. Leave the sink installed; it resolves the restored seam.
	return &buf
}

func readOtelLine(t *testing.T, buf *bytes.Buffer, level string) map[string]any {
	t.Helper()
	if bytes.Count(buf.Bytes(), []byte("\n")) != 1 || !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Fatalf("want exactly one JSON line, got %q", buf.String())
	}
	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["level"] != level || fields["component"] != "opentelemetry" {
		t.Fatalf("unexpected log fields: %v", fields)
	}
	buf.Reset()
	return fields
}

func TestOtelErrorsAreJSONLines(t *testing.T) {
	buf := captureOtelLogs(t)
	var stdbuf bytes.Buffer
	previousOutput := stdlog.Writer()
	stdlog.SetOutput(&stdbuf)
	t.Cleanup(func() { stdlog.SetOutput(previousOutput) })
	installOtelLogging()
	otel.Handle(errors.New("traces export: boom"))
	fields := readOtelLine(t, buf, "error")
	if err, ok := fields["error"].(string); !ok || !strings.Contains(err, "traces export: boom") {
		t.Fatalf("missing export error: %v", fields)
	}
	if stdbuf.Len() != 0 {
		t.Fatalf("stdlib log received %q", stdbuf.String())
	}
}

func TestOtelInternalLoggerIsJSON(t *testing.T) {
	buf := captureOtelLogs(t)
	installOtelLogging()
	logger := logr.New(newOtelLogSink())
	logger.Error(errors.New("bad env"), "parse failed", "key", "OTEL_X")
	fields := readOtelLine(t, buf, "error")
	if fields["key"] != "OTEL_X" || fields["error"] != "bad env" || fields["message"] != "parse failed" {
		t.Fatalf("unexpected error fields: %v", fields)
	}
	logger.V(1).Info("dropped", "count", 3)
	if fields := readOtelLine(t, buf, "warn"); fields["count"] != float64(3) {
		t.Fatalf("unexpected warning fields: %v", fields)
	}
	logger.V(4).Info("noise")
	if buf.Len() != 0 {
		t.Fatalf("verbose info was logged: %q", buf.String())
	}
	logger.WithValues("a", "b").WithName("x").Error(errors.New("failed"), "named")
	if fields := readOtelLine(t, buf, "error"); fields["a"] != "b" || fields["logger"] != "x" {
		t.Fatalf("missing logger context: %v", fields)
	}
	logger.WithValues("unmatched").WithName("x").WithName("y").Error(nil, "odd", "tail")
	fields = readOtelLine(t, buf, "error")
	for _, key := range []string{"unmatched", "tail"} {
		if value, ok := fields[key]; !ok || value != nil {
			t.Fatalf("unmatched key %q must have a nil value: %v", key, fields)
		}
	}
	if fields["logger"] != "x/y" {
		t.Fatalf("unexpected logger name: %v", fields)
	}
}

func TestSetupWithTracingDisabledLeavesOtelErrorHandlingAlone(t *testing.T) {
	buf := captureOtelLogs(t)
	var recorded []error
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { recorded = append(recorded, err) }))
	shutdown, err := Setup(context.Background(), config.TracingConfiguration{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := errors.New("disabled")
	otel.Handle(want)
	if len(recorded) != 1 || recorded[0] != want || buf.Len() != 0 {
		t.Fatalf("disabled setup changed error handling: recorded %v, logs %q", recorded, buf.String())
	}
}

func TestSetupWithTracingEnabledInstallsJSONErrorHandling(t *testing.T) {
	buf := captureOtelLogs(t)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	shutdown, err := Setup(context.Background(), config.TracingConfiguration{
		Enabled: true, Protocol: "grpc", Endpoint: "127.0.0.1:1", Insecure: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	otel.Handle(errors.New("x"))
	if fields := readOtelLine(t, buf, "error"); fields["error"] != "x" {
		t.Fatalf("unexpected error: %v", fields)
	}
}
