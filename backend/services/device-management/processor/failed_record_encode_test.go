// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// capturingWriter keeps every message published, so a test can assert on the BODY. The shared
// MockMessageWriter records only that WriteMessages was called; the defect is a publish whose
// body is nil, which a call count cannot see.
type capturingWriter struct {
	msgs []messaging.Message
	err  error
}

func (w *capturingWriter) WriteMessages(_ context.Context, m ...messaging.Message) error {
	w.msgs = append(w.msgs, m...)
	return w.err
}

func (w *capturingWriter) WriteToDevice(_ context.Context, _ string, m ...messaging.Message) error {
	w.msgs = append(w.msgs, m...)
	return w.err
}

func (w *capturingWriter) HandleResponse(error) {}

// countingAck counts the acks a consumed message receives.
type countingAck struct{ n int }

func (a *countingAck) Ack() error { a.n++; return nil }

// failedRecordProcessor builds the processor through its real constructor over a Microservice
// with its own registry, wired to the dead-letter producer the way main.go wires it. The
// failed channel is made directly (capacity 1) and Initialize is not run, so no resolver starts.
func failedRecordProcessor(t *testing.T, w *capturingWriter) (*InboundEventsProcessor, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	iproc := NewInboundEventsProcessor(ms, nil, nil, msgtest.InlineOrderedWriter{W: w},
		core.NewNoOpLifecycleCallbacks(), nil, config.AuthModeOptional, 0, NewResolveMetrics(ms),
		WithDeadLetters(deadletter.NewProducer(ms)))
	iproc.failed = make(chan failedItem, 1)
	return iproc, reg
}

// queueFailed puts one failure on the channel, its source a consumed message that reports its
// acks to ack. errText becomes the record's Error: "\xff" is not valid UTF-8, which the
// proto3 encoder refuses.
func queueFailed(iproc *InboundEventsProcessor, errText string, ack *countingAck) {
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), "device-management",
		"event could not be resolved", errors.New(errText), []byte("payload"))
	src := messaging.NewConsumedMessage(testTenantSubject, []byte("x"), 1, nil, ack)
	src.StreamSeq = 42
	iproc.failed <- failedItem{tenant: "tenant1", event: *failed, src: src, correlation: "corr-1"}
}

// The encoder the tests below lean on: PFailedEvent is proto3, and a proto3 string field
// holding invalid UTF-8 does not marshal. If a protobuf change stopped refusing it, the
// failure would surface HERE, with the reason, instead of as a puzzling failure further down.
func TestTheFailedEventEncoderRefusesInvalidUTF8(t *testing.T) {
	_, err := proto.MarshalFailedEvent(&dmodel.FailedEvent{Error: "\xff"})
	require.Error(t, err)
	_, err = proto.MarshalFailedEvent(&dmodel.FailedEvent{Error: "boom"})
	require.NoError(t, err)
}

// A failure record that will not encode must not be stored. On the code this replaced it was
// published with a nil body, which proto3 decodes WITHOUT ERROR as an empty failure with
// reason 0, and its source was acked behind it: the real failure was recorded nowhere.
func TestAFailureRecordThatCannotBeEncodedIsNotPublished(t *testing.T) {
	w := &capturingWriter{}
	iproc, reg := failedRecordProcessor(t, w)
	ack := &countingAck{}
	queueFailed(iproc, "\xff", ack)

	require.False(t, iproc.ProcessFailedEvent(context.Background()))

	require.Len(t, w.msgs, 0, "a record that cannot be encoded was published: %+v", w.msgs)
	require.Equal(t, 1, ack.n, "the source is acked once: a redelivery yields the same unencodable record")
	require.Equal(t, 1.0, gatheredCounter(t, reg, deviceManagementLost))
}

// The counterweight: refusing everything would also pass the test above. An encodable record
// still publishes a body that decodes to what was recorded, and its source is acked once.
func TestAnEncodableFailureRecordIsPublishedAndDecodes(t *testing.T) {
	w := &capturingWriter{}
	iproc, reg := failedRecordProcessor(t, w)
	ack := &countingAck{}
	queueFailed(iproc, "boom", ack)

	require.False(t, iproc.ProcessFailedEvent(context.Background()))

	require.Len(t, w.msgs, 1)
	msg := w.msgs[0]
	require.NotEmpty(t, msg.Value)
	got, err := proto.UnmarshalFailedEvent(msg.Value)
	require.NoError(t, err)
	require.Equal(t, "event could not be resolved", got.Message)
	require.Equal(t, "boom", got.Error)
	require.Equal(t, uint(proto.FailureReason_Invalid), got.Reason)
	require.Equal(t, strconv.Itoa(int(proto.FailureReason_Invalid)), string(msg.Key))
	require.NotEmpty(t, msg.DedupID)
	require.Equal(t, "corr-1", msg.CorrelationID())
	require.Equal(t, 1, ack.n)
	require.Equal(t, 0.0, gatheredCounter(t, reg, deviceManagementLost))
}

// A publish that fails is not this loss: the record encoded, the broker refused it, and the
// source is left unacked so it is redelivered (and, at its last delivery, lettered by the
// max-delivery recorder). Counting it here would page for a failure that is being retried.
func TestAFailureRecordWhosePublishFailsIsLeftForRedelivery(t *testing.T) {
	w := &capturingWriter{err: errors.New("broker down")}
	iproc, reg := failedRecordProcessor(t, w)
	ack := &countingAck{}
	queueFailed(iproc, "boom", ack)

	require.False(t, iproc.ProcessFailedEvent(context.Background()))

	require.Len(t, w.msgs, 1)
	require.Equal(t, 0, ack.n, "an unstored record must leave its source to be redelivered")
	require.Equal(t, 0.0, gatheredCounter(t, reg, deviceManagementLost))
}

func TestWithDeadLettersRefusesNil(t *testing.T) {
	require.Panics(t, func() { WithDeadLetters(nil) })
}

// The unresolved-event arm shares the failed-record policy: an archived event that will not
// encode leaves no failure record to publish, so it is lost the same way — acked (a redelivery
// cannot encode it either), counted on the same series, and nothing reaches the failed-events
// writer.
func TestAnUnresolvedEventThatCannotBeEncodedIsCountedAsLost(t *testing.T) {
	w := &capturingWriter{}
	iproc, reg := failedRecordProcessor(t, w)
	ack := &countingAck{}
	src := messaging.NewConsumedMessage(testTenantSubject, []byte("x"), 1, nil, ack)

	iproc.OnUnresolvedEvent(src, "tenant1", uint(proto.FailureReason_Invalid),
		esmodel.UnresolvedEvent{Device: "\xff"}, errors.New("unresolved"))

	require.Len(t, iproc.failed, 0, "an unencodable archive must not become a failure record")
	require.Len(t, w.msgs, 0)
	require.Equal(t, 1, ack.n)
	require.Equal(t, 1.0, gatheredCounter(t, reg, deviceManagementLost))
}
