// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// Notifier is the dispatch seam between the durable alarm-events consumer and the
// channel delivery it drives (ADR-017). The processor decodes each alarm transition
// and hands it to a Notifier under the message's tenant-scoped context; a Notifier
// decides — per the tenant's notification policy — which channels (SMTP, webhook)
// to deliver it through. Keeping this an interface lets later slices drop in the
// policy-driven multi-channel dispatcher without touching the consumer plumbing.
//
// The delivery contract an implementation MUST honor — the consumer is a durable,
// worker-pooled JetStream reader, so:
//
//   - At-least-once, not exactly-once. Notify may be re-invoked with the same event
//     after a redelivery (a returned error left the message unacked) or a crash before ack. A multi-channel Notifier
//     that returns an error after PARTIAL success (email sent, Slack timed out) gets
//     the WHOLE event redelivered and will double-send the channel that succeeded —
//     so per-channel delivery must be idempotent, or per-channel completion tracked,
//     before returning an error. Prefer per-channel retry inside Notify over leaning
//     on redelivery (see the error contract below).
//   - Unordered. A pool of workers dispatches concurrently with no per-alarm
//     partitioning, so transitions for the same alarm (RAISED then auto-CLEARED) can
//     arrive out of order, and a redelivered RAISED can land after a later CLEARED.
//     An implementation that must not e.g. mail "cleared" before "raised" must order
//     on the event's OccurredTime/RaisedTime itself.
//   - Bounded execution. Notify runs on a background context (drain-on-shutdown), so
//     it must bound its own duration; a hung channel call stalls graceful shutdown.
//
// Error contract: a returned error is treated as TRANSIENT by the processor and the
// message is left unacked for AckWait-paced redelivery — but redelivery today has a
// finite MaxDeliver cap, after which the notification is DROPPED (there is no dead-letter
// path yet). Return an error only for a genuinely retryable failure; log-and-swallow
// a permanent one (a malformed policy) so a poison notification does not loop. A real
// channel adapter should own its own retry/backoff/durability rather than rely on
// processor redelivery for reliability.
type Notifier interface {
	Notify(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error
}

// LogNotifier is the first-slice Notifier: it records the notification the service
// would deliver, without any channel yet. It makes the consumer end-to-end
// observable (an alarm transition produces a log line in this service) and is the
// baseline the real channel dispatcher replaces. It never fails, so it never
// triggers redelivery.
type LogNotifier struct{}

// NewLogNotifier creates a LogNotifier.
func NewLogNotifier() *LogNotifier { return &LogNotifier{} }

// Notify logs the alarm transition that would be delivered. The tenant is read from
// the scoped context the processor built from the message subject.
func (n *LogNotifier) Notify(ctx context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	tenant, _ := core.TenantFromContext(ctx)
	ev := log.Info().
		Str("tenant", tenant).
		Str("event", event.EventType.String()).
		Str("alarm", event.AlarmToken).
		Str("alarmKey", event.AlarmKey).
		Str("originatorType", event.OriginatorType).
		Uint("originatorId", event.OriginatorId).
		Str("severity", event.Severity).
		Str("state", event.State).
		Bool("acknowledged", event.Acknowledged)
	if event.PreviousSeverity != "" {
		ev = ev.Str("previousSeverity", event.PreviousSeverity)
	}
	if event.LastValue != nil {
		ev = ev.Float64("lastValue", *event.LastValue)
	}
	if event.Message != nil {
		ev = ev.Str("message", *event.Message)
	}
	ev.Msg("Alarm notification (no channels configured yet — ADR-017 first slice logs delivery intent)")
	return nil
}
