// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

func governedCommandRule() rules.Rule {
	r := sendCmdRule()
	threshold := 30.0
	r.When = rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: &threshold}
	return r
}

// Deliver-before-checkpoint: a detection drained before a crash and not yet published
// is re-derived by startup replay, and that replay is its ONLY delivery. Its command
// must still reach command-delivery (whose idempotency token collapses it if the
// first delivery did happen), without charging the outbound budget, which is closed
// here so that a charge would refuse it.
func TestStartupReplayRecoversCommandsWithoutCharging(t *testing.T) {
	for _, shards := range []int{1, 2} {
		t.Run(fmt.Sprint(shards), func(t *testing.T) { startupReplayRecoversCommands(t, shards) })
	}
}

func startupReplayRecoversCommands(t *testing.T, shards int) {
	w := &derivedCapture{}
	rp := stampingProcessor(t, diffRegistry(t), w)
	rp.cfg.Shards = shards
	rp.closeDetector()
	rp.engine = rp.newDetector()
	t.Cleanup(rp.closeDetector)
	msg := resolvedMessage(t, diffEvent{device: "device-1", occurred: testBase, value: "40"}, 1, testBase, &fakeAck{})
	rp.Replay = &fakeReplayOpener{msgs: []messaging.Message{msg}, head: 1}
	if err := rp.replayToHead(); err != nil {
		t.Fatal(err)
	}
	if len(w.events) == 0 {
		t.Fatal("replay fixture produced no detection")
	}
	sink := &reactFakeSink{}
	gate := &meterGate{admit: false}
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, gate, &deadRecorder{}, testShedBudget)
	raised := 0
	for _, ev := range w.events {
		if !ev.Replayed {
			t.Fatal("a detection derived during startup replay was not marked replayed")
		}
		if ev.Edge != runtime.EdgeResolved {
			raised++
		}
		rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	}
	if raised == 0 || len(sink.sent) != raised || len(gate.at) != 0 {
		t.Fatalf("startup replay sent %d of %d recovered commands and charged %d times; want all sent, none charged", len(sink.sent), raised, len(gate.at))
	}
	gate.admit = true
	recovered := len(sink.sent)
	// The replay path must leave no sticky suppression on the live detector.
	rp.handle(resolvedMessage(t, diffEvent{device: "device-1", occurred: testBase.Add(time.Second), value: "20"}, 2, testBase.Add(time.Second), &fakeAck{}))
	rp.handle(resolvedMessage(t, diffEvent{device: "device-1", occurred: testBase.Add(2 * time.Second), value: "40"}, 3, testBase.Add(2*time.Second), &fakeAck{}))
	rp.checkpoint(context.Background())
	for _, ev := range w.events {
		if ev.OccurredTime.After(testBase) {
			if ev.Replayed {
				t.Fatal("a live detection after replay was marked replayed, which would exempt it from the guards")
			}
			rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
		}
	}
	if len(sink.sent) <= recovered || len(gate.at) == 0 {
		t.Fatal("live detection after replay was not sent through the outbound gate")
	}
}

func TestCommandCeilingRefusesAndRecords(t *testing.T) {
	sink := &reactFakeSink{}
	dead := &deadRecorder{}
	gate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	rd, reg := reactWithConnectors(t, governedCommandRule(), sink, nil, gate, dead, testShedBudget)
	ev := sendCmdEvent()
	ev.TriggeredAt = ev.OccurredTime
	for i := 0; i < 3; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		ack := &fakeAck{}
		rd.handle(derivedMsg(t, "acme", ev, 1, ack))
		if ack.acks != 1 {
			t.Fatal("a policy refusal must make ack progress")
		}
	}
	if len(sink.sent) != 2 {
		t.Fatalf("sent %d commands, want ceiling of 2", len(sink.sent))
	}
	letters := dead.letters(t)
	if len(letters) != 1 || !strings.HasPrefix(letters[0].Detail, "sendCommand/command_rate_limited/") || letters[0].Reference != ev.RuleID {
		t.Fatalf("rate refusal not recorded against its rule: %+v", letters)
	}
	const refused = "devicechain_eventprocessing_react_command_refused_total"
	if labelledCounter(t, reg, refused, "reason", react.CommandRateLimited) != 1 || labelledCounter(t, reg, refused, "result", core.ResultUnwanted) != 1 {
		t.Fatal("rate refusal must be counted as unwanted")
	}
	// The existing outbound budget is per tenant, so another tenant has its own burst.
	ev.Tenant, ev.RuleID = "other", "other/p@1/r1"
	rd.handle(derivedMsg(t, "other", ev, 1, &fakeAck{}))
	if len(sink.sent) != 3 {
		t.Fatal("one tenant exhausted another tenant's command budget")
	}
}

// Each accepted command produces the next fresh rising detection on the same device:
// unlike a duplicate delivery, every feedback edge has a different idempotency identity.
func TestCommandSyntheticFeedbackLoopIsCutAndRecorded(t *testing.T) {
	sink := &reactFakeSink{}
	dead := &deadRecorder{}
	rd, reg := reactWithConnectors(t, governedCommandRule(), sink, nil, &meterGate{admit: true}, dead, testShedBudget)
	ev := sendCmdEvent()
	for i := 0; i < 121; i++ {
		if len(sink.sent) != i {
			t.Fatal("feedback fixture stopped producing commands before its allowance")
		}
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	}
	if len(sink.sent) != 120 {
		t.Fatalf("loop sent %d commands, want 120", len(sink.sent))
	}
	letters := dead.letters(t)
	if len(letters) != 1 || !strings.HasPrefix(letters[0].Detail, "sendCommand/command_loop/") {
		t.Fatalf("loop suppression has no distinct diagnostic: %+v", letters)
	}
	if labelledCounter(t, reg, "devicechain_eventprocessing_react_command_refused_total", "reason", react.CommandLoop) != 1 {
		t.Fatal("loop refusal was not counted distinctly")
	}
	// A different device, rule, or tenant must retain its own allowance.
	ev.Series = "device-2"
	rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	ev.Series, ev.RuleID = "device-1", "acme/p@1/r2"
	rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	ev.Tenant, ev.RuleID = "other", "other/p@1/r1"
	rd.handle(derivedMsg(t, "other", ev, 1, &fakeAck{}))
	if len(sink.sent) != 123 {
		t.Fatal("loop allowance leaked across devices, rules, or tenants")
	}
}

func TestReplayedCommandsAreSentWithoutCharging(t *testing.T) {
	sink := &reactFakeSink{}
	gate := &meterGate{admit: false}
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, gate, &deadRecorder{}, testShedBudget)
	ev := sendCmdEvent()
	for i := 0; i < 121; i++ {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		wire["replayed"] = true
		b, err = json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		rd.handle(messaging.NewConsumedMessage("dc.acme.derived-events", b, 1, nil, &fakeAck{}))
	}
	if len(sink.sent) != 121 || len(gate.at) != 0 {
		t.Fatalf("replay sent %d commands of 121 and charged %d times", len(sink.sent), len(gate.at))
	}
	// Replay must not use any of the live device/rule loop allowance either.
	gate.admit = true
	for i := 0; i < 120; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	}
	if len(sink.sent) != 241 || len(gate.at) != 120 {
		t.Fatal("replay consumed the live command allowance")
	}
}

func TestCommandOrdinarySendPreservesRequest(t *testing.T) {
	sink := &reactFakeSink{}
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, &meterGate{admit: true}, &deadRecorder{}, testShedBudget)
	ev := sendCmdEvent()
	rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	if len(sink.sent) != 1 || sink.sent[0].DeviceToken != ev.Series || sink.sent[0].Command != "setMode" || sink.sent[0].Cause == nil || sink.sent[0].Cause.Kind != react.CauseKindRule {
		t.Fatalf("ordinary command changed: %+v", sink.sent)
	}
}

func TestCommandsAndConnectorsShareOneTenantBudget(t *testing.T) {
	sink := &reactFakeSink{}
	connectors := &connSink{}
	dead := &deadRecorder{}
	gate := core.NewTenantRateLimiter(core.StaticCeiling(1, 1))
	rd, _ := reactWithConnectors(t, connectorRule(sendCommandAction(), httpCallAction()), sink, connectors, gate, dead, testShedBudget)
	ev := sendCmdEvent()
	ev.TriggeredAt = ev.OccurredTime
	rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	if len(sink.sent) != 1 || len(connectors.got) != 0 {
		t.Fatal("commands and connectors did not spend the same tenant budget")
	}
	letters := dead.letters(t)
	if len(letters) != 1 || !strings.HasPrefix(letters[0].Detail, "httpCall/shed/") {
		t.Fatalf("shared-budget connector refusal not recorded: %+v", letters)
	}
}

func TestCommandRefusalSummariesKeepReasonCodes(t *testing.T) {
	dead := &deadRecorder{}
	rd, _ := reactWithConnectors(t, governedCommandRule(), &reactFakeSink{}, nil, &meterGate{}, dead, ShedLetterBudget{})
	msg := derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{})
	rd.sheds.letter(context.Background(), msg, sendCmdEvent(), []react.ShedAction{
		{Kind: "sendCommand", Token: "a", Code: react.CommandRateLimited},
		{Kind: "sendCommand", Token: "b", Code: react.CommandLoop},
		{Kind: "sendCommand", Token: "c", Code: react.CommandGuardFull},
	})
	if len(dead.msgs) != 0 {
		t.Fatal("refusal letters exceeded their budget")
	}
	if err := rd.sheds.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	letters := dead.letters(t)
	if len(letters) != 1 {
		t.Fatalf("want one summary, got %+v", letters)
	}
	for _, code := range []string{react.CommandRateLimited, react.CommandLoop, react.CommandGuardFull} {
		if !strings.Contains(letters[0].Detail, code+"=1") {
			t.Fatalf("summary lost %s: %s", code, letters[0].Detail)
		}
	}
}
