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
// subject after every write that may have changed it. Like the other fact writers it is
// injected into the shared *Api at wiring time and never fails the write that called it:
// a marshal or publish failure is logged and counted, and the device's next resolved
// event carries the same anchors, which is what a consumer falls back on.
//
// The context it is handed is the service's own (model.ownershipReadContext): the tenant
// of the write and nothing of the caller's, so the subject is the tenant's and the fact
// is never attributed to, or bounded by, the member whose write caused it.
type DeviceOwnershipWriter struct {
	writer   messaging.MessageWriter
	failures prometheus.Counter
}

// NewDeviceOwnershipWriter builds an ownership-fact publisher over writer, counting every
// fact it fails to publish on failures (nil disables the count).
func NewDeviceOwnershipWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DeviceOwnershipWriter {
	return &DeviceOwnershipWriter{writer: writer, failures: failures}
}

// PublishDeviceOwnershipChanged marshals and publishes one ownership fact.
func (w *DeviceOwnershipWriter) PublishDeviceOwnershipChanged(ctx context.Context, event *model.DeviceOwnershipChangedEvent) {
	bytes, err := proto.MarshalDeviceOwnershipChangedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Msg("Unable to marshal device ownership-changed event")
		countFactFailure(w.failures)
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).
			Msg("Unable to publish device ownership-changed event; consumers pick the device's owners up from its next resolved event.")
		countFactFailure(w.failures)
	}
}
