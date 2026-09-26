// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// alarmRecorder is an alarm sink that records every request and can fail them all.
type alarmRecorder struct {
	mu        sync.Mutex
	raised    []react.AlarmRequest
	attempted []time.Time
	fail      bool
}

func (a *alarmRecorder) Dispatch(_ context.Context, req react.AlarmRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempted = append(a.attempted, time.Now())
	if a.fail {
		return errors.New("raise-alarm publish failed")
	}
	a.raised = append(a.raised, req)
	return nil
}

func (a *alarmRecorder) snapshot() ([]react.AlarmRequest, []time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]react.AlarmRequest(nil), a.raised...), append([]time.Time(nil), a.attempted...)
}

// constructedReactDispatcherWithAlarms is constructedReactDispatcher with an alarm sink: through
// NewReactDispatcher, with the dead-letter sink from a producer on ms.
func constructedReactDispatcherWithAlarms(ms *core.Microservice, resolver react.RuleResolver,
	cmd react.CommandSink, alarms react.AlarmSink, dead deadletter.Writer) *ReactDispatcher {
	rd := NewReactDispatcher(ms, nil, resolver, cmd, alarms, nil, nil,
		deadletter.NewProducer(ms).NewSink(dead), testShedBudget, NewReactMetrics(ms))
	rd.procCtx = context.Background()
	return rd
}

// commandThenAlarmRule is the shape the old early return lost an alarm to: a command listed
// before the alarm.
func commandThenAlarmRule() rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		Actions: []rules.Action{
			{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode"}},
			{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}},
		}}
}

var hexToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

// 🔑 THE LOSS THIS ENDS. A command that fails on every delivery — a tenant at its held-command
// limit, command-delivery down for minutes — used to take the alarm listed after it with it: the
// alarm was never raised, and the exhausted letter said only that "the actions" could not be
// dispatched. The alarm is now raised on the first delivery, re-sent under the same token on each
// retry, and the letter names exactly the action that failed, under the token the sink was sent.
func TestReactDeadLetterNamesTheActionsThatFailed(t *testing.T) {
	dead := &deadRecorder{}
	cmd := &reactFakeSink{fail: true}
	alarms := &alarmRecorder{}
	rd := constructedReactDispatcherWithAlarms(&core.Microservice{FunctionalArea: "event-processing"},
		reactFakeResolver{rule: commandThenAlarmRule(), found: true}, cmd, alarms, dead)

	first := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, first))
	raised, _ := alarms.snapshot()
	if len(raised) != 1 || raised[0].AlarmKey != "overheat" {
		t.Fatalf("the alarm after a failing command must be raised on the first delivery: %+v", raised)
	}
	if first.acks != 0 || len(dead.msgs) != 0 {
		t.Fatalf("below the cap the event must be left for redelivery: acks=%d letters=%d", first.acks, len(dead.msgs))
	}

	last := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, last))
	letters := dead.letters(t)
	if len(letters) != 1 {
		t.Fatalf("wrote %d letters at the cap, want 1", len(letters))
	}
	token := cmd.attempted[len(cmd.attempted)-1].Token
	if !hexToken.MatchString(token) {
		t.Fatalf("the command sink was sent %q, not a content token", token)
	}
	if want := "sendCommand/failed/" + token; letters[0].Detail != want {
		t.Fatalf("Detail = %q, want exactly %q: the letter must name the failed action, and only it", letters[0].Detail, want)
	}
	if letters[0].Reference != "acme/p@1/r1" {
		t.Fatalf("Reference = %q", letters[0].Reference)
	}
	if last.acks != 1 {
		t.Fatalf("the lettered event must be acked: acks=%d", last.acks)
	}
	raised, _ = alarms.snapshot()
	if len(raised) != 2 || raised[0].Token == "" || raised[0].Token != raised[1].Token {
		t.Fatalf("the retry must re-send the alarm under the one token its stream collapses on: %+v", raised)
	}
}

// A rule that cannot be read on the final attempt attempted nothing, and the letter says so rather
// than naming no action and leaving the reader to guess.
func TestReactDeadLetterSaysTheRuleCouldNotBeRead(t *testing.T) {
	dead := &deadRecorder{}
	rd := constructedReactDispatcherWithAlarms(&core.Microservice{FunctionalArea: "event-processing"},
		reactFakeResolver{err: errors.New("store down")}, &reactFakeSink{}, &alarmRecorder{}, dead)
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, &fakeAck{}))
	letters := dead.letters(t)
	if len(letters) != 1 {
		t.Fatalf("wrote %d letters, want 1", len(letters))
	}
	if want := "the rule could not be read on the final attempt, so no action was attempted"; letters[0].Detail != want {
		t.Fatalf("Detail = %q, want %q", letters[0].Detail, want)
	}
}

// hungCall is one call into hungCommandSink.
type hungCall struct {
	deadline    time.Time
	hasDeadline bool
	start, end  time.Time
}

// hungCommandSink is command-delivery not answering: each Send waits until its context ends, or
// until a ceiling far past the test's AckWait (which is what an unbounded attempt would hit).
type hungCommandSink struct {
	mu    sync.Mutex
	calls []hungCall
}

const hungCeiling = 8 * time.Second

func (s *hungCommandSink) Send(ctx context.Context, _ react.CommandRequest) error {
	c := hungCall{start: time.Now()}
	c.deadline, c.hasDeadline = ctx.Deadline()
	select {
	case <-ctx.Done():
	case <-time.After(hungCeiling):
	}
	c.end = time.Now()
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	return errors.New("command-delivery did not answer")
}

func (s *hungCommandSink) snapshot() []hungCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hungCall(nil), s.calls...)
}

// Every action is attempted, so an attempt against sinks that hang would cost one timeout PER
// ACTION — and one that outlived its delivery would be redelivered and handled again while still
// running, spreading its re-publishes past the duplicate window that collapses them. The reader
// REACT is built with (ReactReaderOptions, as main.go builds it) stamps each delivery with its ack
// deadline, and handle bounds every sink call by it: two hung commands and the alarm after them all
// finish inside ONE AckWait, and the alarm is still raised.
//
// Real broker, real reader options, real constructor. The broker's AckWait is shortened to one
// second so the bound is visible against the hung sink's much longer ceiling.
func TestAnAttemptEndsWithItsDelivery(t *testing.T) {
	const ackWait = time.Second
	b := startDetectBroker(t)

	ms := &core.Microservice{InstanceId: coveredInstance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: b.host, Port: b.port}
	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := m.NewReader(streams.DerivedEvents, ReactReaderOptions(func() bool { return true })...)
		reader = r
		return err
	})
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	nmgr.SetAckWaitForTesting(t, ackWait)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	rule := commandThenAlarmRule()
	rule.Actions = append([]rules.Action{
		{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "reboot"}},
	}, rule.Actions...) // [reboot, setMode, raiseAlarm]
	cmd := &hungCommandSink{}
	alarms := &alarmRecorder{}
	rd := NewReactDispatcher(ms, reader, reactFakeResolver{rule: rule, found: true}, cmd, alarms, nil, nil,
		nil, testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	t.Cleanup(func() { _ = rd.Stop(context.Background()) })

	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	body, err := json.Marshal(sendCmdEvent())
	require.NoError(t, err)
	require.NoError(t, w.WriteMessages(core.WithTenant(context.Background(), "acme"), messaging.Message{Value: body}))

	var alarmAt []time.Time
	require.Eventually(t, func() bool {
		_, alarmAt = alarms.snapshot()
		return len(alarmAt) > 0
	}, 2*hungCeiling+5*time.Second, 20*time.Millisecond, "the alarm after two hung commands was never attempted")

	calls := cmd.snapshot()
	require.GreaterOrEqual(t, len(calls), 2, "both commands must be attempted before the alarm")
	firstCall, secondCall := calls[0], calls[1]
	require.True(t, firstCall.hasDeadline, "the sink call carried no deadline: the attempt is unbounded")
	require.LessOrEqual(t, firstCall.deadline.Sub(firstCall.start), ackWait,
		"the sink call's deadline is later than its delivery's AckWait")
	require.LessOrEqual(t, alarmAt[0].Sub(firstCall.start), ackWait+500*time.Millisecond,
		"the attempt ran past its delivery: two hung commands took %v before the alarm was reached",
		alarmAt[0].Sub(firstCall.start))
	require.True(t, secondCall.end.Before(alarmAt[0]) || secondCall.end.Equal(alarmAt[0]),
		"the second command must be attempted on the same attempt, before the alarm")
}
