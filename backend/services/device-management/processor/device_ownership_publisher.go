// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// DeviceOwnershipWriter is the NATS-backed model.DeviceOwnershipPublisher (ADR-081): it
// publishes a device's complete current anchor set on the per-tenant device-ownership
// subject, for the ownership drain (model.DrainOwnershipFacts).
//
// 🔴 IT RETURNS ITS FAILURE, UNLIKE THE OTHER FACT WRITERS. They are called after a commit by
// a write that has nothing left to do with an error, so they log and count it. This one is
// called by the drain, which deletes a queued fact only once the broker has acknowledged it:
// a failure swallowed here would be a fact deleted unsent — the device's old owner keeping
// its live view for as long as the device stays quiet, which is the loss the queue exists to
// prevent. It still counts every failure, so a broker refusing ownership facts shows on the
// same series as before.
//
// The context it is handed is the drain's: the tenant of the queued row and nothing of any
// caller's, so the subject is the tenant's and the fact is never attributed to, or bounded
// by, the member whose write queued it.
type DeviceOwnershipWriter struct {
	writer   messaging.MessageWriter
	failures prometheus.Counter
}

// NewDeviceOwnershipWriter builds an ownership-fact publisher over writer, counting every
// fact it fails to publish on failures (nil disables the count).
func NewDeviceOwnershipWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DeviceOwnershipWriter {
	return &DeviceOwnershipWriter{writer: writer, failures: failures}
}

// PublishDeviceOwnershipChanged marshals and publishes one ownership fact, and reports
// whether the broker acknowledged it.
func (w *DeviceOwnershipWriter) PublishDeviceOwnershipChanged(ctx context.Context, event *model.DeviceOwnershipChangedEvent) error {
	bytes, err := proto.MarshalDeviceOwnershipChangedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Msg("Unable to marshal device ownership-changed event")
		countFactFailure(w.failures)
		return err
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Warn().Err(err).Str("device", event.DeviceToken).
			Msg("Unable to publish device ownership-changed event; it stays queued and is published on a later pass")
		countFactFailure(w.failures)
		return err
	}
	return nil
}
