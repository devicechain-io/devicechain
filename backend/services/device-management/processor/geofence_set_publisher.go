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
// model.MaxGeoFenceVertices positions each — marshals to more than the 1 MiB per-message
// ceiling, so every one of its fence edits produced a publish the broker refused, a log
// line nobody was reading, and a mutation that returned 200. Downstream, containment
// reported a counted eval error for that tenant on every location event, indefinitely.
// That is not a wire hiccup; it is a documented configuration failing by construction.
//
// And it is not a corner of that configuration either. The set is over the ceiling at
// every coordinate precision anyone would use — ~980 KB of stored GeoJSON at five decimal
// places, which is already about a metre — so "at the ceiling" here means the ordinary
// case for a tenant who filled the limits, not an abusive one. See
// model.MaxGeoFenceGeometryBytes for the measurements and for why no per-fence bound can
// make the aggregate fit while coordinates are text.
//
// maxPayload is what closes it. Over the ceiling the writer publishes the POINTER form of
// the fact (model.GeoFenceSetMintedEvent.FencesOmitted) — version and mintedAt, no fences —
// which the consumer resolves through the frozen archive the same way a replay of last
// week's events already does.
//
// Be precise about what that makes total, because it is not the publish path. A publish can
// still fail — WriteMessages returning an error is still logged and swallowed here, and
// emitMintedGeoFenceSet still returns silently when a just-minted snapshot will not decode —
// and those remain best-effort by design, because the authoring action has already committed
// and must not be rolled back by a wire problem. What is now total is COVERAGE OF THE SIZE
// CEILING: there is no longer a fence set whose size alone means no fact can be sent. The
// remaining holes are transport faults, which are transient and which the reconcile sweep
// repairs; the size hole was permanent and the sweep could not repair it.
type GeoFenceSetWriter struct {
	writer messaging.MessageWriter
	// maxPayload is the broker's per-message ceiling in bytes, minus factHeadroom, floored
	// at 1. Zero means the caller did not state a ceiling and the check is off, which is the
	// right default for a caller that genuinely does not know it (tests).
	//
	// 🔴 THE FLOOR IS WHY THIS IS NOT AN int32 CEILING HELD DIRECTLY. A configured ceiling
	// at or below factHeadroom subtracts to zero or less, and zero is the "no ceiling stated"
	// value — so an operator setting streamMaxMsgSize to 2048 would have turned the whole
	// protection off and restored the pre-fix defect silently. "Nothing was stated" and "what
	// was stated is tiny" must not share an encoding: the first means do not check, the
	// second means nothing fits.
	maxPayload int
	// omitted counts fence-set facts published in the pointer form. Nil-safe.
	omitted prometheus.Counter
}

// NewGeoFenceSetWriter builds a fence-set publisher over the given writer.
//
// maxMsgSize is the broker's configured per-message ceiling
// (infrastructure.nats.streamMaxMsgSize); a non-positive value means no ceiling was stated
// and disables the size check. omitted counts pointer facts and may be nil.
//
// 🔴 A STATED CEILING SMALLER THAN factHeadroom FAILS CLOSED, NOT OPEN. ApplyDefaults coerces
// only a non-positive streamMaxMsgSize, and values.schema.json sets no minimum, so a chart
// override of 2048 is an accepted configuration. Subtracting the headroom from it and then
// treating the result as "no ceiling" would publish every oversized fact straight at a broker
// that refuses it — the exact defect this writer exists to remove, reachable by a value
// nobody would think of as dangerous. The floor of 1 makes that configuration send POINTERS
// instead, which is the correct reading of it: under a ceiling that small, nothing fits.
func NewGeoFenceSetWriter(writer messaging.MessageWriter, maxMsgSize int32, omitted prometheus.Counter) *GeoFenceSetWriter {
	limit := 0
	if maxMsgSize > 0 {
		limit = int(maxMsgSize) - factHeadroom
		if limit < 1 {
			limit = 1
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
	pointerFact := false
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
		pointerFact = true
		log.Warn().Int32("version", event.Version).Int("fences", len(event.Fences)).
			Int("maxPayloadBytes", w.maxPayload).
			Msg("Geofence set is too large for one broker message; publishing a pointer fact instead. Consumers resolve its fences from the frozen archive, which costs one cross-service read per fence edit for this tenant.")
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Int32("version", event.Version).
			Msg("Unable to publish geofence set event")
		return
	}
	// Counted AFTER the write, not before it. The metric's help string says these facts were
	// PUBLISHED, and an increment taken ahead of a failing write would make that sentence
	// false in exactly the situation an operator is reading the number to understand.
	if pointerFact && w.omitted != nil {
		w.omitted.Inc()
	}
}
