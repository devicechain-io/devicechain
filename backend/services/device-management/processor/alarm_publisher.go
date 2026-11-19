// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
)

// AlarmEventWriter publishes once per relay attempt. Durable outboxes own retries.
type AlarmEventWriter struct {
	writer    messaging.MessageWriter
	connected func() bool
	marshal   func(*model.AlarmStateChangeEvent) ([]byte, error)
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
	err = w.writer.WriteMessages(ctx, messaging.Message{Value: payload, DedupID: dedupID})
	if err != nil {
		var timeout net.Error
		if (w.connected != nil && !w.connected()) ||
			errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) ||
			errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrConnectionClosed) ||
			errors.Is(err, nats.ErrDisconnected) || errors.Is(err, nats.ErrConnectionReconnecting) ||
			errors.Is(err, nats.ErrNoStreamResponse) || errors.Is(err, jetstream.ErrNoStreamResponse) ||
			(errors.As(err, &timeout) && timeout.Timeout()) {
			return fmt.Errorf("%w: %w", model.ErrAlarmEventBrokerUnavailable, err)
		}
	}
	return err
}

// WithConnected supplies the live connection state for classifying publication failures.
func (w *AlarmEventWriter) WithConnected(connected func() bool) *AlarmEventWriter {
	w.connected = connected
	return w
}
