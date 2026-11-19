// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
)

// DeviceRosterPublisher publishes device-roster events (ADR-051 slice 4c-2). Like the
// detection-rules publisher it is best-effort and side-band to the
// device write: a marshal/publish failure is logged by the implementation, never
// surfaced to the caller — a NATS hiccup must not fail or retry the device create/update.
// Emission is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is durably
// persisted by event-processing's consumer (persist-before-ack) and so survives a
// restart, but a fact that never reaches the stream is NOT recovered by replay — it is
// repaired by event-processing's reconcile against this service (DeviceRosterPage, at the
// start of its DETECT term and every five minutes). Implementations must be safe for
// concurrent use.
//
// The error is that of a publish the broker did not accept (nil for a delivered fact, and for
// a fact the implementation dropped for a reason particular to it, such as a marshal failure).
// It is never surfaced to the caller of the device write. When it wraps
// ErrRosterBrokerUnreachable, a fan-out that sends one fact per device (a type re-point, a bulk
// create, an import) stops there instead of waiting out one publish ceiling per device; any
// other refusal may be transient, and the fan-out carries on as it always has.
// ErrRosterBrokerUnreachable marks a roster publish refused while the publisher's broker
// connection was down: the facts after it in the same fan-out would be refused too.
var ErrRosterBrokerUnreachable = errors.New("the broker connection is down")

type DeviceRosterPublisher interface {
	PublishDeviceRoster(ctx context.Context, event *DeviceRosterEvent) error
}
