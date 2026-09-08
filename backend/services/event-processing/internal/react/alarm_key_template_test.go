// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// alarmKeyRule builds a one-action rule raising under either a literal key or a rendered one.
func alarmKeyRule(key, template string) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm,
			RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: key, AlarmKeyTemplate: template}}}}
}

// evtFor is a rising-edge detection for one device.
func evtFor(series string) runtime.DerivedEvent {
	e := raisedEvt(f64(120))
	e.Series = series
	return e
}

// dispatchAlarm runs one detection through a dispatcher over rule and returns the alarm requests it
// produced.
func dispatchAlarm(t *testing.T, rule rules.Rule, ev runtime.DerivedEvent) []AlarmRequest {
	t.Helper()
	sink := &fakeAlarmSink{}
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, sink, nil, nil, newFakeMetrics())
	if out := d.Dispatch(context.Background(), ev); out != Done {
		t.Fatalf("outcome = %v, want Done", out)
	}
	return sink.raised
}

// 🔴 THE LOAD-BEARING TEST FOR THIS FEATURE: a rendered key must actually DIFFER per device.
//
// A rendering that was wired up but ignored its input — a template compiled and evaluated against a
// constant, or a resolver that fell back to the static field — would still "render", still log
// nothing, and still file alarms. It would just file them all under one key. Two devices, two keys,
// asserted to be different AND to be the values the template names, is what separates "the renderer
// ran" from "the renderer rendered".
func TestAlarmKeyTemplateRendersPerDevice(t *testing.T) {
	rule := alarmKeyRule("", `"overtemp-" + series`)

	first := dispatchAlarm(t, rule, evtFor("pump-01"))
	second := dispatchAlarm(t, rule, evtFor("pump-02"))

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("want one alarm request per device, got %d and %d", len(first), len(second))
	}
	if first[0].AlarmKey == second[0].AlarmKey {
		t.Fatalf("both devices filed under the SAME key %q — the template is not reading the series",
			first[0].AlarmKey)
	}
	if got, want := first[0].AlarmKey, "overtemp-pump-01"; got != want {
		t.Fatalf("pump-01 alarm key = %q, want %q", got, want)
	}
	if got, want := second[0].AlarmKey, "overtemp-pump-02"; got != want {
		t.Fatalf("pump-02 alarm key = %q, want %q", got, want)
	}
}

// The counterweight, and the compatibility contract: a rule authored before alarm-key templates
// existed carries a literal key with no template, and that key must reach the sink UNCHANGED. A
// literal like "over-temp" is not even valid CEL (it parses as a subtraction of two undeclared
// identifiers), so a design that had made AlarmKey itself the template would have broken every
// published rule — which is exactly why the template lives on its own field.
func TestLiteralAlarmKeyIsNotRendered(t *testing.T) {
	got := dispatchAlarm(t, alarmKeyRule("over-temp", ""), evtFor("pump-01"))
	if len(got) != 1 {
		t.Fatalf("want one alarm request, got %d", len(got))
	}
	if got[0].AlarmKey != "over-temp" {
		t.Fatalf("literal alarm key came through as %q, want %q unchanged", got[0].AlarmKey, "over-temp")
	}
}

// 🔴 The edge-pairing invariant. A raiseAlarm dispatches on both edges and the alarm key is what
// pairs them: if the falling edge rendered a different key, the raise would be stranded ACTIVE
// forever with its clear applied to a stranger. The falling edge here carries NO value — the shape
// that would have broken a value-reading key — and must still produce the identical key.
func TestAlarmKeyTemplateRendersIdenticallyOnBothEdges(t *testing.T) {
	rule := alarmKeyRule("", `"overtemp-" + series`)

	raised := dispatchAlarm(t, rule, evtFor("pump-01"))

	resolved := evtFor("pump-01")
	resolved.Edge = runtime.EdgeResolved
	resolved.Value = nil
	cleared := dispatchAlarm(t, rule, resolved)

	if len(raised) != 1 || len(cleared) != 1 {
		t.Fatalf("want one request per edge, got %d raised and %d cleared", len(raised), len(cleared))
	}
	if cleared[0].Edge != runtime.EdgeResolved {
		t.Fatalf("falling edge dispatched as %q", cleared[0].Edge)
	}
	if raised[0].AlarmKey != cleared[0].AlarmKey {
		t.Fatalf("the clear names %q but the raise named %q — the alarm would strand ACTIVE",
			cleared[0].AlarmKey, raised[0].AlarmKey)
	}
}

// A rendered key that is not a valid alarm key must be REFUSED, not filed. The template passes
// publish (it is well-typed and cheap); only a particular device makes it render badly — here a
// series long enough to push the key past the 128-character storage column. Nothing may reach the
// sink: an over-long key is a write error that redelivers to poison, and a truncated or substituted
// one would file the alarm under a name the author never wrote.
func TestOverlongRenderedAlarmKeyIsRefused(t *testing.T) {
	rule := alarmKeyRule("", `"overtemp-" + series`)
	long := strings.Repeat("d", 130) // valid token characters, but past MaxTokenLen once prefixed

	if got := dispatchAlarm(t, rule, evtFor(long)); len(got) != 0 {
		t.Fatalf("an unstorable rendered key was dispatched anyway: %q", got[0].AlarmKey)
	}
}

// The same fail-closed refusal on the FALLING edge, and for the same reason it is safe there: the
// rising edge failed identically (the template reads only the series), so no contribution exists to
// be stranded. Asserting it stops a future "always dispatch the clear" special case from being added
// without noticing it would now dispatch an EMPTY key, which the raise-alarm consumer drops as
// malformed anyway.
func TestRefusedAlarmKeyIsNotDispatchedOnTheFallingEdgeEither(t *testing.T) {
	rule := alarmKeyRule("", `"overtemp-" + series`)
	resolved := evtFor(strings.Repeat("d", 130))
	resolved.Edge = runtime.EdgeResolved
	resolved.Value = nil

	if got := dispatchAlarm(t, rule, resolved); len(got) != 0 {
		t.Fatalf("a clear was dispatched under an unusable key: %q", got[0].AlarmKey)
	}
}

// A rule declaring type raiseAlarm with no raiseAlarm payload must be DROPPED, not panicked on. It
// cannot come through the publish gate, but the gate's variant check is not re-run when a rule is
// decoded from the durable projection, so a hand-edited row reaches the dispatcher — and a panic
// there is not one bad alarm, it is the shared consumer loop crash-looping on every redelivery.
func TestMalformedRaiseAlarmActionIsDroppedNotPanicked(t *testing.T) {
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm}}} // declared type, absent payload

	if got := dispatchAlarm(t, rule, evtFor("pump-01")); len(got) != 0 {
		t.Fatalf("a malformed raiseAlarm produced %d alarm requests, want none", len(got))
	}
}

// A rendered key and a literal key must never collide in the action identity — including when the
// literal key is EMPTY, which is legal (the dispatcher then defaults it to the rule's stable key).
// Without the discriminator, `raiseAlarm` with no key and `raiseAlarm` with template "" would be one
// identity, and the publish gate would reject a legitimate pair as duplicates.
func TestActionContentKeySeparatesRenderedFromLiteralAlarmKeys(t *testing.T) {
	literal := rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "k"}}
	rendered := rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKeyTemplate: `"k"`}}
	empty := rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{}}

	keys := map[string]string{
		"literal":  actionContentKey(literal),
		"rendered": actionContentKey(rendered),
		"empty":    actionContentKey(empty),
	}
	if keys["literal"] == keys["rendered"] || keys["empty"] == keys["rendered"] {
		t.Fatalf("content keys collide: %#v", keys)
	}
	// The literal case must be byte-for-byte what it was before templates existed.
	if got, want := keys["literal"], "raiseAlarm\x00k"; got != want {
		t.Fatalf("literal content key = %q, want the pre-template shape %q", got, want)
	}
	// And the gate's duplicate identity must separate the same pair, or the two would drift.
	if rules.ActionDedupKey(literal) == rules.ActionDedupKey(rendered) ||
		rules.ActionDedupKey(empty) == rules.ActionDedupKey(rendered) {
		t.Fatal("ActionDedupKey does not separate a rendered key from a literal one")
	}
}
