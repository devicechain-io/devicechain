// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"time"
)

// GeoFenceSetMintedEvent is the fact emitted post-commit whenever a geofence change mints
// a new fence-set version (ADR-078), telling event-processing what the fences ARE at that
// version so its live containment projection can answer without ever reading back into
// this service on the hot path.
//
// 🔴 IT CARRIES THE WHOLE FROZEN SET, NOT A "GO AND RE-READ IT" SIGNAL, and that is the
// one place this fact departs from the roster and device-attribute facts (whose consumers
// receive only an identity and re-read the authoritative projection on their loop). The
// reason those facts must be re-read is ORDERING: an attribute's current value is mutable,
// so two consumers racing over reorderable streams could install a stale observation. A
// fence-set VERSION is immutable by construction — version 7's snapshot is frozen at mint
// and nothing ever rewrites it — so a fact carrying the value cannot be stale relative to
// anything. There is no ordering hazard to defend against, and carrying the value is what
// keeps the consumer's hot path free of a synchronous round trip.
//
// The tenant is not a field: it travels on the per-tenant NATS subject, exactly like the
// device-roster, device-attribute and detection-rules-published facts.
type GeoFenceSetMintedEvent struct {
	// Version is the fence-set version just minted. It is what a resolved location event
	// is stamped with, and it is the key the consumer files this set under.
	Version int32 `json:"version"`
	// Fences is the frozen fence set AS OF that version — the same GeoFenceSnapshotRef
	// list stored in GeoFenceSetVersion.Snapshot, so the wire and the archive cannot
	// describe the fences differently.
	Fences []GeoFenceSnapshotRef `json:"fences"`
	// MintedAt is when the change that minted this version committed. It is carried for
	// operator diagnosis (how old is the set this engine is holding?) and is not read by
	// containment.
	MintedAt time.Time `json:"mintedAt"`
}

// MarshalGeoFenceSetMintedEvent encodes a fence-set fact for the wire (the producer side).
//
// JSON, not protobuf, and deliberately so: the payload's substantive content is the stored
// snapshot document, which is ALREADY JSON (GeoFenceSnapshotRef.Geometry is a
// json.RawMessage held verbatim from what the author wrote). Re-encoding an opaque
// geometry document into a proto bytes field would buy nothing and would add a second
// encoding of the same bytes to keep honest. The raise-alarm fact takes the same shape for
// the same reason.
func MarshalGeoFenceSetMintedEvent(event *GeoFenceSetMintedEvent) ([]byte, error) {
	return json.Marshal(event)
}

// UnmarshalGeoFenceSetMintedEvent decodes a fence-set fact (the consumer side,
// event-processing). A missing fence list normalizes to a non-nil empty slice, so the
// version minted by deleting a tenant's LAST fence decodes to a known-EMPTY set rather
// than a nil one — the distinction the whole projection turns on.
func UnmarshalGeoFenceSetMintedEvent(encoded []byte) (*GeoFenceSetMintedEvent, error) {
	event := &GeoFenceSetMintedEvent{}
	if err := json.Unmarshal(encoded, event); err != nil {
		return nil, err
	}
	if event.Fences == nil {
		event.Fences = []GeoFenceSnapshotRef{}
	}
	return event, nil
}

// GeoFenceSetPublisher publishes fence-set facts (ADR-078). Like the detection-rules,
// roster and device-attribute publishers it is best-effort and side-band to the fence
// write: a marshal/publish failure is logged by the implementation, never surfaced to the
// caller — a NATS hiccup must not fail or roll back the authoring action, which is the
// source of truth.
//
// Emission is at-most-once, and the recovery story differs from the other fact publishers
// in a way worth stating: the consumer keeps no durable projection of its own, because
// this service already IS the durable archive of every version's snapshot. A fact that
// never reaches the stream is recovered by the consumer's startup reconcile reading those
// snapshots back, not by a replay of the stream. Implementations must be safe for
// concurrent use.
type GeoFenceSetPublisher interface {
	PublishGeoFenceSet(ctx context.Context, event *GeoFenceSetMintedEvent)
}
