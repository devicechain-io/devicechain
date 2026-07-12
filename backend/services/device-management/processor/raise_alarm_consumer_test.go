// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// fakeAlarmApi implements DeviceManagementApi by embedding a (nil) MockApi to satisfy the whole
// interface, overriding only the two methods the raise-alarm consumer calls with plain, inspectable
// behavior — so the test needs no testify expectation plumbing for the unused methods.
type fakeAlarmApi struct {
	*dmtest.MockApi
	devices    []*model.Device
	devErr     error
	raiseErr   error
	raiseCalls int
	lastRaise  raiseArgs
}

type raiseArgs struct {
	deviceId            uint
	alarmKey, metricKey string
	severity            string
	value               *float64
	occurredTime        time.Time
}

func fptr(v float64) *float64 { return &v }

func (f *fakeAlarmApi) DevicesByToken(_ context.Context, _ []string) ([]*model.Device, error) {
	return f.devices, f.devErr
}

func (f *fakeAlarmApi) RaiseAlarm(_ context.Context, deviceId uint, alarmKey, metricKey, severity string, value *float64, occurredTime time.Time) error {
	f.raiseCalls++
	f.lastRaise = raiseArgs{deviceId, alarmKey, metricKey, severity, value, occurredTime}
	return f.raiseErr
}

// fakeAck records ack/nak counts.
type fakeAck struct {
	acks, naks int
}

func (a *fakeAck) Ack() error { a.acks++; return nil }
func (a *fakeAck) Nak() error { a.naks++; return nil }

func newTestConsumer(api model.DeviceManagementApi) *RaiseAlarmConsumer {
	rc := &RaiseAlarmConsumer{Api: api, metrics: nil}
	rc.procCtx = context.Background()
	return rc
}

func raiseMsg(t *testing.T, tenant string, req model.RaiseAlarmRequest, numDelivered int, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := model.MarshalRaiseAlarmRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	return messaging.NewConsumedMessage("dc."+tenant+".raise-alarm", b, numDelivered, nil, ack)
}

func validReq() model.RaiseAlarmRequest {
	return model.RaiseAlarmRequest{
		DeviceToken: "device-1", AlarmKey: "acme/p@1/r1", MetricKey: "temperature",
		Severity: "critical", Value: fptr(91), OccurredTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

// TestRaiseAlarmConsumerApplies proves a valid request resolves the device, maps severity to the
// UPPERCASE tier, calls RaiseAlarm, and acks.
func TestRaiseAlarmConsumerApplies(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}}
	api.devices[0].ID = 42
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), 0, ack))

	if api.raiseCalls != 1 {
		t.Fatalf("expected one RaiseAlarm call, got %d", api.raiseCalls)
	}
	if api.lastRaise.deviceId != 42 || api.lastRaise.severity != "CRITICAL" || api.lastRaise.alarmKey != "acme/p@1/r1" {
		t.Fatalf("RaiseAlarm args wrong: %+v", api.lastRaise)
	}
	if api.lastRaise.value == nil || *api.lastRaise.value != 91 {
		t.Fatalf("the triggering value must flow through to RaiseAlarm; got %v", api.lastRaise.value)
	}
	if ack.acks != 1 || ack.naks != 0 {
		t.Fatalf("a successful raise must ack once: acks=%d naks=%d", ack.acks, ack.naks)
	}

	// A value-less request (a silence-driven fire) carries a nil value through unchanged, so
	// device-management leaves the alarm's last value NULL rather than fabricating a 0.
	apiNil := &fakeAlarmApi{devices: []*model.Device{{}}}
	rcNil := newTestConsumer(apiNil)
	req := validReq()
	req.Value = nil
	rcNil.handle(context.Background(), raiseMsg(t, "acme", req, 0, &fakeAck{}))
	if apiNil.raiseCalls != 1 || apiNil.lastRaise.value != nil {
		t.Fatalf("a value-less request must raise with a nil value; got calls=%d value=%v", apiNil.raiseCalls, apiNil.lastRaise.value)
	}
}

// TestRaiseAlarmConsumerPoison sweeps the drop-and-ack cases: unparseable, empty device/key,
// unknown severity, and a device that no longer exists — none call RaiseAlarm, all ack.
func TestRaiseAlarmConsumerPoison(t *testing.T) {
	cases := []struct {
		name    string
		req     model.RaiseAlarmRequest
		devices []*model.Device
	}{
		{"empty device", func() model.RaiseAlarmRequest { r := validReq(); r.DeviceToken = ""; return r }(), []*model.Device{{}}},
		{"empty alarm key", func() model.RaiseAlarmRequest { r := validReq(); r.AlarmKey = ""; return r }(), []*model.Device{{}}},
		{"unknown severity", func() model.RaiseAlarmRequest { r := validReq(); r.Severity = "catastrophic"; return r }(), []*model.Device{{}}},
		{"zero time", func() model.RaiseAlarmRequest { r := validReq(); r.OccurredTime = time.Time{}; return r }(), []*model.Device{{}}},
		{"device gone", validReq(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAlarmApi{devices: tc.devices}
			rc := newTestConsumer(api)
			ack := &fakeAck{}
			rc.handle(context.Background(), raiseMsg(t, "acme", tc.req, 0, ack))
			if api.raiseCalls != 0 {
				t.Fatalf("poison request must not raise an alarm")
			}
			if ack.acks != 1 || ack.naks != 0 {
				t.Fatalf("poison must ack-drop: acks=%d naks=%d", ack.acks, ack.naks)
			}
		})
	}
}

// TestRaiseAlarmConsumerDropsResolvedEdge proves the 6d-pre-2b staging guard: a request carrying the
// RESOLVED edge is dropped WITHOUT reaching the raise path (the contributor-resolve integrator lands
// in 6d-pre-2c), and acked. It marshals the real wire bytes and compares the shared AlarmEdgeResolved
// constant — so it also pins the producer/consumer edge spelling against fail-open drift. A raised edge
// (and the default empty edge) still raises.
func TestRaiseAlarmConsumerDropsResolvedEdge(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}}
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	req := validReq()
	req.Edge = model.AlarmEdgeResolved
	rc.handle(context.Background(), raiseMsg(t, "acme", req, 0, ack))
	if api.raiseCalls != 0 {
		t.Fatalf("a resolved edge must NOT reach the raise path (integrator is 6d-pre-2c); got %d calls", api.raiseCalls)
	}
	if ack.acks != 1 || ack.naks != 0 {
		t.Fatalf("a dropped resolved edge must ack: acks=%d naks=%d", ack.acks, ack.naks)
	}

	// A raised edge (explicit) still raises through the existing path.
	apiR := &fakeAlarmApi{devices: []*model.Device{{}}}
	rcR := newTestConsumer(apiR)
	reqR := validReq()
	reqR.Edge = model.AlarmEdgeRaised
	rcR.handle(context.Background(), raiseMsg(t, "acme", reqR, 0, &fakeAck{}))
	if apiR.raiseCalls != 1 {
		t.Fatalf("a raised edge must still raise; got %d calls", apiR.raiseCalls)
	}
}

// TestRaiseAlarmConsumerUndecodable proves a non-JSON body is dropped-and-acked.
func TestRaiseAlarmConsumerUndecodable(t *testing.T) {
	api := &fakeAlarmApi{}
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	rc.handle(context.Background(), messaging.NewConsumedMessage("dc.acme.raise-alarm", []byte("not json"), 0, nil, ack))
	if api.raiseCalls != 0 || ack.acks != 1 {
		t.Fatalf("undecodable must ack-drop without raising: raiseCalls=%d acks=%d", api.raiseCalls, ack.acks)
	}
}

// TestRaiseAlarmConsumerTransientRetry proves a store failure below the cap naks (retry), and at the
// cap acks (drops).
func TestRaiseAlarmConsumerTransientRetry(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}, raiseErr: errors.New("db down")}
	rc := newTestConsumer(api)

	below := &fakeAck{}
	rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), 1, below))
	if below.naks != 1 || below.acks != 0 {
		t.Fatalf("a transient failure below the cap must nak: acks=%d naks=%d", below.acks, below.naks)
	}

	atcap := &fakeAck{}
	rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), messaging.MaxDeliver, atcap))
	if atcap.acks != 1 || atcap.naks != 0 {
		t.Fatalf("at the redelivery cap a failing request must ack-drop: acks=%d naks=%d", atcap.acks, atcap.naks)
	}
}

// TestRaiseAlarmConsumerResolveErrorRetries proves a device-resolution STORE error naks (retry) —
// distinct from a device-gone (empty result), which drops.
func TestRaiseAlarmConsumerResolveErrorRetries(t *testing.T) {
	api := &fakeAlarmApi{devErr: errors.New("db down")}
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), 0, ack))
	if ack.naks != 1 || ack.acks != 0 || api.raiseCalls != 0 {
		t.Fatalf("a resolve error must nak (retry): acks=%d naks=%d raiseCalls=%d", ack.acks, ack.naks, api.raiseCalls)
	}
}
