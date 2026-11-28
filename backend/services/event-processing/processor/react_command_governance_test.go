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
	// The durable had handed the message to the term that died: its detection is re-derived.
	rp.ResolvedEventsReader = &deliveredReader{fakeReader: &fakeReader{}, through: 1}
	if err := rp.replayToHead(); err != nil {
		t.Fatal(err)
	}
	if len(w.events) == 0 {
		t.Fatal("replay fixture produced no detection")
	}
	sink := &reactFakeSink{}
	gate := &meterGate{admit: false}
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, &deadRecorder{}, testShedBudget, react.WithCommandRateGate(gate))
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

// deliveredReader reports how far the resolved-events durable had delivered before this term.
type deliveredReader struct {
	*fakeReader
	through uint64
}

func (r *deliveredReader) DeliveredThrough(context.Context) (uint64, error) { return r.through, nil }

// Startup replay covers the window past the checkpoint, and only the part of it the durable had
// already handed to a term is a re-derivation. Events that arrived while nothing was consuming
// were never applied by any engine: their detections are fresh triggers, and their commands must
// be charged to the tenant's ceiling and the loop guard like live ones, or a backlog of any size
// actuates past both. The re-derived part is still sent uncharged.
func TestStartupReplayGovernsTheBacklogNoTermWasGiven(t *testing.T) {
	for _, shards := range []int{1, 2} {
		t.Run(fmt.Sprint(shards), func(t *testing.T) { startupReplayGovernsBacklog(t, shards) })
	}
}

func startupReplayGovernsBacklog(t *testing.T, shards int) {
	for _, tc := range []struct {
		name      string
		reader    messaging.MessageReader
		rederived map[string]bool
	}{
		// Sequence 1 had been delivered to the term that died; 2 and 3 arrived after it.
		{"delivered through 1", &deliveredReader{fakeReader: &fakeReader{}, through: 1}, map[string]bool{"device-1": true}},
		// A reader that cannot say proves no earlier delivery: everything is governed.
		{"unknown", nil, map[string]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &derivedCapture{}
			rp := stampingProcessor(t, diffRegistry(t), w)
			rp.cfg.Shards = shards
			rp.closeDetector()
			rp.engine = rp.newDetector()
			t.Cleanup(rp.closeDetector)
			var msgs []messaging.Message
			for i := 1; i <= 3; i++ {
				at := testBase.Add(time.Duration(i-1) * time.Second)
				msgs = append(msgs, resolvedMessage(t, diffEvent{device: fmt.Sprintf("device-%d", i), occurred: at, value: "40"}, uint64(i), at, &fakeAck{}))
			}
			rp.Replay = &fakeReplayOpener{msgs: msgs, head: 3}
			rp.ResolvedEventsReader = tc.reader
			if err := rp.replayToHead(); err != nil {
				t.Fatal(err)
			}
			sink := &reactFakeSink{}
			gate := &meterGate{admit: false}
			rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, &deadRecorder{}, testShedBudget, react.WithCommandRateGate(gate))
			rederived, fresh := 0, 0
			for _, ev := range w.events {
				if ev.Edge == runtime.EdgeResolved {
					continue
				}
				if ev.Replayed != tc.rederived[ev.Series] {
					t.Fatalf("%s's detection marked replayed=%v; only a detection of an event an earlier term was given is re-derived", ev.Series, ev.Replayed)
				}
				if ev.Replayed {
					rederived++
				} else {
					fresh++
				}
				rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
			}
			if rederived != len(tc.rederived) || fresh != 3-len(tc.rederived) {
				t.Fatalf("fixture produced %d re-derived and %d fresh raises", rederived, fresh)
			}
			// The ceiling is closed: a fresh command is charged and refused, a re-derived one is sent uncharged.
			if len(sink.sent) != rederived || len(gate.at) != fresh {
				t.Fatalf("sent %d (want the %d re-derived) and charged %d (want the %d fresh)", len(sink.sent), rederived, len(gate.at), fresh)
			}
			for _, req := range sink.sent {
				if !tc.rederived[req.DeviceToken] {
					t.Fatalf("a backlog command for %s was sent past a closed ceiling", req.DeviceToken)
				}
			}
		})
	}
}

func TestCommandCeilingRefusesAndRecords(t *testing.T) {
	sink := &reactFakeSink{}
	dead := &deadRecorder{}
	gate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	rd, reg := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, dead, testShedBudget, react.WithCommandRateGate(gate))
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
	rd, reg := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, dead, testShedBudget, react.WithCommandRateGate(&meterGate{admit: true}))
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
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, &deadRecorder{}, testShedBudget, react.WithCommandRateGate(gate))
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
	rd, _ := reactWithConnectors(t, governedCommandRule(), sink, nil, nil, &deadRecorder{}, testShedBudget, react.WithCommandRateGate(&meterGate{admit: true}))
	ev := sendCmdEvent()
	rd.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	if len(sink.sent) != 1 || sink.sent[0].DeviceToken != ev.Series || sink.sent[0].Command != "setMode" || sink.sent[0].Cause == nil || sink.sent[0].Cause.Kind != react.CauseKindRule {
		t.Fatalf("ordinary command changed: %+v", sink.sent)
	}
}

// A storm in either action family must shed within its own bucket and leave
// the other family's equally sized allowance intact.
func TestConnectorStormStillAdmitsCommand(t *testing.T) {
	connectorGate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commandGate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commands := &reactFakeSink{}
	connectors := &connSink{}
	dead := &deadRecorder{}
	storm, _ := reactWithConnectors(t, connectorRule(httpCallAction()), nil, connectors, connectorGate, dead, testShedBudget)
	target, _ := reactWithConnectors(t, connectorRule(sendCommandAction()), commands, nil, connectorGate, dead, testShedBudget, react.WithCommandRateGate(commandGate))
	ev := sendCmdEvent()
	ev.TriggeredAt = ev.OccurredTime
	for i := 0; i < 3; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		storm.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	}
	if len(connectors.got) != 2 || len(dead.letters(t)) != 1 {
		t.Fatal("connector storm did not exhaust and shed from its bucket")
	}
	target.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	if len(commands.sent) != 1 || len(dead.letters(t)) != 1 {
		t.Fatal("connector storm spent the command budget")
	}
}

func TestCommandStormStillAdmitsHTTPCall(t *testing.T) {
	connectorGate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commandGate := core.NewTenantRateLimiter(core.StaticCeiling(1, 2))
	commands := &reactFakeSink{}
	connectors := &connSink{}
	dead := &deadRecorder{}
	storm, _ := reactWithConnectors(t, connectorRule(sendCommandAction()), commands, nil, connectorGate, dead, testShedBudget, react.WithCommandRateGate(commandGate))
	target, _ := reactWithConnectors(t, connectorRule(httpCallAction()), nil, connectors, connectorGate, dead, testShedBudget)
	ev := sendCmdEvent()
	ev.TriggeredAt = ev.OccurredTime
	for i := 0; i < 3; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		storm.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	}
	if len(commands.sent) != 2 || len(dead.letters(t)) != 1 {
		t.Fatal("command storm did not exhaust and shed from its bucket")
	}
	target.handle(derivedMsg(t, "acme", ev, 1, &fakeAck{}))
	if len(connectors.got) != 1 || len(dead.letters(t)) != 1 {
		t.Fatal("command storm spent the connector budget")
	}
}

func TestCommandRefusalSummariesKeepReasonCodes(t *testing.T) {
	dead := &deadRecorder{}
	rd, _ := reactWithConnectors(t, governedCommandRule(), &reactFakeSink{}, nil, nil, dead, ShedLetterBudget{}, react.WithCommandRateGate(&meterGate{}))
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
