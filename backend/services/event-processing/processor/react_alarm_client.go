// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// wireAlarmEdge maps the dispatcher's internal edge token (runtime vocabulary) onto the SHARED wire
// vocabulary (dmmodel), so the device-management wire binds only to dmmodel's constants on BOTH ends —
// the producer here and the consumer's `req.Edge == model.AlarmEdgeResolved`. Making the translation
// explicit (rather than passing the string through) means a change to either module's edge spelling is
// a compile-visible mapping edit, not a silent fail-open drift into the raise path.
func wireAlarmEdge(edge string) string {
	if edge == runtime.EdgeResolved {
		return dmmodel.AlarmEdgeResolved
	}
	return dmmodel.AlarmEdgeRaised
}

// alarmClient is the REACT dispatcher's raise-alarm sink (ADR-051 slice 5c): it publishes a
// raise-alarm request onto device-management's dedicated raise-alarm subject, which a thin
// device-management consumer folds into the alarm's contributor set (slice 5c-1 / ADR-057). It is
// dependency-inverted behind react.AlarmSink so the dispatcher never depends on the transport.
//
// It carries no idempotency token: the downstream contributor-set fold is keyed on (device, alarmKey)
// with a per-contributor monotonic decision-ts, so an at-least-once redelivery is safe without one.
// Since the 6d cutover it is always wired (the sole alarm path).
type alarmClient struct {
	writer messaging.MessageWriter
}

// NewAlarmClient builds a raise-alarm sink over a tenant-scoped writer on the raise-alarm subject.
func NewAlarmClient(writer messaging.MessageWriter) react.AlarmSink {
	return &alarmClient{writer: writer}
}

// Dispatch publishes one alarm request (raise or clear, per req.Edge — ADR-057) on its tenant's
// subject. The writer derives the subject from the tenant in context (fail-closed on none), so the
// request lands on exactly "{instance}.{tenant}.raise-alarm". A marshal or write failure is returned
// so the dispatcher retries (the event redelivers; the downstream contributor upsert makes the re-run
// idempotent). The triggering value is carried through as a nullable pointer (slice 6a): a
// value-bearing raised detection (threshold/repeating crossing sample, deltaRate/aggregate computed
// scalar) stamps its real value, while a silence-driven fire and every resolved edge carry nil —
// device-management then leaves the alarm's last value NULL rather than writing a fabricated 0.
func (c *alarmClient) Dispatch(ctx context.Context, req react.AlarmRequest) error {
	payload, err := dmmodel.MarshalRaiseAlarmRequest(&dmmodel.RaiseAlarmRequest{
		DeviceToken:  req.DeviceToken,
		AlarmKey:     req.AlarmKey,
		RuleID:       req.RuleID,
		Edge:         wireAlarmEdge(req.Edge),
		MetricKey:    req.MetricKey,
		Severity:     req.Severity,
		Value:        req.Value,
		OccurredTime: req.OccurredTime,
	})
	if err != nil {
		return fmt.Errorf("react: marshal alarm request for device %q: %w", req.DeviceToken, err)
	}
	tctx := dccore.WithTenant(ctx, req.Tenant)
	if err := c.writer.WriteMessages(tctx, messaging.Message{Value: payload}); err != nil {
		return fmt.Errorf("react: publish alarm request for device %q: %w", req.DeviceToken, err)
	}
	return nil
}
