// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// AlarmEventWriter publishes once per relay attempt. Durable outboxes own retries.
type AlarmEventWriter struct {
	writer  messaging.MessageWriter
	marshal func(*model.AlarmStateChangeEvent) ([]byte, error)
	AlarmEventMetrics
}

// AlarmEventMetrics remains registered once, in the initialize phase.
type AlarmEventMetrics struct{ deadLettered prometheus.Counter }

func NewAlarmEventMetrics(ms *core.Microservice) AlarmEventMetrics {
	return AlarmEventMetrics{deadLettered: ms.NewCounter("alarm_event_dead_lettered_total",
		"retained; stays at zero since alarm events are relayed from the outbox; see alarm_events_quarantined")}
}

// NewAlarmEventWriter retains the sink parameter for existing wiring; poison is now
// quarantined durably in the outbox without requiring an available broker.
func NewAlarmEventWriter(writer messaging.MessageWriter, _ *deadletter.Sink, metrics AlarmEventMetrics) *AlarmEventWriter {
	return &AlarmEventWriter{writer: writer, marshal: proto.MarshalAlarmStateChangeEvent, AlarmEventMetrics: metrics}
}

func (w *AlarmEventWriter) PublishAlarmEventDedup(ctx context.Context, event *model.AlarmStateChangeEvent, dedupID string) error {
	payload, err := w.marshal(event)
	if err != nil {
		return fmt.Errorf("%w: %w", model.ErrAlarmEventUnpublishable, err)
	}
	return w.writer.WriteMessages(ctx, messaging.Message{Value: payload, DedupID: dedupID})
}
