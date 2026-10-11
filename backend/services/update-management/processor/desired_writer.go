// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package processor is update-management's side of the device channels: the writer that puts an
// assignment, a download grant or a cancel on a device's desired-state subject, the durable
// consumer of what devices report, and the deadline sweep that moves attempts nobody reported on.
package processor

import (
	"context"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// DesiredWriter is the NATS-backed model.DesiredPublisher. The tenant comes from the caller's
// context and the device token is spliced into the subject by the underlying writer, which
// validates it.
//
// Delivery is best-effort and at-most-once, and deliberately carries no dedup id: a device that
// syncs again must be sent its current assignment again, and a dedup window would swallow exactly
// that resend. A message that never reaches the stream is repaired by the device's next sync, which
// is why the caller logs a failure and does not fail the operation that caused it.
type DesiredWriter struct {
	writer messaging.MessageWriter
	// failures counts messages this writer could not put on the wire. Nil-safe.
	failures prometheus.Counter
}

// NewDesiredWriter builds the publisher over a writer for the device-desired stream.
func NewDesiredWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DesiredWriter {
	return &DesiredWriter{writer: writer, failures: failures}
}

// PublishDesired implements model.DesiredPublisher.
func (w *DesiredWriter) PublishDesired(ctx context.Context, deviceToken string, body []byte) error {
	if err := w.writer.WriteToDevice(ctx, deviceToken, messaging.Message{Value: body}); err != nil {
		if w.failures != nil {
			w.failures.Inc()
		}
		return err
	}
	return nil
}
