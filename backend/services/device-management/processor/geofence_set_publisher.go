// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// factHeadroom is the slack left between a fence-set fact's marshalled payload and the
// broker's per-message ceiling.
//
// The ceiling the deployment configures (infrastructure.nats.streamMaxMsgSize) is compared
// by the broker against a message the broker measures, which is not only the bytes handed
// to WriteMessages: the subject and any headers the writer attaches ride along, and NATS
// applies its own max_payload to the same message. Sizing against the raw payload alone
// would put the switch-over point a few hundred bytes on the wrong side of the wall — and
// the failure mode of being slightly wrong is the one this whole file exists to remove (a
// refused publish, logged and swallowed). 4 KiB is far more than any envelope this stream
// carries and costs nothing: it moves the pointer-fact threshold by 0.4% of a 1 MiB
// ceiling.
const factHeadroom = 4 << 10

// GeoFenceSetWriter is the concrete, NATS-backed implementation of
// model.GeoFenceSetPublisher (ADR-078): it marshals a newly-minted fence set and publishes
// it to the geofence-set subject. Like the roster, detection-rules, device-attribute and
// entity-deleted writers it lives in the processor layer (which owns the messaging writer)
// and is injected into the shared *Api at wiring time, keeping the model free of a
// messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the tenant
// in the caller's context (the fence write runs under the request's tenant), so no tenant
// plumbing is needed here — and so a fence set can never be published onto another
// tenant's subject by a bug in this file. A marshal or publish failure is logged and
// swallowed; it must never fail or roll back the authoring action, which is the source of
// truth.
//
// 🔴 "BEST-EFFORT" USED TO INCLUDE A CASE THAT WAS NOT AN ACCIDENT AND WAS NOT RARE. A
// tenant at the documented authoring ceiling — model.MaxGeoFencesPerTenant fences of
// model.MaxGeoFenceVertices vertices each — marshals to more than the 1 MiB per-message
// ceiling, so every one of its fence edits produced a publish the broker refused, a log
// line nobody was reading, and a mutation that returned 200. Downstream, containment
// reported a counted eval error for that tenant on every location event, indefinitely.
// That is not a wire hiccup; it is a documented configuration failing by construction.
//
// maxPayload is what closes it. Over the ceiling the writer publishes the POINTER form of
// the fact (model.GeoFenceSetMintedEvent.FencesOmitted) — version and mintedAt, no fences —
// which the consumer resolves through the frozen archive the same way a replay of last
// week's events already does. The fact is always small enough to send, so a minted version
// is always announced, and the publish path is TOTAL rather than best-effort with a hole.
type GeoFenceSetWriter struct {
	writer messaging.MessageWriter
	// maxPayload is the broker's per-message ceiling in bytes, minus factHeadroom. A
	// non-positive value disables the check, which is the right default for a caller that
	// does not know the ceiling (tests): the behaviour is then exactly what it was before
	// this field existed.
	maxPayload int
	// omitted counts fence-set facts published in the pointer form. Nil-safe.
	omitted prometheus.Counter
}

// NewGeoFenceSetWriter builds a fence-set publisher over the given writer.
//
// maxMsgSize is the broker's configured per-message ceiling
// (infrastructure.nats.streamMaxMsgSize); a non-positive value disables the size check.
// omitted counts pointer facts and may be nil.
func NewGeoFenceSetWriter(writer messaging.MessageWriter, maxMsgSize int32, omitted prometheus.Counter) *GeoFenceSetWriter {
	limit := 0
	if maxMsgSize > 0 {
		limit = int(maxMsgSize) - factHeadroom
		if limit < 1 {
			// A ceiling smaller than the headroom is a misconfiguration, not a signal to
			// send everything as a pointer: fall back to no check rather than degrade every
			// tenant's fences to an archive read on a value nobody meant to set.
			limit = 0
		}
	}
	return &GeoFenceSetWriter{writer: writer, maxPayload: limit, omitted: omitted}
}

// PublishGeoFenceSet marshals and publishes one fence-set fact. It never returns an error
// (the interface is fire-and-forget); failures are logged.
//
// A set whose marshalled fact exceeds the broker's ceiling is published as a POINTER fact
// instead of being dropped. Note the ORDER: the full fact is marshalled first and its real
// size measured, rather than the fence count or vertex total being used as a proxy. The
// thing the broker refuses is bytes, and the geometry documents are author-written JSON of
// no predictable size, so anything short of marshalling is a guess that is wrong in both
// directions.
func (w *GeoFenceSetWriter) PublishGeoFenceSet(ctx context.Context, event *model.GeoFenceSetMintedEvent) {
	bytes, err := model.MarshalGeoFenceSetMintedEvent(event)
	if err != nil {
		log.Error().Err(err).Int32("version", event.Version).
			Msg("Unable to marshal geofence set event")
		return
	}
	if w.maxPayload > 0 && len(bytes) > w.maxPayload {
		pointer := &model.GeoFenceSetMintedEvent{
			Version:       event.Version,
			Fences:        []model.GeoFenceSnapshotRef{},
			MintedAt:      event.MintedAt,
			FencesOmitted: true,
		}
		bytes, err = model.MarshalGeoFenceSetMintedEvent(pointer)
		if err != nil {
			log.Error().Err(err).Int32("version", event.Version).
				Msg("Unable to marshal geofence set pointer event")
			return
		}
		if w.omitted != nil {
			w.omitted.Inc()
		}
		log.Warn().Int32("version", event.Version).Int("fences", len(event.Fences)).
			Int("maxPayloadBytes", w.maxPayload).
			Msg("Geofence set is too large for one broker message; publishing a pointer fact instead. Consumers resolve its fences from the frozen archive, which costs one cross-service read per fence edit for this tenant.")
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Int32("version", event.Version).
			Msg("Unable to publish geofence set event")
	}
}
