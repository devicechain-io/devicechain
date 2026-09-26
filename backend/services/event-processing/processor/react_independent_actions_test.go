// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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
	rule := commandThenAlarmRule()
	rule.Actions = append([]rules.Action{
		{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "reboot"}},
	}, rule.Actions...) // [reboot, setMode, raiseAlarm]
	cmd := &hungCommandSink{}
	alarms := &alarmRecorder{}
	publish := startReactOnBroker(t, ackWait, func(ms *core.Microservice, reader messaging.MessageReader, dl *deadletter.Producer) *ReactDispatcher {
		return NewReactDispatcher(ms, reader, reactFakeResolver{rule: rule, found: true}, cmd, alarms, nil, nil,
			nil, testShedBudget, NewReactMetrics(ms))
	})
	publish()

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

// startReactOnBroker runs a REACT dispatcher over a real broker the way main.go wires it — real
// reader options, a durable whose AckWait is shortened to ackWait, the max-delivery recorder — and
// returns a function that publishes sendCmdEvent for tenant acme onto the derived-events stream.
// build constructs the dispatcher over the reader, with dead letters from dl, the producer the
// max-delivery recorder uses (a Microservice can build only one: its counters register once).
func startReactOnBroker(t *testing.T, ackWait time.Duration,
	build func(ms *core.Microservice, reader messaging.MessageReader, dl *deadletter.Producer) *ReactDispatcher) func() {
	t.Helper()
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
	dl := deadletter.NewProducer(ms)
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(dl))
	nmgr.SetAckWaitForTesting(t, ackWait)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})

	rd := build(ms, reader, dl)
	require.NoError(t, rd.Start(context.Background()))
	t.Cleanup(func() { _ = rd.Stop(context.Background()) })

	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	body, err := json.Marshal(sendCmdEvent())
	require.NoError(t, err)
	return func() {
		t.Helper()
		require.NoError(t, w.WriteMessages(core.WithTenant(context.Background(), "acme"), messaging.Message{Value: body}))
	}
}

// ctxDeadRecorder is a dead-letter writer that refuses a write whose context has ended, the way a
// publish on an expired context does, and records the ones it accepted. It is safe to read while
// the dispatcher's goroutine writes.
type ctxDeadRecorder struct {
	mu   sync.Mutex
	msgs []messaging.Message
}

func (d *ctxDeadRecorder) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	d.msgs = append(d.msgs, msgs...)
	return nil
}

func (d *ctxDeadRecorder) letters(t *testing.T) []deadletter.Envelope {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]deadletter.Envelope, 0, len(d.msgs))
	for _, m := range d.msgs {
		e, err := deadletter.Unmarshal(m.Value)
		require.NoError(t, err, "a written dead letter does not read back")
		out = append(out, e)
	}
	return out
}

// At the cap the exhausted letter is the only record the event leaves, and it is written exactly
// when the delivery's time has run out: the final attempt against a command-delivery that answers
// nothing spends its whole ack deadline. The letter must be written anyway: the event is acked
// after it whether or not it lands, so a letter lost to the expired deadline leaves no record at all.
// Two things keep it — handle passes the letter a context without the deadline, and the dead-letter
// sink detaches every write from its caller's deadline — and this pins the outcome, not either one:
// the letter is lost only if BOTH give way (a writer here refuses an ended context, as a publish
// does).
//
// Real broker, real reader options, real constructor: only a capacity reader's message carries an
// ack deadline, so a hand-built message cannot show this.
func TestTheExhaustedLetterOutlivesTheDeliveryDeadline(t *testing.T) {
	const ackWait = time.Second
	dead := &ctxDeadRecorder{}
	publish := startReactOnBroker(t, ackWait, func(ms *core.Microservice, reader messaging.MessageReader, dl *deadletter.Producer) *ReactDispatcher {
		return NewReactDispatcher(ms, reader, reactFakeResolver{rule: sendCmdRule(), found: true},
			&hungCommandSink{}, nil, nil, nil, dl.NewSink(dead), testShedBudget, NewReactMetrics(ms))
	})
	publish()

	var letters []deadletter.Envelope
	require.Eventually(t, func() bool {
		letters = dead.letters(t)
		return len(letters) > 0
	}, time.Duration(messaging.MaxDeliver)*(ackWait+hungCeiling)+10*time.Second, 50*time.Millisecond,
		"no exhausted letter was written after the final delivery")
	require.Len(t, letters, 1)
	require.Equal(t, deadletter.ReasonExhausted, letters[0].Reason)
	require.Regexp(t, `^sendCommand/failed/[0-9a-f]{64}$`, letters[0].Detail)
}

// hungSuccessSink is a command-delivery that answers only at the last moment: each Send waits until
// its context ends and then reports success, so an attempt is Done but has spent its whole deadline.
type hungSuccessSink struct{}

func (hungSuccessSink) Send(ctx context.Context, _ react.CommandRequest) error {
	select {
	case <-ctx.Done():
	case <-time.After(hungCeiling):
	}
	return nil
}

// The Done path's shed letters are written after every sink call of the attempt, so on an attempt
// whose sinks took the whole ack deadline they too are written when the deadline has passed. They
// must still be written, on the same two protections as the exhausted letter.
func TestShedLettersOutliveTheDeliveryDeadline(t *testing.T) {
	const ackWait = time.Second
	dead := &ctxDeadRecorder{}
	// The shed action first, so the command after it is the one that spends the deadline: each
	// action's share is the time left over the actions left, and the last one's is all of it.
	rule := connectorRule(httpCallAction(), sendCommandAction())
	publish := startReactOnBroker(t, ackWait, func(ms *core.Microservice, reader messaging.MessageReader, dl *deadletter.Producer) *ReactDispatcher {
		return NewReactDispatcher(ms, reader, reactFakeResolver{rule: rule, found: true}, hungSuccessSink{}, nil,
			&connSink{}, &meterGate{}, dl.NewSink(dead), testShedBudget, NewReactMetrics(ms))
	})
	publish()

	var letters []deadletter.Envelope
	require.Eventually(t, func() bool {
		letters = dead.letters(t)
		return len(letters) > 0
	}, 2*(ackWait+hungCeiling)+5*time.Second, 50*time.Millisecond,
		"no shed letter was written")
	require.Equal(t, deadletter.ReasonShed, letters[0].Reason)
	require.Regexp(t, `^httpCall/shed/[0-9a-f]{64}$`, letters[0].Detail)
}

// At the cap the exhausted letter is the only record of a shed as well as of a failure: shed letters
// are written only when an attempt is Done, and an attempt that ends at the cap is not. So the letter
// names both, failures first, each by its kind and the token the rest of the platform knows it by.
func TestTheExhaustedLetterNamesTheShedActionsToo(t *testing.T) {
	rule := connectorRule(sendCommandAction(), httpCallAction())

	// The shed action's token, read off the shed letter a Done attempt of the same detection
	// writes: tokens are content hashes, so it is the one the exhausted letter must name.
	doneDead := &deadRecorder{}
	done, _ := reactWithConnectors(t, rule, &reactFakeSink{}, &connSink{}, &meterGate{}, doneDead, testShedBudget)
	done.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{}))
	shedLetters := doneDead.letters(t)
	require.Len(t, shedLetters, 1)
	shedToken, ok := strings.CutPrefix(shedLetters[0].Detail, "httpCall/shed/")
	require.True(t, ok && hexToken.MatchString(shedToken), "shed letter detail %q", shedLetters[0].Detail)

	dead := &deadRecorder{}
	cmd := &reactFakeSink{fail: true}
	rd, _ := reactWithConnectors(t, rule, cmd, &connSink{}, &meterGate{}, dead, testShedBudget)
	last := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, last))

	letters := dead.letters(t)
	require.Len(t, letters, 1, "at the cap the exhausted letter is the only one: shed letters are written on Done")
	require.Equal(t, deadletter.ReasonExhausted, letters[0].Reason)
	cmdToken := cmd.attempted[len(cmd.attempted)-1].Token
	require.Equal(t, "sendCommand/failed/"+cmdToken+"; httpCall/shed/"+shedToken, letters[0].Detail)
	require.Equal(t, 1, last.acks)
}
