// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/tracing"
	"github.com/nats-io/nats.go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var tracedMessageSink *nats.Msg

func TestDisabledPublishHeadersAndAllocations(t *testing.T) {
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled}))
	m := Message{Value: []byte("event"), DedupID: "dedup", Headers: map[string]string{HeaderCorrelationID: "upstream", "Dc-Ev": "1"}}
	want := natsMsg("i.t.inbound-events", m)
	got := publishMessage(ctx, want.Subject, streams.InboundEvents, m)
	if !reflect.DeepEqual(want, got) || got.Header.Get("traceparent") != "" || got.Header.Get(nats.MsgIdHdr) != "dedup" {
		t.Fatalf("disabled wire message changed: %+v", got)
	}
	baseline := testing.AllocsPerRun(100, func() { tracedMessageSink = natsMsg(want.Subject, m) })
	actual := testing.AllocsPerRun(100, func() { tracedMessageSink = publishMessage(ctx, want.Subject, streams.InboundEvents, m) })
	if actual != baseline {
		t.Fatalf("tracing adds %g allocations", actual-baseline)
	}
	if n := testing.AllocsPerRun(100, func() { injectPublish(ctx, streams.InboundEvents, m, got) }); n != 0 {
		t.Fatalf("disabled injection allocates %g", n)
	}
}

func TestOneTraceAcrossNATSPublishRoutes(t *testing.T) {
	_, nmgr := orderedManager(t)
	exporter := tracetest.NewInMemoryExporter()
	producer := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	consumer := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
	ctx, root := producer.Tracer("event-sources").Start(orderedCtx(), "ingest")
	sw, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	ow := newOrdered(t, nmgr, 4)
	defer ow.Close()
	aw := newAsync(t, nmgr, 4, false)
	defer aw.Close()
	rp, err := nmgr.NewRoundPublisher(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct {
		name string
		send func(Message) error
	}{
		{"sync", func(m Message) error { return sw.WriteMessages(ctx, m) }},
		{"ordered", func(m Message) error {
			done := make(chan error, 1)
			ow.Publish(ctx, m, func(err error) { done <- err })
			return awaitTracePublish(t, done)
		}},
		{"async batch", func(m Message) error {
			done := make(chan error, 1)
			aw.Submit(ctx, []Message{m}, func(err error) { done <- err })
			return awaitTracePublish(t, done)
		}},
		{"round", func(m Message) error { _, err := rp.Publish(ctx, m); return err }},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			exporter.Reset()
			restore := tracing.Install(producer)
			t.Cleanup(restore)
			err := route.send(Message{Value: []byte(route.name), DedupID: route.name, Headers: map[string]string{"Dc-Ev": "1", HeaderCorrelationID: "upstream"}})
			restore()
			if err != nil {
				t.Fatal(err)
			}
			info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := nmgr.js.GetMsg(info.Config.Name, info.State.LastSeq)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw.Data) != route.name || raw.Header.Get("traceparent") == "" || raw.Header.Get(nats.MsgIdHdr) != route.name || raw.Header.Get("Dc-Ev") != "1" {
				t.Fatalf("wrong stored message: %+v", raw)
			}
			wire := &nats.Msg{Subject: raw.Subject, Header: raw.Header, Data: raw.Data}
			if wireSize(wire) != wire.Size() {
				t.Fatal("trace headers missing from size accounting")
			}
			restore = tracing.Install(consumer)
			t.Cleanup(restore)
			msg := NewConsumedMessage(raw.Subject, raw.Data, 1, natsHeaders(wire), nil)
			if trace.SpanContextFromContext(msg.TraceContext()).TraceID() != root.SpanContext().TraceID() {
				t.Fatal("accessor lost the publisher trace")
			}
			_, _, span := msg.Process(context.Background(), "resolved-events.store")
			if span == nil {
				t.Fatal("consumer span absent")
			}
			span.End()
			restore()
			spans := exporter.GetSpans()
			if len(spans) != 2 {
				t.Fatalf("got %d spans, want producer and consumer", len(spans))
			}
			pub, con := spans[0], spans[1]
			if pub.SpanKind != trace.SpanKindProducer || con.SpanKind != trace.SpanKindConsumer || pub.Parent.SpanID() != root.SpanContext().SpanID() || con.Parent.SpanID() != pub.SpanContext.SpanID() || con.SpanContext.TraceID() != root.SpanContext().TraceID() {
				t.Fatalf("broken trace links: %+v %+v", pub, con)
			}
		})
	}
	root.End()
	_ = producer.Shutdown(context.Background())
	_ = consumer.Shutdown(context.Background())
}

func awaitTracePublish(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("publish timed out")
		return nil
	}
}

func TestEnvelopeMembersKeepTheirOwnTraces(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(tracing.Install(tp))
	first, a := tp.Tracer("ingest").Start(context.Background(), "first")
	second, b := tp.Tracer("ingest").Start(context.Background(), "second")
	m, _ := envelopeMessage(t, nil, event("a", 1), event("b", 1))
	m = m.WithTraceMembers([]TraceMember{{Index: 0, Context: first}, {Index: 2, Context: second}})
	nm := publishMessage(first, m.Subject, streams.InboundEvents, m)
	if nm.Header.Get(HeaderTraceMembers) == "" || wireSize(nm) != nm.Size() {
		t.Fatal("missing or uncounted envelope trace metadata")
	}
	consumed := NewConsumedMessage(nm.Subject, nm.Data, 1, natsHeaders(nm), nil)
	units, marked, err := SplitEnvelope(consumed, "acme", noop, nil)
	if err != nil || !marked || len(units) != 2 {
		t.Fatalf("split: %v %v %d", err, marked, len(units))
	}
	for _, u := range units {
		_, _, span := u.Msg.Process(context.Background(), "inbound-events")
		if span == nil {
			t.Fatal("member span missing")
		}
		span.End()
	}
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("spans %d", len(spans))
	}
	for i, root := range []trace.Span{a, b} {
		pub, con := spans[i], spans[i+2]
		if pub.Parent.SpanID() != root.SpanContext().SpanID() || con.Parent.SpanID() != pub.SpanContext.SpanID() || con.SpanContext.TraceID() != root.SpanContext().TraceID() {
			t.Fatalf("member %d joined another trace", i)
		}
	}
	a.End()
	b.End()
	_ = tp.Shutdown(context.Background())
}

func TestEnabledPublishRequiresPublisherContext(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(tracing.Install(tp))
	m := Message{Headers: map[string]string{"traceparent": "00-01000000000000000000000000000000-0200000000000000-01", "tracestate": "vendor=value", HeaderCorrelationID: "upstream"}}
	nm := publishMessage(context.Background(), "i.t.inbound-events", streams.InboundEvents, m)
	if nm.Header.Get("traceparent") != "" || nm.Header.Get("tracestate") != "" || len(exporter.GetSpans()) != 0 {
		t.Fatal("forwarded propagation without a publisher span")
	}
	if m.Headers["traceparent"] == "" {
		t.Fatal("mutated caller headers")
	}
}

func BenchmarkDisabledPublishHeaders(b *testing.B) {
	ctx := context.Background()
	m := Message{Headers: map[string]string{HeaderCorrelationID: "upstream"}}
	nm := natsMsg("i.t.inbound-events", m)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		injectPublish(ctx, streams.InboundEvents, m, nm)
	}
}
