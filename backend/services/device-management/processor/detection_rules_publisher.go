// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// DetectionRulesPublishedWriter is the concrete, NATS-backed implementation of
// model.DetectionRulesPublishedPublisher (ADR-051 slice 4b-3): it marshals a
// detection-rules-published event and publishes it to the detection-rules-published
// subject. Like the alarm and entity-deleted writers it lives in the processor layer
// (which owns the messaging writer) and is injected into the shared *Api at wiring
// time, keeping the model free of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the publish runs under the request's tenant), so no
// tenant plumbing is needed here. A marshal or publish failure is logged and swallowed —
// it must never fail or retry the profile publish, which is the source of truth. Emission
// is at-most-once (ADR-044 async-fact posture): a delivered fact is durably persisted by
// event-processing (so it survives a restart), but a fact that never reaches the stream is
// recovered by a later publish or the planned reconcile, not by replay.
type DetectionRulesPublishedWriter struct {
	writer messaging.MessageWriter
}

// NewDetectionRulesPublishedWriter builds a detection-rules publisher over the writer.
func NewDetectionRulesPublishedWriter(writer messaging.MessageWriter) *DetectionRulesPublishedWriter {
	return &DetectionRulesPublishedWriter{writer: writer}
}

// PublishDetectionRulesPublished marshals and publishes a detection-rules-published
// event. It never returns an error (the interface is fire-and-forget); failures are
// logged.
func (w *DetectionRulesPublishedWriter) PublishDetectionRulesPublished(ctx context.Context, event *model.DetectionRulesPublishedEvent) {
	bytes, err := proto.MarshalDetectionRulesPublishedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("profileVersion", event.ProfileVersionToken).
			Msg("Unable to marshal detection-rules-published event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("profileVersion", event.ProfileVersionToken).Int("rules", len(event.Rules)).
			Msg("Unable to publish detection-rules-published event")
	}
}
