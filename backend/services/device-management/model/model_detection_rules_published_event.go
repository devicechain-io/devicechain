// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
)

// DetectionRulesPublishedPublisher publishes detection-rules-published events
// (ADR-051 slice 4b-3). Like the alarm publisher it is best-effort
// and side-band to the publish: a marshal/publish failure is logged by the implementation,
// never surfaced to the caller — a NATS hiccup must not fail or retry the profile publish.
// The emit is at-most-once (ADR-044 async-fact posture): a DELIVERED fact is durably
// persisted by event-processing's consumer (persist-before-ack) and so survives a restart,
// but a fact that never reaches the stream is NOT recovered by replay — it is repaired by
// event-processing's reconcile against this service (ActiveProfileRules, at the start of its
// DETECT term and every five minutes). Implementations must be safe for concurrent use.
type DetectionRulesPublishedPublisher interface {
	PublishDetectionRulesPublished(ctx context.Context, event *DetectionRulesPublishedEvent)
}
