// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
)

// purgedReportsApi answers every call the way a deleted tenant's writes are answered: the
// erasure fence refuses them, identically on every delivery.
type purgedReportsApi struct{ calls int }

func (a *purgedReportsApi) SyncDeviceConfiguration(context.Context, string, int64) (*model.DeviceConfigurationRevision, error) {
	a.calls++
	return nil, fmt.Errorf("minting a revision: %w", rdb.ErrTenantPurged)
}

func (a *purgedReportsApi) ApplyDeviceConfigurationReport(context.Context, string, int64, string,
	model.ConfigurationStatus, []string) error {
	a.calls++
	return fmt.Errorf("recording the report: %w", rdb.ErrTenantPurged)
}

// 🔴 A DELETED TENANT'S REPORT IS DROPPED ON ITS FIRST DELIVERY. Treated as transient it would
// spend the whole redelivery budget and then be lettered at the cap — a new record about a
// tenant whose data is being erased. Both kinds this consumer applies reach a fenced write.
func TestADeviceReportForADeletedTenantIsDroppedNotLettered(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	dead := &deadRecorder{}
	api := &purgedReportsApi{}
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
		for _, delivery := range []int{1, messaging.MaxDeliver} {
			ack := &fakeAck{}
			msg := messaging.NewConsumedMessage("dc.acme.device-reports.dev-1", []byte(body), delivery, nil, ack).
				WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "dc_device-reports",
					Consumer: "dc_device-management_device-reports", Seq: 7})
			rc.handle(context.Background(), msg)
			if ack.acks != 1 {
				t.Fatalf("%s on delivery %d: a report the fence refuses must be acked, acks=%d", kind, delivery, ack.acks)
			}
		}
	}
	if api.calls != 4 {
		t.Fatalf("the fake was reached %d times, want 4: the reports did not reach the fenced write", api.calls)
	}
	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters for a deleted tenant, want 0", len(dead.msgs))
	}
}
