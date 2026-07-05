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
// A returned error is treated as transient by the processor (the message is naked
// for redelivery up to the finite MaxDeliver cap), so an implementation should
// return an error only for a retryable failure (a channel timeout), not for a
// permanent one (a malformed policy) — the latter should be logged and swallowed so
// a poison notification does not loop.
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
