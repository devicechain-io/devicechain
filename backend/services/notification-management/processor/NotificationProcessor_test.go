// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
)

// A parseable scoped subject ({instance}.{tenant}.{suffix}) so the worker can derive
// a per-message tenant context (fail-closed).
const testAlarmSubject = "instance1.tenant1.alarm-events"

// recordingAck records the A3 disposition applied to a message so a test can assert
// whether the worker acked (handled / dropped) or naked (redeliver) it.
type recordingAck struct {
	acked int
	naked int
}

func (r *recordingAck) Ack() error { r.acked++; return nil }
func (r *recordingAck) Nak() error { r.naked++; return nil }

// fakeNotifier records the events handed to it and can be told to fail.
type fakeNotifier struct {
	calls  int
	events []*dmmodel.AlarmStateChangeEvent
	err    error
}

func (f *fakeNotifier) Notify(_ context.Context, e *dmmodel.AlarmStateChangeEvent) error {
	f.calls++
	f.events = append(f.events, e)
	return f.err
}

// newTestProcessor builds a processor with nil metrics (ProcessorMetrics is
// nil-safe) so dispatchOne can be exercised without a Prometheus registry.
func newTestProcessor(n Notifier) *NotificationProcessor {
	return &NotificationProcessor{Notifier: n}
}

// validEventBytes marshals a representative alarm state-change envelope.
func validEventBytes(t *testing.T) []byte {
	t.Helper()
	msg := "temperature above threshold"
	val := 42.5
	bytes, err := dmproto.MarshalAlarmStateChangeEvent(&dmmodel.AlarmStateChangeEvent{
		EventType:      dmmodel.AlarmEventRaised,
		AlarmToken:     "alarm-1",
		OriginatorType: "Device",
		OriginatorId:   7,
		AlarmKey:       "over-temp",
		MetricKey:      "temperature",
		State:          "ACTIVE",
		Severity:       "CRITICAL",
		LastValue:      &val,
		Message:        &msg,
		RaisedTime:     time.Unix(1_700_000_000, 0).UTC(),
		OccurredTime:   time.Unix(1_700_000_000, 0).UTC(),
	})
	assert.Nil(t, err)
	return bytes
}

func msgWith(subject string, value []byte, numDelivered int, ack messaging.Acknowledger) messaging.Message {
	return messaging.NewConsumedMessage(subject, value, numDelivered, nil, ack)
}

// A well-formed event is dispatched and acked once, exactly once.
func TestDispatchDeliversAndAcks(t *testing.T) {
	n := &fakeNotifier{}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), 1, ack))

	assert.Equal(t, 1, n.calls, "notifier should be invoked once")
	assert.Equal(t, 1, ack.acked, "delivered event should be acked")
	assert.Equal(t, 0, ack.naked)
	if assert.Len(t, n.events, 1) {
		assert.Equal(t, "alarm-1", n.events[0].AlarmToken)
		assert.Equal(t, dmmodel.AlarmEventRaised, n.events[0].EventType)
	}
}

// A subject with no parseable tenant is a poison message: dropped (acked), never
// dispatched, so a tenant-less event can not be routed to a tenant's channels.
func TestDispatchDropsUnparseableTenant(t *testing.T) {
	n := &fakeNotifier{}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith("bogus", validEventBytes(t), 1, ack))

	assert.Equal(t, 0, n.calls, "notifier must not run without a tenant")
	assert.Equal(t, 1, ack.acked, "poison message should be dropped (acked)")
	assert.Equal(t, 0, ack.naked)
}

// An undecodable payload is a poison message: dropped (acked), never dispatched.
func TestDispatchDropsUndecodablePayload(t *testing.T) {
	n := &fakeNotifier{}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, []byte("not-a-proto"), 1, ack))

	assert.Equal(t, 0, n.calls, "notifier must not run on an undecodable event")
	assert.Equal(t, 1, ack.acked, "poison message should be dropped (acked)")
	assert.Equal(t, 0, ack.naked)
}

// A transient dispatch failure below the redelivery cap is naked for redelivery.
func TestDispatchNaksTransientFailure(t *testing.T) {
	n := &fakeNotifier{err: errors.New("smtp timeout")}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), 1, ack))

	assert.Equal(t, 1, n.calls)
	assert.Equal(t, 1, ack.naked, "a transient failure should request redelivery")
	assert.Equal(t, 0, ack.acked)
}

// A dispatch failure that has exhausted the redelivery cap is given up on (acked)
// rather than looped forever.
func TestDispatchGivesUpAtMaxDeliver(t *testing.T) {
	n := &fakeNotifier{err: errors.New("smtp timeout")}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))

	assert.Equal(t, 1, n.calls)
	assert.Equal(t, 1, ack.acked, "an event past the redelivery cap should be given up (acked)")
	assert.Equal(t, 0, ack.naked)
}

// The LogNotifier is the always-succeeding baseline: it never fails, so it never
// triggers redelivery.
func TestLogNotifierNeverFails(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "tenant1")
	err := NewLogNotifier().Notify(ctx, &dmmodel.AlarmStateChangeEvent{
		EventType:  dmmodel.AlarmEventRaised,
		AlarmToken: "alarm-1",
		Severity:   "CRITICAL",
		State:      "ACTIVE",
	})
	assert.Nil(t, err)
}
