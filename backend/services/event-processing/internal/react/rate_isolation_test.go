// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/core"
)

// Freeze trigger time so the third action must exhaust a real two-token bucket.
// Changing the rule keeps both action kinds on the same dispatcher and tenant.
func TestConnectorStormStillAdmitsCommand(t *testing.T) {
	connectorRate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commandRateForConnectorStorm := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commands := &fakeSink{}
	connectors := &fakeConnectorSink{}
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://hooks.example/x"}), found: true},
		commands, nil, connectors, connectorRate, newFakeMetrics(), WithCommandRateGate(commandRateForConnectorStorm))
	ev := evt()
	ev.TriggeredAt = ev.OccurredTime
	for i := 0; i < 3; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		res := d.Dispatch(context.Background(), ev)
		if res.Outcome != Done || (i == 2 && (len(res.Shed) != 1 || res.Shed[0].Kind != "httpCall")) {
			t.Fatalf("connector storm did not exhaust its bucket: %+v", res)
		}
	}
	if len(connectors.got) != 2 {
		t.Fatalf("connector storm sent %d calls, want 2", len(connectors.got))
	}
	d.resolver = fakeResolver{rule: governedCommandRule(), found: true}
	res := d.Dispatch(context.Background(), ev)
	if res.Outcome != Done || len(res.Shed) != 0 || len(commands.sent) != 1 {
		t.Fatalf("connector storm starved sendCommand: sends=%d result=%+v", len(commands.sent), res)
	}
}

func TestCommandStormStillAdmitsConnector(t *testing.T) {
	connectorRate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commandRateForCommandStorm := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commands := &fakeSink{}
	connectors := &fakeConnectorSink{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true},
		commands, nil, connectors, connectorRate, newFakeMetrics(), WithCommandRateGate(commandRateForCommandStorm))
	ev := evt()
	ev.TriggeredAt = ev.OccurredTime
	for i := 0; i < 3; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		res := d.Dispatch(context.Background(), ev)
		if res.Outcome != Done || (i == 2 && (len(res.Shed) != 1 || res.Shed[0].Kind != "sendCommand")) {
			t.Fatalf("command storm did not exhaust its bucket: %+v", res)
		}
	}
	if len(commands.sent) != 2 {
		t.Fatalf("command storm sent %d commands, want 2", len(commands.sent))
	}
	d.resolver = fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://hooks.example/x"}), found: true}
	res := d.Dispatch(context.Background(), ev)
	if res.Outcome != Done || len(res.Shed) != 0 || len(connectors.got) != 1 {
		t.Fatalf("command storm starved httpCall: calls=%d result=%+v", len(connectors.got), res)
	}
}
