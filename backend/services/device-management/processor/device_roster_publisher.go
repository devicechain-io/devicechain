// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// DeviceRosterWriter is the concrete, NATS-backed implementation of
// model.DeviceRosterPublisher (ADR-051 slice 4c-2): it marshals a device-roster event
// and publishes it to the device-roster subject. Like the detection-rules and
// entity-deleted writers it lives in the processor layer (which owns the messaging
// writer) and is injected into the shared *Api at wiring time, keeping the model free
// of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the device write runs under the request's tenant), so
// no tenant plumbing is needed here. A marshal or publish failure is logged and COUNTED,
// never surfaced — it must never fail or retry the device create/update, which is the
// source of truth. Emission is at-most-once (ADR-044 async-fact posture): a delivered fact
// is durably persisted by event-processing, and a fact that never reaches the stream is
// repaired by event-processing's reconcile against this service (at the start of its
// leadership term and every five minutes); it is not replayed.
type DeviceRosterWriter struct {
	writer messaging.MessageWriter
	// failures counts facts this writer could not put on the wire, for any reason. Nil-safe.
	failures prometheus.Counter
	// connected reports whether the broker connection is up (WithConnected). Nil means it is
	// unknown, and no refusal is reported as model.ErrRosterBrokerUnreachable.
	connected func() bool
}

// WithConnected tells the writer how to learn whether its broker connection is up, so a publish
// refused while it is down is reported as model.ErrRosterBrokerUnreachable and a per-device
// fan-out stops sending facts that would each wait out the same unanswered publish.
func (w *DeviceRosterWriter) WithConnected(connected func() bool) *DeviceRosterWriter {
	w.connected = connected
	return w
}

// NewDeviceRosterWriter builds a device-roster publisher over the given writer, counting
// every fact it fails to publish on failures (nil disables the count).
func NewDeviceRosterWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DeviceRosterWriter {
	return &DeviceRosterWriter{writer: writer, failures: failures}
}

// PublishDeviceRoster marshals and publishes a device-roster event. Failures are logged and
// counted. It returns the error of a publish the broker did not accept, wrapped in
// model.ErrRosterBrokerUnreachable when the connection is down at that moment, so a caller
// sending one fact per device can stop sending facts that would each wait out the publish
// ceiling. A refusal on a live connection may be transient and is returned unwrapped. A marshal
// failure is particular to its event, so it is counted and answered as nil.
func (w *DeviceRosterWriter) PublishDeviceRoster(ctx context.Context, event *model.DeviceRosterEvent) error {
	bytes, err := proto.MarshalDeviceRosterEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Msg("Unable to marshal device-roster event")
		countFactFailure(w.failures)
		return nil
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("profile", event.ProfileToken).
			Msg("Unable to publish device-roster event; the detection engine picks the change up at its next reconcile against this service.")
		countFactFailure(w.failures)
		if w.connected != nil && !w.connected() {
			return fmt.Errorf("%w: %w", model.ErrRosterBrokerUnreachable, err)
		}
		return err
	}
	return nil
}
