// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// alarmClient is the REACT dispatcher's raise-alarm sink (ADR-051 slice 5c): it publishes a
// raise-alarm request onto device-management's dedicated raise-alarm subject, which a thin
// device-management consumer applies through the existing alarm engine (slice 5c-1). It is
// dependency-inverted behind react.AlarmSink so the dispatcher never depends on the transport.
//
// It carries no idempotency token: raise-alarm is an upsert keyed on (device, alarmKey) downstream,
// so an at-least-once redelivery is safe without one. It is wired ONLY when raise-alarm dispatch is
// enabled (a default-off flag until slice 6 retires the measurement-driven evaluator), so a nil sink
// leaves raiseAlarm actions inert.
type alarmClient struct {
	writer messaging.MessageWriter
}

// NewAlarmClient builds a raise-alarm sink over a tenant-scoped writer on the raise-alarm subject.
func NewAlarmClient(writer messaging.MessageWriter) react.AlarmSink {
	return &alarmClient{writer: writer}
}

// Raise publishes one raise-alarm request on its tenant's subject. The writer derives the subject
// from the tenant in context (fail-closed on none), so the request lands on exactly
// "{instance}.{tenant}.raise-alarm". A marshal or write failure is returned so the dispatcher
// retries (the event redelivers; the downstream upsert makes the re-raise idempotent). Value is
// omitted (0) until the derived event carries the triggering value — a later slice; per slice 5c-1,
// raise-alarm must not be enabled before then.
func (c *alarmClient) Raise(ctx context.Context, req react.AlarmRequest) error {
	payload, err := dmmodel.MarshalRaiseAlarmRequest(&dmmodel.RaiseAlarmRequest{
		DeviceToken:  req.DeviceToken,
		AlarmKey:     req.AlarmKey,
		MetricKey:    req.MetricKey,
		Severity:     req.Severity,
		OccurredTime: req.OccurredTime,
	})
	if err != nil {
		return fmt.Errorf("react: marshal raise-alarm for device %q: %w", req.DeviceToken, err)
	}
	tctx := dccore.WithTenant(ctx, req.Tenant)
	if err := c.writer.WriteMessages(tctx, messaging.Message{Value: payload}); err != nil {
		return fmt.Errorf("react: publish raise-alarm for device %q: %w", req.DeviceToken, err)
	}
	return nil
}
