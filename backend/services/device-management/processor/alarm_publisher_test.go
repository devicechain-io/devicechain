// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type alarmEventRecorder struct {
	messages []messaging.Message
	err      error
}

func (w *alarmEventRecorder) WriteMessages(_ context.Context, messages ...messaging.Message) error {
	w.messages = append(w.messages, messages...)
	return w.err
}
func (w *alarmEventRecorder) WriteToDevice(ctx context.Context, _ string, messages ...messaging.Message) error {
	return w.WriteMessages(ctx, messages...)
}
func (w *alarmEventRecorder) HandleResponse(error) {}

func TestAlarmEventDedupEncodingAndBrokerFailures(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	recorder := &alarmEventRecorder{}
	writer := NewAlarmEventWriter(recorder, nil, NewAlarmEventMetrics(ms))
	ev := &model.AlarmStateChangeEvent{AlarmToken: "alarm", EventType: model.AlarmEventRaised}
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, writer.PublishAlarmEventDedup(ctx, ev, "alarm-event-acme-7"))
	require.Equal(t, "alarm-event-acme-7", recorder.messages[0].DedupID)
	recorder.err = errors.New("broker down")
	// A broker refusal must stay retryable: the relay quarantines anything marked unpublishable.
	refused := writer.PublishAlarmEventDedup(ctx, ev, "id")
	require.ErrorIs(t, refused, recorder.err)
	require.NotErrorIs(t, refused, model.ErrAlarmEventUnpublishable)
	cause := errors.New("marshal failed")
	writer.marshal = func(*model.AlarmStateChangeEvent) ([]byte, error) { return nil, cause }
	err := writer.PublishAlarmEventDedup(ctx, ev, "id")
	require.ErrorIs(t, err, model.ErrAlarmEventUnpublishable)
	require.ErrorIs(t, err, cause)
	require.Len(t, recorder.messages, 2)
	families, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if family.GetName() == "devicechain_devicemanagement_alarm_event_dead_lettered_total" {
			found = true
			require.Zero(t, family.Metric[0].Counter.GetValue())
		}
	}
	require.True(t, found, "documented dead letter metric must remain registered")
}

func gatheredCounter(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("the registry exports no %s", name)
	return 0
}

const deviceManagementLost = "devicechain_devicemanagement_dead_letter_lost_total"

func TestAlarmEventWriterClassifiesUnavailable(t *testing.T) {
	ev := &model.AlarmStateChangeEvent{AlarmToken: "alarm", EventType: model.AlarmEventRaised}
	refusal := errors.New("tenant permission denied")
	for _, test := range []struct {
		name                   string
		cause                  error
		connected, unavailable bool
	}{
		{"refusal", refusal, true, false},
		{"connection down", refusal, false, true},
		{"deadline", context.DeadlineExceeded, true, true},
		{"nats timeout", nats.ErrTimeout, true, true},
		{"no responders", nats.ErrNoResponders, true, true},
		{"closed", nats.ErrConnectionClosed, true, true},
		{"disconnected", nats.ErrDisconnected, true, true},
		{"reconnecting", nats.ErrConnectionReconnecting, true, true},
		{"legacy stream timeout", nats.ErrNoStreamResponse, true, true},
		{"stream timeout", jetstream.ErrNoStreamResponse, true, true},
		{"network timeout", os.ErrDeadlineExceeded, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &alarmEventRecorder{err: fmt.Errorf("publish: %w", test.cause)}
			writer := NewAlarmEventWriter(recorder, nil, AlarmEventMetrics{}).WithConnected(func() bool { return test.connected })
			err := writer.PublishAlarmEventDedup(context.Background(), ev, "id")
			require.ErrorIs(t, err, test.cause)
			require.Equal(t, test.unavailable, errors.Is(err, model.ErrAlarmEventBrokerUnavailable))
			require.NotErrorIs(t, err, model.ErrAlarmEventUnpublishable)
			writer.marshal = func(*model.AlarmStateChangeEvent) ([]byte, error) { return nil, refusal }
			err = writer.PublishAlarmEventDedup(context.Background(), ev, "id")
			require.ErrorIs(t, err, model.ErrAlarmEventUnpublishable)
			require.NotErrorIs(t, err, model.ErrAlarmEventBrokerUnavailable)
			require.Len(t, recorder.messages, 1, "encoding failure must not publish")
		})
	}
}
