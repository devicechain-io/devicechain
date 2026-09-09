// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// alarmEventRecorder stands in for the alarm-events writer. It fails the first
// failures calls and records everything it is handed, including the tenant on the
// context — the real writer scopes its subject from that and is fail-closed without
// one, so a publish made under the wrong context writes nothing while looking
// identical to one that succeeded.
type alarmEventRecorder struct {
	failures int
	err      error
	calls    int
	payloads [][]byte
	tenants  []string
}

func (w *alarmEventRecorder) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.calls++
	tenant, _ := core.TenantFromContext(ctx)
	w.tenants = append(w.tenants, tenant)
	if w.calls <= w.failures {
		return w.err
	}
	for _, m := range msgs {
		w.payloads = append(w.payloads, m.Value)
	}
	return nil
}

func (w *alarmEventRecorder) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *alarmEventRecorder) HandleResponse(error) {}

// refusingDeadWriter is a dead-letter stream that is down too — the case where the
// transition can be recorded nowhere at all.
type refusingDeadWriter struct{ calls int }

func (d *refusingDeadWriter) WriteMessages(context.Context, ...messaging.Message) error {
	d.calls++
	return errors.New("the dead-letter stream is away as well")
}

// alarmWriterFor builds the publisher through its REAL constructor, so the counters,
// the sink and the area name are wired the way main.go wires them rather than by hand.
// A struct-literal Microservice has no metrics registry, which makes its counters
// unregistered but fully functional — they still count.
func alarmWriterFor(t *testing.T, w messaging.MessageWriter, dead deadletter.Writer) *AlarmEventWriter {
	t.Helper()
	return NewAlarmEventWriter(&core.Microservice{FunctionalArea: "device-management"}, w, dead)
}

func alarmEvent() *model.AlarmStateChangeEvent {
	return &model.AlarmStateChangeEvent{
		EventType:      model.AlarmEventRaised,
		AlarmToken:     "alarm-1",
		OriginatorType: "device",
		OriginatorId:   42,
		AlarmKey:       "acme/p@1/r1",
		MetricKey:      "temperature",
		State:          "ACTIVE",
		Severity:       "CRITICAL",
		RaisedTime:     time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		OccurredTime:   time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

// 🔴 THIS IS THE LAST HOP OF THE ALARM FLOW, AND UNTIL NOW IT WAS THE ONLY ONE THAT
// ANSWERED "I could not pass this on" WITH SILENCE. The hop before it — the raise-alarm
// consumer — retries and then dead-letters, because a lost edge is a genuine loss for an
// edge-triggered producer. The consequence here is strictly worse: the alarm row has
// already transitioned, so a swallowed publish leaves an alarm ACTIVE in the database
// that nobody is paged about and that no counter, letter or later event mentions.
//
// It cannot be recovered downstream either. notification-management reads alarm-events
// through a durable whose cursor only ever advances over messages that were actually
// published, and nothing walks alarm rows for transitions the stream never carried — so
// "the subscriber can re-query" is not available for an event that was never written.
func TestAnAlarmEventThatCannotBePublishedIsDeadLetteredAndCounted(t *testing.T) {
	pub := &alarmEventRecorder{failures: alarmPublishAttempts, err: errors.New("the broker is away")}
	dead := &deadRecorder{}
	w := alarmWriterFor(t, pub, dead)
	ctx := core.WithTenant(context.Background(), "acme")

	w.PublishAlarmEvent(ctx, alarmEvent())

	if pub.calls != alarmPublishAttempts {
		t.Fatalf("the publish was attempted %d times, want %d: nothing else will ever retry "+
			"this event, so the local retry is the only attempt it gets", pub.calls, alarmPublishAttempts)
	}
	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters for an alarm transition that reached no subscriber, want 1",
			len(dead.msgs))
	}
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if e.Kind != deadletter.KindNotification || e.Reason != deadletter.ReasonExhausted {
		t.Fatalf("the letter does not say what it is or why: kind=%q reason=%q", e.Kind, e.Reason)
	}
	if e.Source != "device-management" {
		t.Fatalf("source = %q: the letter must name the hop that gave up, since the same kind "+
			"is written by notification-management one hop later", e.Source)
	}
	if e.Reference != "alarm-1" {
		t.Fatalf("reference = %q, want the alarm token — it is the only thing that lets an "+
			"operator find the transition nobody was paged about", e.Reference)
	}
	if e.Attempts != alarmPublishAttempts {
		t.Fatalf("attempts = %d, want %d", e.Attempts, alarmPublishAttempts)
	}
	// The payload is the wire bytes, so the letter carries the event itself rather than a
	// description of it: an operator can decode exactly what the subscriber never saw.
	want, err := proto.MarshalAlarmStateChangeEvent(alarmEvent())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(e.Payload) != string(want) {
		t.Fatalf("the letter does not carry the event that was lost (%d payload bytes, want %d)",
			len(e.Payload), len(want))
	}
	if dead.tenants[0] != "acme" {
		t.Fatalf("the letter was written under tenant %q; the real writer is fail-closed "+
			"without one and would have written nothing", dead.tenants[0])
	}
	if got := testutil.ToFloat64(w.deadLettered); got != 1 {
		t.Fatalf("alarm_event_dead_lettered_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(w.deadLetterLost); got != 0 {
		t.Fatalf("alarm_event_dead_letter_lost_total = %v, want 0: the letter was written", got)
	}
}

// The counterweight. Recording a give-up is only worth anything while a publish that
// worked stays a publish that worked — a hop that filed a letter every time would bury
// the one an operator has to act on.
func TestAPublishedAlarmEventIsNotDeadLettered(t *testing.T) {
	pub := &alarmEventRecorder{}
	dead := &deadRecorder{}
	w := alarmWriterFor(t, pub, dead)

	w.PublishAlarmEvent(core.WithTenant(context.Background(), "acme"), alarmEvent())

	if pub.calls != 1 || len(pub.payloads) != 1 {
		t.Fatalf("a successful publish must happen exactly once: calls=%d payloads=%d",
			pub.calls, len(pub.payloads))
	}
	if pub.tenants[0] != "acme" {
		t.Fatalf("the event was published under tenant %q", pub.tenants[0])
	}
	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters for an event that was published", len(dead.msgs))
	}
	if got := testutil.ToFloat64(w.deadLettered); got != 0 {
		t.Fatalf("alarm_event_dead_lettered_total = %v, want 0", got)
	}
	back, err := proto.UnmarshalAlarmStateChangeEvent(pub.payloads[0])
	if err != nil {
		t.Fatalf("what reached the stream does not decode: %v", err)
	}
	if back.AlarmToken != "alarm-1" || back.EventType != model.AlarmEventRaised {
		t.Fatalf("the published event is not the one that was handed in: %+v", back)
	}
}

// A broker blip is not a give-up. The retry exists to turn one into a publish rather than
// into a letter, and a letter written for a transient fault that resolved on the next
// attempt would be a false report of an alarm nobody was paged about.
func TestAnAlarmEventThatSucceedsOnRetryIsNotDeadLettered(t *testing.T) {
	pub := &alarmEventRecorder{failures: alarmPublishAttempts - 1, err: errors.New("no responders")}
	dead := &deadRecorder{}
	w := alarmWriterFor(t, pub, dead)

	w.PublishAlarmEvent(core.WithTenant(context.Background(), "acme"), alarmEvent())

	if len(pub.payloads) != 1 {
		t.Fatalf("the event never reached the stream after %d attempts", pub.calls)
	}
	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters for an event that was published on a retry", len(dead.msgs))
	}
}

// 🔴 UNPROCESSABLE, NOT EXHAUSTED, AND STILL RECORDED. An event that will not marshal
// cannot be made to marshal by trying again, so retrying it would only burn the caller's
// time — but the transition still happened and still reached nobody, which is the same
// operator-visible outcome as a broker that refused it.
func TestAnAlarmEventThatCannotBeMarshalledIsDeadLetteredAsUnprocessable(t *testing.T) {
	pub := &alarmEventRecorder{}
	dead := &deadRecorder{}
	w := alarmWriterFor(t, pub, dead)
	w.marshal = func(*model.AlarmStateChangeEvent) ([]byte, error) {
		return nil, errors.New("this event cannot be rendered")
	}

	w.PublishAlarmEvent(core.WithTenant(context.Background(), "acme"), alarmEvent())

	if pub.calls != 0 {
		t.Fatalf("an unmarshallable event was published %d times", pub.calls)
	}
	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters, want 1", len(dead.msgs))
	}
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if e.Reason != deadletter.ReasonUnprocessable {
		t.Fatalf("reason = %q, want %q: nothing was retried, so nothing was exhausted",
			e.Reason, deadletter.ReasonUnprocessable)
	}
	if len(e.Payload) != 0 {
		t.Fatalf("the letter carries %d payload bytes; the bytes are what failed to exist",
			len(e.Payload))
	}
	if got := testutil.ToFloat64(w.deadLettered); got != 1 {
		t.Fatalf("alarm_event_dead_lettered_total = %v, want 1", got)
	}
}

// 🔴 THE LOSS COUNTER IS THE ONE AN OPERATOR ALERTS ON, so it must move on the one
// outcome it names: the transition could be neither published nor filed. Counting it
// here rather than off the sink's returned error is what keeps the counter from drifting
// away from the condition it claims to measure.
func TestAnAlarmEventThatCanBeNeitherPublishedNorFiledIsCountedAsLost(t *testing.T) {
	pub := &alarmEventRecorder{failures: alarmPublishAttempts, err: errors.New("the broker is away")}
	dead := &refusingDeadWriter{}
	w := alarmWriterFor(t, pub, dead)

	w.PublishAlarmEvent(core.WithTenant(context.Background(), "acme"), alarmEvent())

	if dead.calls == 0 {
		t.Fatal("no attempt was made to file the letter")
	}
	if got := testutil.ToFloat64(w.deadLetterLost); got != 1 {
		t.Fatalf("alarm_event_dead_letter_lost_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(w.deadLettered); got != 0 {
		t.Fatalf("alarm_event_dead_lettered_total = %v, want 0: no letter was written", got)
	}
}

// 🔴 A CANCELLED CALLER IS NOT A REASON TO STOP RECORDING. The transition committed
// before the publish was attempted, so an operator whose request was cancelled — or a
// consumer being shut down mid-rollout — still leaves an alarm that reached nobody. The
// retry stops (further attempts under a dead context would fail on the cancellation
// rather than on the broker), the arm files the letter under a detached context, and the
// attempt count on the letter says how far it actually got rather than the cap.
func TestACancelledCallerStopsTheRetryButStillFilesTheLetter(t *testing.T) {
	pub := &alarmEventRecorder{failures: alarmPublishAttempts, err: errors.New("the broker is away")}
	dead := &deadRecorder{}
	w := alarmWriterFor(t, pub, dead)
	ctx, cancel := context.WithCancel(core.WithTenant(context.Background(), "acme"))
	cancel()

	w.PublishAlarmEvent(ctx, alarmEvent())

	if pub.calls != 1 {
		t.Fatalf("the publish was attempted %d times under a cancelled context, want 1", pub.calls)
	}
	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters, want 1: the cancellation belongs to the caller, not "+
			"to the record of an alarm nobody was paged about", len(dead.msgs))
	}
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if e.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1: the letter must say how far the publish actually "+
			"got, not that it spent the whole retry budget", e.Attempts)
	}
	if dead.tenants[0] != "acme" {
		t.Fatalf("the letter was written under tenant %q; the sink detaches the caller's "+
			"cancellation but must keep its tenant, which is what scopes the subject",
			dead.tenants[0])
	}
}

// A publisher wired without a dead-letter stream must still publish, and must not panic
// on the failure path — that is the pre-wiring default, and the nil sink is the thing
// standing between it and a crash in the one moment the arm exists for.
func TestAnAlarmPublisherWithNoDeadLetterStreamStillPublishes(t *testing.T) {
	pub := &alarmEventRecorder{failures: alarmPublishAttempts, err: errors.New("the broker is away")}
	w := alarmWriterFor(t, pub, nil)

	w.PublishAlarmEvent(core.WithTenant(context.Background(), "acme"), alarmEvent())

	if pub.calls != alarmPublishAttempts {
		t.Fatalf("attempts = %d, want %d", pub.calls, alarmPublishAttempts)
	}
}
