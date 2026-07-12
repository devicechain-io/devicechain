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
	devices   []*model.Device
	devErr    error
	edgeErr   error
	edgeCalls int
	lastEdge  edgeArgs
}

type edgeArgs struct {
	deviceId                    uint
	alarmKey, metricKey, ruleID string
	edge, severity              string
	value                       *float64
	occurredTime                time.Time
}

func fptr(v float64) *float64 { return &v }

func (f *fakeAlarmApi) DevicesByToken(_ context.Context, _ []string) ([]*model.Device, error) {
	return f.devices, f.devErr
}

// RaiseAlarm is unused by the consumer post-6d-pre-2c (it routes to ApplyAlarmContributorEdge), kept
// only to satisfy the interface.
func (f *fakeAlarmApi) RaiseAlarm(context.Context, uint, string, string, string, *float64, time.Time) error {
	return nil
}

func (f *fakeAlarmApi) ApplyAlarmContributorEdge(_ context.Context, deviceId uint, alarmKey, metricKey, ruleID, edge, severity string, value *float64, occurredTime time.Time) error {
	f.edgeCalls++
	f.lastEdge = edgeArgs{deviceId, alarmKey, metricKey, ruleID, edge, severity, value, occurredTime}
	return f.edgeErr
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
		DeviceToken: "device-1", AlarmKey: "acme/p@1/r1", RuleID: "acme/p@1/r1", MetricKey: "temperature",
		Edge: model.AlarmEdgeRaised, Severity: "critical", Value: fptr(91),
		OccurredTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

// TestRaiseAlarmConsumerApplies proves a valid raised request resolves the device, maps severity to
// the UPPERCASE tier, applies the RAISED contributor edge with the rule id, and acks.
func TestRaiseAlarmConsumerApplies(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}}
	api.devices[0].ID = 42
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	rc.handle(context.Background(), raiseMsg(t, "acme", validReq(), 0, ack))

	if api.edgeCalls != 1 {
		t.Fatalf("expected one ApplyAlarmContributorEdge call, got %d", api.edgeCalls)
	}
	e := api.lastEdge
	if e.deviceId != 42 || e.severity != "CRITICAL" || e.alarmKey != "acme/p@1/r1" || e.ruleID != "acme/p@1/r1" || e.edge != model.AlarmEdgeRaised {
		t.Fatalf("edge args wrong: %+v", e)
	}
	if e.value == nil || *e.value != 91 {
		t.Fatalf("the triggering value must flow through; got %v", e.value)
	}
	if ack.acks != 1 || ack.naks != 0 {
		t.Fatalf("a successful apply must ack once: acks=%d naks=%d", ack.acks, ack.naks)
	}

	// A value-less request (a silence-driven fire) carries a nil value through unchanged.
	apiNil := &fakeAlarmApi{devices: []*model.Device{{}}}
	rcNil := newTestConsumer(apiNil)
	req := validReq()
	req.Value = nil
	rcNil.handle(context.Background(), raiseMsg(t, "acme", req, 0, &fakeAck{}))
	if apiNil.edgeCalls != 1 || apiNil.lastEdge.value != nil {
		t.Fatalf("a value-less request must apply with a nil value; got calls=%d value=%v", apiNil.edgeCalls, apiNil.lastEdge.value)
	}
}

// TestRaiseAlarmConsumerRoutesResolvedEdge proves a RESOLVED edge now reaches the integrator carrying
// edge=resolved (6d-pre-2c) — where the contributor is removed — rather than being dropped. It
// marshals the real wire bytes and compares the shared AlarmEdgeResolved constant, pinning the
// producer/consumer edge spelling against fail-open drift. A resolve needs no severity.
func TestRaiseAlarmConsumerRoutesResolvedEdge(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}}
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	req := validReq()
	req.Edge = model.AlarmEdgeResolved
	req.Severity = "" // a resolve does not require a tier
	req.Value = nil
	rc.handle(context.Background(), raiseMsg(t, "acme", req, 0, ack))
	if api.edgeCalls != 1 || api.lastEdge.edge != model.AlarmEdgeResolved {
		t.Fatalf("a resolved edge must reach the integrator with edge=resolved; got calls=%d edge=%q", api.edgeCalls, api.lastEdge.edge)
	}
	if ack.acks != 1 || ack.naks != 0 {
		t.Fatalf("a successful resolve must ack: acks=%d naks=%d", ack.acks, ack.naks)
	}
}

// TestRaiseAlarmConsumerPoison sweeps the drop-and-ack cases: empty device/key/rule, unknown RAISE
// severity, an unknown edge token, and a device that no longer exists — none apply an edge, all ack.
func TestRaiseAlarmConsumerPoison(t *testing.T) {
	cases := []struct {
		name    string
		req     model.RaiseAlarmRequest
		devices []*model.Device
	}{
		{"empty device", func() model.RaiseAlarmRequest { r := validReq(); r.DeviceToken = ""; return r }(), []*model.Device{{}}},
		{"empty alarm key", func() model.RaiseAlarmRequest { r := validReq(); r.AlarmKey = ""; return r }(), []*model.Device{{}}},
		{"empty rule id", func() model.RaiseAlarmRequest { r := validReq(); r.RuleID = ""; return r }(), []*model.Device{{}}},
		{"unknown raise severity", func() model.RaiseAlarmRequest { r := validReq(); r.Severity = "catastrophic"; return r }(), []*model.Device{{}}},
		{"zero time", func() model.RaiseAlarmRequest { r := validReq(); r.OccurredTime = time.Time{}; return r }(), []*model.Device{{}}},
		{"unknown edge", func() model.RaiseAlarmRequest { r := validReq(); r.Edge = "sideways"; return r }(), []*model.Device{{}}},
		{"device gone", validReq(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAlarmApi{devices: tc.devices}
			rc := newTestConsumer(api)
			ack := &fakeAck{}
			rc.handle(context.Background(), raiseMsg(t, "acme", tc.req, 0, ack))
			if api.edgeCalls != 0 {
				t.Fatalf("poison request must not apply an edge")
			}
			if ack.acks != 1 || ack.naks != 0 {
				t.Fatalf("poison must ack-drop: acks=%d naks=%d", ack.acks, ack.naks)
			}
		})
	}
}

// TestRaiseAlarmConsumerLegacyEmptyEdgeRaises proves an empty edge (a legacy/pre-edge request) is
// treated as a RAISE, never a resolve — so an unstamped request can't be mis-routed to a clear.
func TestRaiseAlarmConsumerLegacyEmptyEdgeRaises(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}}
	rc := newTestConsumer(api)
	req := validReq()
	req.Edge = ""
	rc.handle(context.Background(), raiseMsg(t, "acme", req, 0, &fakeAck{}))
	if api.edgeCalls != 1 || api.lastEdge.edge != model.AlarmEdgeRaised {
		t.Fatalf("an empty edge must apply as raised; got calls=%d edge=%q", api.edgeCalls, api.lastEdge.edge)
	}
}

// TestRaiseAlarmConsumerUndecodable proves a non-JSON body is dropped-and-acked.
func TestRaiseAlarmConsumerUndecodable(t *testing.T) {
	api := &fakeAlarmApi{}
	rc := newTestConsumer(api)
	ack := &fakeAck{}
	rc.handle(context.Background(), messaging.NewConsumedMessage("dc.acme.raise-alarm", []byte("not json"), 0, nil, ack))
	if api.edgeCalls != 0 || ack.acks != 1 {
		t.Fatalf("undecodable must ack-drop without applying: edgeCalls=%d acks=%d", api.edgeCalls, ack.acks)
	}
}

// TestRaiseAlarmConsumerTransientRetry proves a store failure below the cap naks (retry), and at the
// cap acks (drops).
func TestRaiseAlarmConsumerTransientRetry(t *testing.T) {
	api := &fakeAlarmApi{devices: []*model.Device{{}}, edgeErr: errors.New("db down")}
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
	if ack.naks != 1 || ack.acks != 0 || api.edgeCalls != 0 {
		t.Fatalf("a resolve error must nak (retry): acks=%d naks=%d edgeCalls=%d", ack.acks, ack.naks, api.edgeCalls)
	}
}
