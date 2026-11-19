// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
)

// DeviceAttributePublisher publishes device-attribute events (ADR-051 slice 4c-3). Like
// the roster and detection-rules publishers it is best-effort and side-band
// to the attribute write: a marshal/publish failure is logged by the implementation,
// never surfaced to the caller — a NATS hiccup must not fail or retry the attribute
// set/delete. Emission is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is
// durably persisted by event-processing's consumer (persist-before-ack) and survives a
// restart, but a fact that never reaches the stream is NOT recovered by replay — it is
// repaired by event-processing's reconcile against this service
// (DeviceThresholdAttributePage, at the start of its DETECT term and every five minutes).
// Implementations must be safe for concurrent use.
type DeviceAttributePublisher interface {
	PublishDeviceAttribute(ctx context.Context, event *DeviceAttributeEvent)
}
