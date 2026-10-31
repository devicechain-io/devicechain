// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func holdFor(store *holdtest.Store) *storehold.Holder {
	return &storehold.Holder{Name: "test", Probe: store.Probe, Tune: holdtest.FastTuning()}
}

func finishes(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// outageApi refuses ApplyReport with err for the first `failures` calls.
type outageApi struct {
	*fakeApi
	failures int32
	err      error
	calls    atomic.Int32
}

func (a *outageApi) ApplyReport(ctx context.Context, device string, rep ota.Report) (ota.Verdict, error) {
	if a.calls.Add(1) <= a.failures {
		return "", a.err
	}
	return a.fakeApi.ApplyReport(ctx, device, rep)
}

func lastDelivery(ack messaging.Acknowledger) messaging.Message {
	return messaging.NewConsumedMessage(reportsSubject, []byte(progressBody("RECEIVED")), messaging.MaxDeliver, nil, ack).
		WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "dc_device-reports",
			Consumer: "dc_update-management_device-reports", Seq: 7})
}

// 🔴 THE REGRESSION: a device's update report is its only copy of what it did. A database outage
// past the delivery budget used to letter it and ack it on the last delivery. Held, it stays
// unlettered, is applied once the store answers, and acked once. The control (no hold) letters
// it; a poison report (live store) still takes its budget and is lettered.
func TestAnUpdateReportIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hold     bool
		err      error
		lettered bool
	}{
		{"store down, held", true, holdtest.DiskFull, false},
		{"control: no hold", false, holdtest.DiskFull, true},
		{"poison, store answers", true, holdtest.ConnLost, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			api := &outageApi{fakeApi: &fakeApi{}, failures: 3, err: tc.err}
			if tc.lettered && tc.hold {
				api.failures = 1 << 30
			}
			dead := &letterRecorder{}
			c := NewReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api,
				deadletter.NewProducer(ms).NewSink(dead), NewUpdateReportsMetrics(ms))
			if tc.hold {
				c.Hold = holdFor(&holdtest.Store{})
			}
			ack := &ackRecorder{}
			finishes(t, "handle", func() { c.handle(context.Background(), lastDelivery(ack)) })
			require.Equal(t, tc.lettered, len(dead.msgs) > 0)
			require.Equal(t, 1, ack.acks)
			if !tc.lettered {
				require.Equal(t, int32(4), api.calls.Load(), "three refusals, then applied")
				require.Len(t, api.fakeApi.applied, 1)
			}
		})
	}
}

// Shutdown during a hold leaves the report unacked and unlettered even on its last delivery.
func TestAHeldUpdateReportIsLeftUnackedOnShutdown(t *testing.T) {
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	api := &outageApi{fakeApi: &fakeApi{}, failures: 1 << 30, err: holdtest.DiskFull}
	dead := &letterRecorder{}
	c := NewReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api,
		deadletter.NewProducer(ms).NewSink(dead), NewUpdateReportsMetrics(ms))
	c.Hold = holdFor(&holdtest.Store{})
	ctx, cancel := context.WithCancel(context.Background())
	c.procCtx = ctx
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	ack := &ackRecorder{}
	finishes(t, "a held report on shutdown", func() { c.handle(ctx, lastDelivery(ack)) })
	require.Empty(t, dead.msgs)
	require.Zero(t, ack.acks)
}

// outageRosterApi refuses DeviceRosterChanged with err for the first `failures` calls.
type outageRosterApi struct {
	failures int32
	err      error
	calls    atomic.Int32
}

func (a *outageRosterApi) DeviceRosterChanged(context.Context, string, bool) error {
	if a.calls.Add(1) <= a.failures {
		return a.err
	}
	return nil
}

// A roster fact is held through a store outage on its last delivery and applied afterwards; a
// poison one still takes its budget and is lettered.
func TestARosterFactIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hold     bool
		err      error
		lettered bool
	}{
		{"store down, held", true, holdtest.DiskFull, false},
		{"control: no hold", false, holdtest.DiskFull, true},
		{"poison, store answers", true, holdtest.ConnLost, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
			api := &outageRosterApi{failures: 3, err: tc.err}
			if tc.lettered && tc.hold {
				api.failures = 1 << 30
			}
			dead := &letterRecorder{}
			c := NewRosterConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, deadletter.NewProducer(ms).NewSink(dead))
			if tc.hold {
				c.Hold = holdFor(&holdtest.Store{})
			}
			ack := &ackRecorder{}
			m := messaging.NewConsumedMessage("inst.acme.device-roster", rosterFact(t, "dev-1", true), messaging.MaxDeliver, nil, ack).
				WithOrigin(messaging.Origin{Suffix: streams.DeviceRoster, Stream: "inst_device-roster",
					Consumer: "inst_update-management_device-roster", Seq: 5})
			finishes(t, "handle", func() { c.handle(context.Background(), m) })
			require.Equal(t, tc.lettered, len(dead.msgs) > 0)
			require.Equal(t, 1, ack.acks)
			if !tc.lettered {
				require.Equal(t, int32(4), api.calls.Load())
			}
		})
	}
}
