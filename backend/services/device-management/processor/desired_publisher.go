// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// DesiredConfigurationWriter is the NATS-backed model.DeviceDesiredPublisher: it frames a
// configuration revision as the device wire's config.revision message and publishes it on the
// device's own device-desired subject. The tenant comes from the caller's context, and the
// device token is spliced into the subject by the writer, which validates it.
//
// Delivery is best-effort and at-most-once, and deliberately carries no dedup id: a device
// that syncs again must be sent the current revision again, and a dedup window would swallow
// exactly that resend. A message that never reaches the stream is repaired by the device's next
// sync. Failures are logged and counted, never returned, because they must not fail the write
// that minted the revision.
type DesiredConfigurationWriter struct {
	writer messaging.MessageWriter
	// failures counts revisions this writer could not put on the wire. Nil-safe.
	failures prometheus.Counter
}

// NewDesiredConfigurationWriter builds the publisher over a writer for the device-desired stream.
func NewDesiredConfigurationWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DesiredConfigurationWriter {
	return &DesiredConfigurationWriter{writer: writer, failures: failures}
}

// PublishDesiredConfiguration implements model.DeviceDesiredPublisher.
func (w *DesiredConfigurationWriter) PublishDesiredConfiguration(ctx context.Context, deviceToken string,
	rev *model.DeviceConfigurationRevision) {
	body, err := ota.EncodeConfigRevision(rev.Revision, rev.Digest, rev.Document)
	if err != nil {
		// A revision that cannot be framed intact (a stored document that no longer hashes to its
		// digest, or one over the wire cap) is never sent in a form the device would mis-handle.
		log.Error().Err(err).Str("device", deviceToken).Int64("revision", rev.Revision).
			Msg("Not delivering a configuration revision that cannot be framed intact.")
		countFactFailure(w.failures)
		return
	}
	if err := w.writer.WriteToDevice(ctx, deviceToken, messaging.Message{Value: body}); err != nil {
		log.Error().Err(err).Str("device", deviceToken).Int64("revision", rev.Revision).
			Msg("Unable to publish a configuration revision; the device picks it up at its next sync.")
		countFactFailure(w.failures)
	}
}
