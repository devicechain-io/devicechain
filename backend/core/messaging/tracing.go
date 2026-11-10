// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/tracing"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HeaderTraceMembers preserves each event's parent when several independent
// traces share an envelope. It contains only W3C trace IDs and trace state.
const HeaderTraceMembers = "Dc-Tc"

type TraceMember struct {
	Index   uint32
	Context context.Context
}

type memberTrace struct {
	Parent string `json:"p"`
	State  string `json:"s,omitempty"`
}

// NATS header lookup is case-sensitive, unlike HTTP. Keep W3C keys lowercase
// on publish and accept either casing from an upstream service on consume.
type natsTraceCarrier nats.Header

func (h natsTraceCarrier) Get(key string) string {
	for k, values := range h {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
func (h natsTraceCarrier) Set(key, value string) { h[key] = []string{value} }
func (h natsTraceCarrier) Keys() []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	return keys
}

type messageTraceCarrier map[string]string

func (h messageTraceCarrier) Get(key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
func (h messageTraceCarrier) Set(key, value string) { h[key] = value }
func (h messageTraceCarrier) Keys() []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	return keys
}

func (m Message) WithTraceMembers(members []TraceMember) Message {
	if tracing.Enabled() {
		m.traceMembers = members
	}
	return m
}

func memberTraces(m Message) map[string]memberTrace {
	if !tracing.Enabled() || m.Headers[HeaderTraceMembers] == "" {
		return nil
	}
	var parents map[string]memberTrace
	// A malformed or untrusted propagation header never changes event processing.
	_ = json.Unmarshal([]byte(m.Headers[HeaderTraceMembers]), &parents)
	return parents
}

func (m Message) memberTraceContext(parents map[string]memberTrace, index uint32) Message {
	if len(parents) == 0 {
		return m
	}
	if p, ok := parents[strconv.FormatUint(uint64(index), 10)]; ok {
		m.traceContext = propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": p.Parent, "tracestate": p.State})
	} else {
		m.traceContext = context.Background()
	}
	m.traceMembers = nil
	return m
}

// TraceContext exposes propagation metadata without starting a span. Call Context
// to retain a handler's existing cancellation, deadlines and local values.
func (m Message) TraceContext() context.Context {
	if !tracing.Enabled() {
		return context.Background()
	}
	if m.traceContext != nil {
		return m.traceContext
	}
	return propagation.TraceContext{}.Extract(context.Background(), messageTraceCarrier(m.Headers))
}

func (m Message) Context(base context.Context) context.Context {
	if !tracing.Enabled() {
		return base
	}
	sc := trace.SpanContextFromContext(m.TraceContext())
	if !sc.IsValid() {
		return base
	}
	return trace.ContextWithSpanContext(base, sc)
}

func (m Message) WithTraceContext(ctx context.Context) Message {
	if tracing.Enabled() {
		m.traceContext = ctx
	}
	return m
}

// Process starts only the explicitly instrumented event-path consumers. Subject
// tokens can contain device credentials, so the span name uses a fixed hop name.
func (m Message) Process(base context.Context, hop string) (Message, context.Context, trace.Span) {
	if !tracing.Enabled() {
		return m, base, nil
	}
	tenant, _ := ParseTenantFromSubject(m.Subject)
	ctx, span := tracing.Start(m.Context(base), "messaging.process "+hop, "nats", tenant, trace.SpanKindConsumer)
	return m.WithTraceContext(ctx), ctx, span
}

// publishMessage builds a wire message for header-path checks. Writers use
// startPublishMessage and finishPublish around the actual publish/PubAck.
func publishMessage(ctx context.Context, subject, suffix string, m Message) *nats.Msg {
	nm, spans := startPublishMessage(ctx, subject, suffix, m)
	finishPublish(spans, nil)
	return nm
}

func startPublishMessage(ctx context.Context, subject, suffix string, m Message) (*nats.Msg, []trace.Span) {
	// The async queue already captures ctx in asyncItem before changing goroutines.
	// A queued message may retain a more specific context than the batch's own.
	if tracing.Enabled() && m.traceContext != nil {
		ctx = m.Context(ctx)
	}
	nm := natsMsg(subject, m)
	return nm, injectPublish(ctx, suffix, m, nm)
}

func finishPublish(spans []trace.Span, err error) {
	for _, span := range spans {
		if err != nil {
			span.SetStatus(codes.Error, "Message publication failed")
		}
		span.End()
	}
}

func injectPublish(ctx context.Context, suffix string, m Message, nm *nats.Msg) []trace.Span {
	if !tracing.Enabled() {
		return nil
	}
	// Never forward caller-provided propagation in place of the publisher's span.
	for k := range nm.Header {
		if strings.EqualFold(k, "traceparent") || strings.EqualFold(k, "tracestate") || strings.EqualFold(k, HeaderTraceMembers) {
			delete(nm.Header, k)
		}
	}
	if len(m.traceMembers) > 0 {
		spans := make([]trace.Span, 0, len(m.traceMembers))
		parents := make(map[string]memberTrace, len(m.traceMembers))
		tenant, _ := ParseTenantFromSubject(nm.Subject)
		for _, member := range m.traceMembers {
			if member.Context == nil || !trace.SpanContextFromContext(member.Context).IsValid() {
				continue
			}
			memberCtx, span := tracing.Start(member.Context, "messaging.publish "+suffix, "nats", tenant, trace.SpanKindProducer)
			carrier := propagation.MapCarrier{}
			propagation.TraceContext{}.Inject(memberCtx, carrier)
			parents[strconv.FormatUint(uint64(member.Index), 10)] = memberTrace{Parent: carrier.Get("traceparent"), State: carrier.Get("tracestate")}
			if nm.Header.Get("traceparent") == "" {
				propagation.TraceContext{}.Inject(memberCtx, natsTraceCarrier(nm.Header))
			}
			spans = append(spans, span)
		}
		if len(parents) > 0 {
			data, _ := json.Marshal(parents)
			nm.Header.Set(HeaderTraceMembers, string(data))
		}
		return spans
	}
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	tenant, _ := ParseTenantFromSubject(nm.Subject)
	ctx, span := tracing.Start(ctx, "messaging.publish "+suffix, "nats", tenant, trace.SpanKindProducer)
	propagation.TraceContext{}.Inject(ctx, natsTraceCarrier(nm.Header))
	return []trace.Span{span}
}
