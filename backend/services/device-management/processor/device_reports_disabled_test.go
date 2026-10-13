// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// disabledReportsApi answers every call the way the model answers a device an operator has
// disabled: a refusal, identical on every delivery.
type disabledReportsApi struct{ calls int }

func (a *disabledReportsApi) SyncDeviceConfiguration(context.Context, string, int64) (*model.DeviceConfigurationRevision, error) {
	a.calls++
	return nil, &model.ConfigurationReportRefusal{Reason: model.ReportRejectedDeviceDisabled}
}

func (a *disabledReportsApi) ApplyDeviceConfigurationReport(context.Context, string, int64, string,
	model.ConfigurationStatus, []string) error {
	a.calls++
	return &model.ConfigurationReportRefusal{Reason: model.ReportRejectedDeviceDisabled}
}

// 🔴 A DISABLED DEVICE'S CONFIGURATION TRAFFIC IS SETTLED ON ITS FIRST DELIVERY, COUNTED, AND NOT
// LETTERED. Retrying cannot answer differently while the device stays disabled, and a device is
// disabled precisely when its traffic is unwanted — a dead letter per message would move the flood
// into the dead-letter store. It is counted in config_reports_rejected_total{reason="device_disabled"}.
func TestADisabledDevicesConfigurationReportsAreCountedAndSettled(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	dead := &deadRecorder{}
	api := &disabledReportsApi{}
	rc := NewDeviceReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api,
		deadletter.NewProducer(ms).NewSink(dead), NewDeviceReportsMetrics(ms))
	if err := rc.Initialize(context.Background()); err != nil {
		t.Fatalf("initializing the consumer: %v", err)
	}

	bodies := map[string]string{
		"sync": `{"v":1,"kind":"sync","bootId":"b","config":{"haveRevision":0}}`,
		"config.status": `{"v":1,"kind":"config.status","revision":1,"digest":"sha256:` +
			strings.Repeat("a", 64) + `","status":"APPLIED"}`,
	}
	for kind, body := range bodies {
		ack := &fakeAck{}
		msg := messaging.NewConsumedMessage("dc.acme.device-reports.dev-1", []byte(body), 1, nil, ack).
			WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "dc_device-reports",
				Consumer: "dc_device-management_device-reports", Seq: 7})
		rc.handle(context.Background(), msg)
		if ack.acks != 1 {
			t.Fatalf("%s from a disabled device: acks = %d, want 1 (settled on the first delivery)", kind, ack.acks)
		}
	}
	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters for a disabled device, want 0", len(dead.msgs))
	}
	if got := loopResults(t, reg, "device_reports", core.ResultDropped); got != 2 {
		t.Fatalf("device_reports results{dropped} = %v, want 2 (the label update-management uses for the same refusal)", got)
	}
	if got := testutil.ToFloat64(rc.rejected.WithLabelValues(model.ReportRejectedDeviceDisabled)); got != 2 {
		t.Fatalf("config_reports_rejected_total{reason=device_disabled} = %v, want 2", got)
	}
}
