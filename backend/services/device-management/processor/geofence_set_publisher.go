// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

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
type GeoFenceSetWriter struct {
	writer messaging.MessageWriter
}

// NewGeoFenceSetWriter builds a fence-set publisher over the given writer.
func NewGeoFenceSetWriter(writer messaging.MessageWriter) *GeoFenceSetWriter {
	return &GeoFenceSetWriter{writer: writer}
}

// PublishGeoFenceSet marshals and publishes one fence-set fact. It never returns an error
// (the interface is fire-and-forget); failures are logged.
func (w *GeoFenceSetWriter) PublishGeoFenceSet(ctx context.Context, event *model.GeoFenceSetMintedEvent) {
	bytes, err := model.MarshalGeoFenceSetMintedEvent(event)
	if err != nil {
		log.Error().Err(err).Int32("version", event.Version).
			Msg("Unable to marshal geofence set event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Int32("version", event.Version).
			Msg("Unable to publish geofence set event")
	}
}
