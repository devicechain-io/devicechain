// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// DeviceAttributeEvent is the envelope emitted post-commit when a numeric, platform-set
// attribute of a device is upserted or deleted (ADR-051 slice 4c-3), telling
// event-processing the current numeric value of a device attribute so a detection rule
// can resolve a DYNAMIC threshold from it — "m[metric] <op> attr[key]" reads the
// device's own attribute instead of a compile-time literal, so one published rule
// enforces a per-device limit.
//
// Only numeric device attributes (ADR-012 value type DOUBLE or LONG) in the two
// platform-set scopes (SHARED or SERVER) are emitted: CLIENT (device-reported) is
// excluded so a device cannot set the limit it is checked against. Removed is true when
// the attribute was deleted, or when a previously-numeric attribute is overwritten with
// a non-numeric value or type — so event-processing drops the key rather than keep a
// stale number. Value is the parsed number (ignored when Removed).
//
// Scope is part of the identity. An attribute's natural key is (entity, scope, key), so a
// device may hold the same key in BOTH eligible scopes at once; the fact carries scope so
// event-processing keys its projection on (device, scope, key) and a write/delete in one
// scope never annihilates the other's value. The rule language stays scope-less
// (attr["key"]) — event-processing flattens the two scopes at eval time with a fixed
// precedence, SERVER over SHARED (a platform-only override beats the device-readable one).
//
// A device DELETE emits NO attribute fact: the delete cascade hard-deletes the device's
// attributes in one transaction (bypassing the per-attribute delete path), and
// event-processing drops the device's whole attribute projection off the entity-deleted
// fact instead — exactly as the device roster is torn down. The consumer must therefore
// GC this projection on entity-deleted and order it against the fact's UpdatedAt so a
// reused token cannot inherit a dead device's thresholds (the DeviceRoster tombstone
// precedent, ADR-051 slice 4c-2b-1).
//
// UpdatedAt is the write time: the monotonic clock event-processing orders an
// upsert-vs-delete on (real-time monotonic, exactly like the roster/profile-active
// projections). A concurrent set+delete of the same key in one process can still stamp
// out of commit order (the accepted monotonic-clock posture — it self-heals on the next
// write to the key), so this is not a linearization guarantee. The tenant is not a field
// — it travels on the per-tenant NATS subject, like the roster, entity-deleted, and
// detection-rules-published facts.
type DeviceAttributeEvent struct {
	DeviceToken string
	AttrKey     string
	Scope       string
	Value       float64
	Removed     bool
	UpdatedAt   time.Time
}

// DeviceAttributePublisher publishes device-attribute events (ADR-051 slice 4c-3). Like
// the roster/detection-rules/entity-deleted publishers it is best-effort and side-band
// to the attribute write: a marshal/publish failure is logged by the implementation,
// never surfaced to the caller — a NATS hiccup must not fail or retry the attribute
// set/delete. Emission is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is
// durably persisted by event-processing's consumer (persist-before-ack) and survives a
// restart, but a fact that never reaches the stream is NOT recovered by replay — it
// relies on a subsequent write to the same attribute or the planned reconciliation
// sweep. Implementations must be safe for concurrent use.
type DeviceAttributePublisher interface {
	PublishDeviceAttribute(ctx context.Context, event *DeviceAttributeEvent)
}
