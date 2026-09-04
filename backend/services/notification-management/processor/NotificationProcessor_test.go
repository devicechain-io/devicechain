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
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
)

// A parseable scoped subject ({instance}.{tenant}.{suffix}) so the worker can derive
// a per-message tenant context (fail-closed).
const testAlarmSubject = "instance1.tenant1.alarm-events"

// recordingAck records the A3 disposition applied to a message so a test can assert
// whether the worker acked (handled / dropped) it. A transient failure is retried by
// leaving the message UNACKED (acked stays 0), so acked==0 means "left for
// AckWait-paced redelivery" (ADR-030).
type recordingAck struct {
	acked int
}

func (r *recordingAck) Ack() error { r.acked++; return nil }

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
}

// An undecodable payload is a poison message: dropped (acked), never dispatched.
func TestDispatchDropsUndecodablePayload(t *testing.T) {
	n := &fakeNotifier{}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, []byte("not-a-proto"), 1, ack))

	assert.Equal(t, 0, n.calls, "notifier must not run on an undecodable event")
	assert.Equal(t, 1, ack.acked, "poison message should be dropped (acked)")
}

// A transient dispatch failure below the redelivery cap is left UNACKED for
// AckWait-paced redelivery (never an immediate nak that would burn MaxDeliver in ~1.4ms).
func TestDispatchLeavesTransientFailureUnacked(t *testing.T) {
	n := &fakeNotifier{err: errors.New("smtp timeout")}
	np := newTestProcessor(n)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), 1, ack))

	assert.Equal(t, 1, n.calls)
	assert.Equal(t, 0, ack.acked, "a transient failure below the cap must be left unacked (AckWait retry), not acked")
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

// deadRecorder captures what the arm writes so a test can read the letter back.
// 🔴 IT RECORDS THE CONTEXT'S TENANT. The real writer scopes the subject from the context
// and is fail-closed without one, so an arm handed the wrong context writes nothing and
// counts a loss — indistinguishable from success to a fake that ignores its context.
type deadRecorder struct {
	msgs    []messaging.Message
	tenants []string
	err     error
}

func (d *deadRecorder) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if d.err != nil {
		return d.err
	}
	tenant, _ := core.TenantFromContext(ctx)
	for range msgs {
		d.tenants = append(d.tenants, tenant)
	}
	d.msgs = append(d.msgs, msgs...)
	return nil
}

func processorWithDeadLetters(n Notifier, dead deadletter.Writer) *NotificationProcessor {
	np := newTestProcessor(n)
	np.area = "notification-management"
	np.deadLettered = prometheus.NewCounter(prometheus.CounterOpts{Name: "dl_total"})
	np.deadLetterLost = prometheus.NewCounter(prometheus.CounterOpts{Name: "dl_lost_total"})
	np.dead = deadletter.NewSink(dead, func(error) { np.deadLetterLost.Inc() })
	return np
}

func counterOf(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	assert.Nil(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// 🔴 THE ARM. An alarm that reached nobody used to end as a log line, and an alarm nobody
// was paged about is exactly the failure an operator most needs to be able to find.
func TestANotificationThatReachedNobodyIsDeadLettered(t *testing.T) {
	dead := &deadRecorder{}
	np := processorWithDeadLetters(&fakeNotifier{err: errors.New("smtp is down")}, dead)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(),
		msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))

	assert.Len(t, dead.msgs, 1, "no dead letter was written for an alarm that reached nobody")
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	assert.Nil(t, err)
	assert.Equal(t, deadletter.KindNotification, e.Kind)
	assert.Equal(t, "alarm-1", e.Reference, "the letter must name the alarm nobody was paged about")
	assert.Equal(t, messaging.MaxDeliver, e.Attempts)
	assert.Contains(t, e.Detail, "smtp is down", "the delivery error is what makes the letter diagnosable")
	assert.NotEmpty(t, e.Payload)
	assert.Equal(t, "tenant1", dead.tenants[0],
		"the letter was written under the wrong tenant; the real writer is fail-closed on it")
	// The ack still happens: at the cap no redelivery follows, so leaving it unacked
	// would strand the message rather than retry it.
	assert.Equal(t, 1, ack.acked)

	// 🔴 THE COUNTER PAIR READS THE SAME EITHER WAY ROUND unless something asserts it:
	// one says "recorded", the other says "gone", and the alert rests on the second.
	assert.Equal(t, float64(1), counterOf(t, np.deadLettered))
	assert.Equal(t, float64(0), counterOf(t, np.deadLetterLost))
}

// The other half of that pair: a letter that could not be written counts as LOST and NOT
// as written, or the alert that exists for this case never fires.
func TestANotificationThatCannotBeDeadLetteredCountsAsLost(t *testing.T) {
	dead := &deadRecorder{err: errors.New("broker is away")}
	np := processorWithDeadLetters(&fakeNotifier{err: errors.New("smtp is down")}, dead)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(),
		msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))

	assert.Equal(t, float64(1), counterOf(t, np.deadLetterLost))
	assert.Equal(t, float64(0), counterOf(t, np.deadLettered))
	assert.Equal(t, 1, ack.acked, "a lost letter must still ack its source; no redelivery follows")
}

// 🔴 AND NOT BELOW THE CAP — an alarm still being retried has not been given up on.
func TestANotificationBelowTheCapIsNotDeadLettered(t *testing.T) {
	dead := &deadRecorder{}
	np := processorWithDeadLetters(&fakeNotifier{err: errors.New("smtp is down")}, dead)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), 1, ack))

	assert.Empty(t, dead.msgs, "an alarm still being retried was reported as given up on")
	assert.Equal(t, 0, ack.acked)
}

// 🔴 AN UNDECODABLE ENVELOPE STAYS A DROP. It is not work the platform accepted and failed
// to finish; filing it under the subject's tenant would attribute to them a message that
// was never demonstrably theirs.
func TestAnUndecodableAlarmIsNotDeadLettered(t *testing.T) {
	dead := &deadRecorder{}
	np := processorWithDeadLetters(&fakeNotifier{}, dead)
	ack := &recordingAck{}

	np.dispatchOne(context.Background(),
		msgWith(testAlarmSubject, []byte("not protobuf at all"), messaging.MaxDeliver, ack))

	assert.Empty(t, dead.msgs)
	assert.Equal(t, 1, ack.acked)
}

// A processor with no sink — the shape a deployment without the stream has, and the shape
// every other test in this file builds — must still drop rather than panic.
func TestANotificationDropsWithNoDeadLetterSink(t *testing.T) {
	np := newTestProcessor(&fakeNotifier{err: errors.New("smtp is down")})
	ack := &recordingAck{}
	np.dispatchOne(context.Background(),
		msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
	assert.Equal(t, 1, ack.acked)
}
