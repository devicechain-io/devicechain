// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
)

func testHold(store *holdtest.Store) *storehold.Holder {
	return &storehold.Holder{Name: "test", Probe: store.Probe, Tune: holdtest.FastTuning()}
}

func holdTestMicroservice() *core.Microservice {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return ms
}

func finishesWithin(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// failingEdgeApi refuses ApplyAlarmContributorEdge with err for the first `failures` calls.
type failingEdgeApi struct {
	*fakeAlarmApi
	failures int32
	err      error
	calls    atomic.Int32
}

func (f *failingEdgeApi) ApplyAlarmContributorEdge(ctx context.Context, d uint, a, m, r, e, s string, v *float64, o time.Time) error {
	if f.calls.Add(1) <= f.failures {
		return f.err
	}
	return f.fakeAlarmApi.ApplyAlarmContributorEdge(ctx, d, a, m, r, e, s, v, o)
}

// THE REGRESSION: an alarm edge is edge-TRIGGERED, so a dropped raise is never re-emitted. A
// database outage past the delivery budget used to dead-letter and ack it. On its LAST delivery,
// with the store refusing writes, the edge must stay unlettered and be applied once the store
// takes it. The control (no hold) shows it lettered, so the first half cannot pass on a path
// that never letters.
func TestARaiseAlarmEdgeIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, withHold := range []bool{true, false} {
		api := &failingEdgeApi{fakeAlarmApi: &fakeAlarmApi{devices: []*model.Device{{}}}, failures: 3, err: holdtest.DiskFull}
		rec := &deadRecorder{}
		rc := consumerWithDeadLetters(t, api, rec)
		if withHold {
			rc.Hold = testHold(&holdtest.Store{})
		}
		ack := &fakeAck{}
		finishesWithin(t, "handle", func() {
			rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), messaging.MaxDeliver, ack))
		})
		if withHold {
			if len(rec.msgs) != 0 || ack.acks != 1 || api.fakeAlarmApi.edgeCalls != 1 {
				t.Fatalf("held: letters=%d acks=%d applied=%d; want 0 letters, 1 ack after the edge was applied once",
					len(rec.msgs), ack.acks, api.fakeAlarmApi.edgeCalls)
			}
		} else if len(rec.msgs) != 1 || api.fakeAlarmApi.edgeCalls != 0 {
			t.Fatalf("control: letters=%d applied=%d; without a hold the edge is lettered and lost", len(rec.msgs), api.fakeAlarmApi.edgeCalls)
		}
	}
}

// A connection-class failure with a LIVE store is a poison edge: it takes its delivery budget
// and is lettered at the cap as before.
func TestAPoisonRaiseAlarmEdgeStillTakesItsDeliveryBudget(t *testing.T) {
	api := &failingEdgeApi{fakeAlarmApi: &fakeAlarmApi{devices: []*model.Device{{}}}, failures: 1 << 30, err: holdtest.ConnLost}
	rec := &deadRecorder{}
	rc := consumerWithDeadLetters(t, api, rec)
	rc.Hold = testHold(&holdtest.Store{}) // the probe answers
	ack := &fakeAck{}
	finishesWithin(t, "handle", func() {
		rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), messaging.MaxDeliver, ack))
	})
	if len(rec.msgs) != 1 || ack.acks != 1 {
		t.Fatalf("letters=%d acks=%d; want the poison edge lettered and acked at its cap", len(rec.msgs), ack.acks)
	}
}

// Shutdown during a hold leaves the edge unacked and unlettered even on its last delivery.
func TestAHeldRaiseAlarmEdgeIsLeftUnackedOnShutdown(t *testing.T) {
	api := &failingEdgeApi{fakeAlarmApi: &fakeAlarmApi{devices: []*model.Device{{}}}, failures: 1 << 30, err: holdtest.DiskFull}
	rec := &deadRecorder{}
	rc := consumerWithDeadLetters(t, api, rec)
	rc.Hold = testHold(&holdtest.Store{})
	ctx, cancel := context.WithCancel(context.Background())
	rc.procCtx = ctx
	ack := &fakeAck{}
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	finishesWithin(t, "a held edge on shutdown", func() {
		rc.handle(ctx, raiseMsg(t, "acme", validReq(), messaging.MaxDeliver, ack))
	})
	if len(rec.msgs) != 0 || ack.acks != 0 {
		t.Fatalf("letters=%d acks=%d; a released edge must be left unacked and unlettered", len(rec.msgs), ack.acks)
	}
}

// reportsApi refuses its first `failures` calls with err.
type reportsApi struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (a *reportsApi) SyncDeviceConfiguration(context.Context, string, int64) (*model.DeviceConfigurationRevision, error) {
	if a.calls.Add(1) <= a.failures {
		return nil, a.err
	}
	return &model.DeviceConfigurationRevision{}, nil
}

func (a *reportsApi) ApplyDeviceConfigurationReport(context.Context, string, int64, string, model.ConfigurationStatus, []string) error {
	if a.calls.Add(1) <= a.failures {
		return a.err
	}
	return nil
}

func reportMsg(body string, delivered int, ack *fakeAck) messaging.Message {
	return messaging.NewConsumedMessage("dc.acme.device-reports.dev-1", []byte(body), delivered, nil, ack).
		WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "dc_device-reports",
			Consumer: "dc_device-management_device-reports", Seq: 7})
}

// A device report (sync or config.status) is held through a store outage on its last delivery,
// processed afterwards and acked once; with no hold it is lettered. A poison one (connection
// error, live store) is still lettered at the cap.
func TestADeviceReportIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	bodies := map[string]string{
		"sync": `{"v":1,"kind":"sync","bootId":"b","config":{"haveRevision":0}}`,
		"config.status": `{"v":1,"kind":"config.status","revision":1,"digest":"sha256:` +
			strings.Repeat("a", 64) + `","status":"APPLIED"}`,
	}
	for kind, body := range bodies {
		for _, tc := range []struct {
			name     string
			hold     bool
			err      error
			lettered bool
		}{
			{"store down, held", true, holdtest.DiskFull, false},
			{"control: store down, no hold", false, holdtest.DiskFull, true},
			{"poison, store answers", true, holdtest.ConnLost, true},
		} {
			ms := holdTestMicroservice()
			rec := &deadRecorder{}
			api := &reportsApi{failures: 3, err: tc.err}
			if tc.lettered && tc.hold {
				api.failures = 1 << 30
			}
			rc := NewDeviceReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api,
				deadletter.NewProducer(ms).NewSink(rec), NewDeviceReportsMetrics(ms))
			if tc.hold {
				rc.Hold = testHold(&holdtest.Store{})
			}
			if err := rc.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			ack := &fakeAck{}
			finishesWithin(t, kind+"/"+tc.name, func() {
				rc.handle(context.Background(), reportMsg(body, messaging.MaxDeliver, ack))
			})
			if got := len(rec.msgs) > 0; got != tc.lettered {
				t.Fatalf("%s/%s: lettered=%v want %v", kind, tc.name, got, tc.lettered)
			}
			if ack.acks != 1 {
				t.Fatalf("%s/%s: acks=%d want 1", kind, tc.name, ack.acks)
			}
			if !tc.lettered && api.calls.Load() != 4 {
				t.Fatalf("%s/%s: store reached %d times; want 3 refused + 1 applied", kind, tc.name, api.calls.Load())
			}
		}
	}
}
