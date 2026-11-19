// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
)

// GeoFenceSetPublisher publishes fence-set manifests (ADR-078). Like the detection-rules,
// roster and device-attribute publishers it is best-effort and side-band to the fence write:
// a marshal/publish failure is logged by the implementation, never surfaced to the caller — a
// NATS hiccup must not fail or roll back the authoring action, which is the source of truth.
//
// Emission is at-most-once, and the recovery story differs from the other fact publishers in
// a way worth stating: the consumer keeps no durable projection of its own, because this
// service already IS the durable archive of every version's snapshot and of every geometry
// document any version names. A fact that never reaches the stream is recovered by the
// consumer's startup reconcile reading those back, not by a replay of the stream.
// Implementations must be safe for concurrent use.
//
// 🔴 AN IMPLEMENTATION MUST NEVER TRUNCATE A MANIFEST, and unlike its predecessor it has no
// legitimate reason to want to: the fact carries no geometry, so no fence set can make one
// too large for the transport. If a publish is refused anyway — an operator can configure a
// per-message ceiling below any useful value, and values.schema.json states no minimum — the
// implementation counts and logs the refusal and sends nothing. A short fence list is
// indistinguishable downstream from a tenant who really has that many fences.
type GeoFenceSetPublisher interface {
	PublishGeoFenceSetManifest(ctx context.Context, manifest *GeoFenceSetManifest)
}
