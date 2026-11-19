// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
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
