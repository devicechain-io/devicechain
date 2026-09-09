// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// alarmPublishAttempts bounds the LOCAL retry of an alarm state-change publish.
	//
	// 🔴 IT IS LOCAL BECAUSE NOTHING ELSE WILL RETRY THIS. The hop before it — the
	// raise-alarm consumer — retries by leaving its message unacked and letting
	// JetStream redeliver. There is no such lever here: the transition has already
	// committed and its caller (the edge integrator, or an operator mutation) has
	// already returned, so the only attempts this event will ever get are the ones
	// made inside this call.
	alarmPublishAttempts = 3
	// alarmPublishBackoff paces those attempts. Short, because a caller is held while
	// it runs — the integrator's consumer goroutine, or an operator's mutation.
	alarmPublishBackoff = 100 * time.Millisecond
)

// AlarmEventWriter is the concrete, NATS-backed implementation of
// model.AlarmEventPublisher (ADR-041): it marshals an alarm state-change event and
// publishes it to the alarm-events subject. It lives in the processor layer (which
// owns the messaging writer) and is injected into the shared *Api at wiring time, so
// the model layer stays free of a messaging dependency (dependency inversion).
//
// The tenant-scoped writer derives the subject from the tenant already present in the
// caller's context (the raise-alarm consumer applies each edge under the event's
// tenant; an operator mutation runs under the request's tenant), so no tenant plumbing
// is needed here.
//
// 🔴 A FAILED PUBLISH IS NOT A DROPPED NOTIFICATION, IT IS AN ALARM NOBODY IS PAGED
// ABOUT. The row has already transitioned, so the outcome of a swallowed failure is an
// alarm that is ACTIVE in the database, absent from the bus, and invisible everywhere
// else: notification-management consumes alarm-events with DeliverNew, so its durable's
// cursor only ever advances over messages that were actually published, and no reconciler
// walks alarm rows looking for transitions the stream never carried. There is nothing to
// re-query, because the thing to re-query was never written. So the publish is retried
// (alarmPublishAttempts) and, if it still cannot be made, DEAD-LETTERED (ADR-024) with a
// counter — the same arm the hop before this one already uses, for the same reason.
//
// PublishAlarmEvent still returns no error: the interface is fire-and-forget on purpose,
// because a broker fault must never fail or retry the DB transition that produced it. What
// changes is that giving up is now RECORDED rather than logged and forgotten.
type AlarmEventWriter struct {
	writer messaging.MessageWriter

	// marshal renders the event for the wire. It is a field rather than a direct call to
	// proto.MarshalAlarmStateChangeEvent so the marshal-failure arm — which protobuf will
	// essentially never take on a well-formed event — is reachable from a test. An arm no
	// test can enter is exactly how this hop's publish-failure arm came to be missing.
	marshal func(*model.AlarmStateChangeEvent) ([]byte, error)

	// dead records an alarm transition whose event could not be published (ADR-024). Nil
	// when no dead-letter writer is configured, in which case the failure is logged as
	// before — the pre-wiring default, never the steady state.
	dead *deadletter.Sink
	// area names this service on the letters it writes, read once at construction so the
	// failure path never dereferences anything.
	area string

	deadLettered   prometheus.Counter
	deadLetterLost prometheus.Counter
}

// NewAlarmEventWriter builds an alarm-event publisher over the given writer, with the
// ADR-024 arm behind it. dead may be nil (no dead-letter stream configured).
func NewAlarmEventWriter(ms *core.Microservice, writer messaging.MessageWriter,
	dead deadletter.Writer) *AlarmEventWriter {
	w := &AlarmEventWriter{
		writer:  writer,
		marshal: proto.MarshalAlarmStateChangeEvent,
		area:    ms.FunctionalArea,
		deadLettered: ms.NewCounter("alarm_event_dead_lettered_total",
			"Alarm state-change events that could not be published to the alarm-events "+
				"stream and were written to the dead-letter stream instead. Each one is an "+
				"alarm transition that reached the database but not the bus, so nobody was "+
				"paged about it — it is visible to an operator rather than only logged."),
		deadLetterLost: ms.NewCounter("alarm_event_dead_letter_lost_total",
			"Alarm state-change events that could be neither published NOR dead-lettered, "+
				"so the transition is recorded nowhere but the alarm row itself."),
	}
	if dead != nil {
		w.dead = deadletter.NewSink(dead, func(error) { w.deadLetterLost.Inc() })
	}
	return w
}

// PublishAlarmEvent marshals and publishes an alarm state-change event. It never returns
// an error (the interface is fire-and-forget); a failure it cannot recover from is
// dead-lettered and counted rather than swallowed.
func (w *AlarmEventWriter) PublishAlarmEvent(ctx context.Context, event *model.AlarmStateChangeEvent) {
	bytes, err := w.marshal(event)
	if err != nil {
		// Unprocessable, not exhausted: retrying cannot make an event that will not
		// marshal marshal. There is no payload to carry — the bytes are what failed.
		log.Error().Err(err).Str("alarm", event.AlarmToken).Msg("Unable to marshal alarm state-change event")
		w.deadLetter(ctx, event, deadletter.ReasonUnprocessable, 0, nil, err)
		return
	}

	// 🔑 attempts IS COUNTED, NOT ASSUMED. It goes onto the letter, where it is the
	// difference between "the broker refused this three times" and "the caller went away
	// after the first" — so the loop must not be left early by pinning it to the cap.
	attempts, giveUp := 0, false
	for attempts < alarmPublishAttempts && !giveUp {
		attempts++
		if err = w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err == nil {
			return
		}
		if attempts < alarmPublishAttempts {
			select {
			case <-time.After(alarmPublishBackoff):
			case <-ctx.Done():
				// The caller is gone, so further attempts under its context would only
				// fail on the cancellation rather than on the broker. The dead-letter
				// arm detaches and files the transition anyway.
				giveUp = true
			}
		}
	}

	// Log with the alarm identity here (the writer's HandleResponse logs only the
	// subject) so a dropped transition on a specific alarm is diagnosable.
	log.Error().Err(err).Str("alarm", event.AlarmToken).Str("event", event.EventType.String()).
		Int("attempts", attempts).Msg("Unable to publish alarm state-change event; dead-lettering")
	w.deadLetter(ctx, event, deadletter.ReasonExhausted, attempts, bytes, err)
}

// deadLetter records an alarm transition whose event never reached the bus.
//
// The letter is filed as KindNotification rather than as a kind of its own: the operator
// question it answers is the one that kind already exists for — which alarms did nobody
// get told about — and Source is what says this one died a hop earlier than the
// notification service's own letters. Growing the vocabulary would have split that one
// list in two without changing the question being asked of it.
func (w *AlarmEventWriter) deadLetter(ctx context.Context, event *model.AlarmStateChangeEvent,
	reason deadletter.Reason, attempts int, payload []byte, cause error) {
	if w.dead == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	if err := w.dead.Write(ctx, deadletter.Envelope{
		Kind:   deadletter.KindNotification,
		Reason: reason,
		Source: w.area,
		Summary: "an alarm state-change event could not be published, so the transition is " +
			"recorded on the alarm row but reached no subscriber and paged nobody",
		Detail:     detail,
		Attempts:   attempts,
		Reference:  event.AlarmToken,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}); err != nil {
		log.Error().Err(err).Str("alarm", event.AlarmToken).Str("event", event.EventType.String()).
			Msg("LOST alarm state-change event: it could be neither published nor dead-lettered.")
		return
	}
	w.deadLettered.Inc()
}
