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

// DetectionRulesPublishedWriter is the concrete, NATS-backed implementation of
// model.DetectionRulesPublishedPublisher (ADR-051 slice 4b-3): it marshals a
// detection-rules-published event and publishes it to the detection-rules-published
// subject. Like the alarm and entity-deleted writers it lives in the processor layer
// (which owns the messaging writer) and is injected into the shared *Api at wiring
// time, keeping the model free of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the publish runs under the request's tenant), so no
// tenant plumbing is needed here. A marshal or publish failure is logged and COUNTED,
// never surfaced — it must never fail or retry the profile publish, which is the source
// of truth. Emission is at-most-once (ADR-044 async-fact posture): a delivered fact is
// durably persisted by event-processing, and a fact that never reaches the stream is
// repaired by event-processing's reconcile against this service (at the start of its
// leadership term and every five minutes); it is not replayed.
type DetectionRulesPublishedWriter struct {
	writer messaging.MessageWriter
	// failures counts facts this writer could not put on the wire, for any reason. Nil-safe.
	failures prometheus.Counter
}

// NewDetectionRulesPublishedWriter builds a detection-rules publisher over the writer,
// counting every fact it fails to publish on failures (nil disables the count).
func NewDetectionRulesPublishedWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DetectionRulesPublishedWriter {
	return &DetectionRulesPublishedWriter{writer: writer, failures: failures}
}

// PublishDetectionRulesPublished marshals and publishes a detection-rules-published
// event. It never returns an error (the interface is fire-and-forget); failures are
// logged and counted.
func (w *DetectionRulesPublishedWriter) PublishDetectionRulesPublished(ctx context.Context, event *model.DetectionRulesPublishedEvent) {
	bytes, err := proto.MarshalDetectionRulesPublishedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("profileVersion", event.ProfileVersionToken).
			Msg("Unable to marshal detection-rules-published event")
		countFactFailure(w.failures)
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("profileVersion", event.ProfileVersionToken).Int("rules", len(event.Rules)).
			Msg("Unable to publish detection-rules-published event; the detection engine picks the change up at its next reconcile against this service.")
		countFactFailure(w.failures)
	}
}

// countFactFailure increments a fact writer's failure counter when one is wired. Called only on
// the paths that actually failed, never optimistically ahead of a write, so the number means what
// the metric's help string says it means.
func countFactFailure(failures prometheus.Counter) {
	if failures != nil {
		failures.Inc()
	}
}
