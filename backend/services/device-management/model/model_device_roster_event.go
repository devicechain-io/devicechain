// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// DeviceRosterEvent is the envelope emitted post-commit when a device is created or
// re-typed (ADR-051 slice 4c-2), telling event-processing that a device is EXPECTED
// to report so its DETECT engine can arm absence for a device that has NEVER reported
// (the reported-once-then-silent case is already covered by the heartbeat-armed timer).
//
// ProfileToken is the STABLE profile identity the device's type adopts (ADR-045) — not
// the "{profileToken}@{version}" token — so a re-publish that mints a new version never
// staleness-orphans the roster entry; event-processing cross-references the latest
// published version at arming time. It is empty when the type has no profile (a roster
// entry with no resolvable rules, retained so a later re-type re-homes it).
//
// ExpectedSince is when the device's membership of that profile began — its creation, or the
// stored instant of its last re-type or its type's last re-point (devices.expected_since) — the
// base of the dead-man clock for a never-reported device. The tenant is not a field — it travels on the per-tenant NATS
// subject, exactly like the detection-rules-published and entity-deleted facts.
type DeviceRosterEvent struct {
	DeviceToken   string
	ProfileToken  string
	ExpectedSince time.Time
}

// DeviceRosterPublisher publishes device-roster events (ADR-051 slice 4c-2). Like the
// detection-rules and entity-deleted publishers it is best-effort and side-band to the
// device write: a marshal/publish failure is logged by the implementation, never
// surfaced to the caller — a NATS hiccup must not fail or retry the device create/update.
// Emission is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is durably
// persisted by event-processing's consumer (persist-before-ack) and so survives a
// restart, but a fact that never reaches the stream is NOT recovered by replay — it is
// repaired by event-processing's reconcile against this service (DeviceRosterPage, at the
// start of its DETECT term and every five minutes). Implementations must be safe for
// concurrent use.
type DeviceRosterPublisher interface {
	PublishDeviceRoster(ctx context.Context, event *DeviceRosterEvent)
}
