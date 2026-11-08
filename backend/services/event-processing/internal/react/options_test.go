// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/rs/zerolog"
)

// Every fail-closed skip reaches a drop observer with its reason and goes to the logger the
// dispatcher was given, not the process logger; live metrics are untouched by the new reasons.
func TestDropObserverAndLoggerSeeEveryFailClosedSkip(t *testing.T) {
	var logs bytes.Buffer
	var drops []string
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{
			{Type: rules.ActionSendCommand}, // missing variant
			{Type: rules.ActionRaiseAlarm},  // missing variant
			{Type: rules.ActionHTTPCall},    // missing variant
			{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "c", ExpiresAfter: "nope"}}, // invalid
			{Type: rules.ActionSendCommand, Guard: "this is not cel (", SendCommand: &rules.SendCommandAction{Command: "c"}},
			{Type: "teleport"},
		}}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, &fakeSink{}, &fakeAlarmSink{}, &fakeConnectorSink{}, nil, m,
		WithLogger(zerolog.New(&logs)), WithDropObserver(func(r string) { drops = append(drops, r) }))
	d.Dispatch(context.Background(), evt())
	want := []string{DropMissingVariant, DropMissingVariant, DropMissingVariant, DropInvalidAction, DropGuardError, DropUnknownKind}
	if strings.Join(drops, ",") != strings.Join(want, ",") {
		t.Fatalf("drops = %v, want %v", drops, want)
	}
	if n := strings.Count(logs.String(), "\n"); n != len(want) {
		t.Fatalf("the given logger got %d lines, want %d:\n%s", n, len(want), logs.String())
	}
}

// WithClock measures a command's expiry from the clock it is given, read at each dispatch.
func TestWithClockMeasuresExpiryFromTheGivenClock(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	sink := &fakeSink{}
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "c", ExpiresAfter: "15m"}}}}
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, sink, nil, nil, nil, newFakeMetrics(),
		WithClock(func() time.Time { return at }), WithLogger(zerolog.Nop()))
	d.Dispatch(context.Background(), evt())
	at = at.Add(time.Hour)
	d.Dispatch(context.Background(), evt())
	if len(sink.sent) != 2 || !sink.sent[0].ExpiresAt.Equal(time.Date(2026, 3, 1, 0, 15, 0, 0, time.UTC)) ||
		!sink.sent[1].ExpiresAt.Equal(time.Date(2026, 3, 1, 1, 15, 0, 0, time.UTC)) {
		t.Fatalf("sent = %+v, want expiries 15m after each clock reading", sink.sent)
	}
}
