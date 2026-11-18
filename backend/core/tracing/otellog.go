// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tracing

import (
	"github.com/go-logr/logr"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
)

// Resolve the global logger at log time because services replace it at startup.
var otelLogger = func() *zerolog.Logger { return &log.Logger }

var otelErrorHandler = otel.ErrorHandlerFunc(func(err error) {
	otelLogger().Error().Err(err).Str("component", "opentelemetry").Msg("tracing error")
})

type otelLogSink struct {
	values []any
	name   string
}

func newOtelLogSink() *otelLogSink { return &otelLogSink{} }

func (*otelLogSink) Init(logr.RuntimeInfo) {}

func (*otelLogSink) Enabled(level int) bool { return level <= 1 }

func (s *otelLogSink) Info(level int, msg string, kv ...any) {
	var event *zerolog.Event
	if level <= 1 {
		event = otelLogger().Warn()
	} else {
		event = otelLogger().Info()
	}
	s.fields(event, kv).Msg(msg)
}

func (s *otelLogSink) Error(err error, msg string, kv ...any) {
	s.fields(otelLogger().Error().Err(err), kv).Msg(msg)
}

func (s *otelLogSink) fields(event *zerolog.Event, kv []any) *zerolog.Event {
	event = event.Fields(s.values).Fields(otelLogValues(kv)).Str("component", "opentelemetry")
	if s.name != "" {
		event = event.Str("logger", s.name)
	}
	return event
}

// Give an unmatched key a nil value without modifying the caller's slice.
func otelLogValues(kv []any) []any {
	if len(kv)%2 != 0 {
		return append(append([]any(nil), kv...), nil)
	}
	return kv
}

func (s *otelLogSink) WithValues(kv ...any) logr.LogSink {
	clone := *s
	clone.values = append(append([]any(nil), s.values...), otelLogValues(kv)...)
	return &clone
}

func (s *otelLogSink) WithName(name string) logr.LogSink {
	clone := *s
	if clone.name != "" {
		clone.name += "/"
	}
	clone.name += name
	return &clone
}

func installOtelLogging() {
	otel.SetErrorHandler(otelErrorHandler)
	otel.SetLogger(logr.New(newOtelLogSink()))
}
