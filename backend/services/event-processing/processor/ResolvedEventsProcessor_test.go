// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// fakeReader replays a fixed sequence of (message, error) results, then blocks on
// ctx so the read loop parks instead of spinning past the end of the script.
type fakeReader struct {
	results []readResult
	idx     int
	handled int
	lastErr error
}

type readResult struct {
	msg messaging.Message
	err error
}

// fakeAck records how a message was dispositioned so a test can assert the drop
// actually acknowledged (a plain struct-literal Message's Ack is a no-op).
type fakeAck struct {
	acks int
	naks int
}

func (a *fakeAck) Ack() error { a.acks++; return nil }
func (a *fakeAck) Nak() error { a.naks++; return nil }

func (r *fakeReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if r.idx < len(r.results) {
		res := r.results[r.idx]
		r.idx++
		return res.msg, res.err
	}
	<-ctx.Done()
	return messaging.Message{}, io.EOF
}

func (r *fakeReader) HandleResponse(err error) {
	r.handled++
	r.lastErr = err
}

// resolvedBytes marshals a minimal resolved event carrying the ADR-051 tokens.
func resolvedBytes(t *testing.T) []byte {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   "dev-1",
		DeviceTypeToken:     "sensor-type",
		ProfileVersionToken: "temp-profile@3",
		OccurredTime:        time.Unix(0, 0).UTC(),
		ProcessedTime:       time.Unix(0, 0).UTC(),
		EventType:           esmodel.Measurement,
		Payload:             &dmmodel.ResolvedMeasurementsPayload{},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return b
}

// assertDropped drives one message through the loop and asserts it was acked
// exactly once and never nak'd (the drop contract), continuing (not EOF).
func assertDropped(t *testing.T, subject string, value []byte) {
	t.Helper()
	ack := &fakeAck{}
	msg := messaging.NewConsumedMessage(subject, value, 0, nil, ack)
	rp := &ResolvedEventsProcessor{ResolvedEventsReader: &fakeReader{results: []readResult{{msg: msg}}}}
	if eof := rp.processMessage(context.Background()); eof {
		t.Fatal("expected loop to continue after a message, got EOF")
	}
	if ack.acks != 1 || ack.naks != 0 {
		t.Fatalf("expected exactly one ack and no nak, got acks=%d naks=%d", ack.acks, ack.naks)
	}
}

// A parseable resolved event on a tenant-scoped subject is consumed and dropped
// (acked once, never nak'd).
func TestProcessMessageDropsResolvedEvent(t *testing.T) {
	assertDropped(t, "dc.acme.resolved-events", resolvedBytes(t))
}

// A message whose subject carries no parseable tenant is dropped fail-closed
// (acked, not routed/nak'd) rather than processed.
func TestProcessMessageDropsUntenantedMessage(t *testing.T) {
	assertDropped(t, "no-tenant-here", resolvedBytes(t))
}

// An unparseable payload is dropped (no dead-letter path): acked once, never nak'd,
// so it is not redelivered.
func TestProcessMessageDropsUnparseablePayload(t *testing.T) {
	assertDropped(t, "dc.acme.resolved-events", []byte("not-a-proto"))
}

// EOF on the stream signals the loop to stop.
func TestProcessMessageStopsOnEOF(t *testing.T) {
	rp := &ResolvedEventsProcessor{ResolvedEventsReader: &fakeReader{
		results: []readResult{{err: io.EOF}},
	}}
	if eof := rp.processMessage(context.Background()); !eof {
		t.Fatal("expected loop to stop on EOF")
	}
}

// A transient read error is handed to the reader's self-heal path and the loop
// continues (does not treat it as EOF).
func TestProcessMessageContinuesOnTransientError(t *testing.T) {
	reader := &fakeReader{results: []readResult{{err: context.DeadlineExceeded}}}
	rp := &ResolvedEventsProcessor{ResolvedEventsReader: reader}
	if eof := rp.processMessage(context.Background()); eof {
		t.Fatal("expected loop to continue after a transient error, got EOF")
	}
	if reader.handled != 1 {
		t.Fatalf("expected transient error to be handed to the reader once, got %d", reader.handled)
	}
}
