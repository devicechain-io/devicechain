// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// TestActionContentKeyIsTotal pins that actionContentKey never panics on an action whose variant
// pointer is nil, for EVERY action type the schema declares.
//
// Why this exists. A rule reaching the dispatcher has normally been through the publish gate's
// populatedVariants check, so the variant matching Type is non-nil — which is precisely the reason
// the bare dereferences looked safe and survived review. But that check is not re-run when a rule is
// decoded from the durable projection, so a hand-edited or forged row reaches this code with a nil
// variant, and a panic in the shared consumer loop is not a dropped message: it crash-loops on every
// redelivery. Two of the four branches guarded, two did not, and the guarded ones carried a comment
// claiming the whole helper could never nil-panic.
//
// 🔑 THE TYPE LIST IS DERIVED, NOT WRITTEN DOWN. It is read from rules.Action's own struct tags, so
// a fifth action type is covered the moment its field is declared rather than when someone remembers
// this file. A hand-maintained list would keep passing while the new branch dereferenced bare — the
// exact failure this test is here to prevent, one action type later.
func TestActionContentKeyIsTotal(t *testing.T) {
	types := declaredActionTypes(t)
	if len(types) < 4 {
		// A derivation that finds nothing reads exactly like a clean pass. The four known today are
		// raiseAlarm, sendCommand, httpCall and publish; fewer means the derivation broke, not that
		// the schema shrank.
		t.Fatalf("derived only %d action types (%v) — the derivation is broken, not the schema", len(types), types)
	}

	for _, at := range types {
		t.Run(string(at), func(t *testing.T) {
			// Every variant pointer left nil, which is the malformed shape.
			a := rules.Action{Type: at}

			var got string
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("actionContentKey panicked on a nil %s variant: %v", at, r)
					}
				}()
				got = actionContentKey(a)
			}()

			// The degenerate token is the bare type string. It is never used downstream — the action
			// is dropped in dispatchAction before a token is minted — so the only property that
			// matters is that producing it is safe and deterministic.
			if got != string(at) {
				t.Errorf("nil %s variant: got token %q, want the bare type string %q", at, got, string(at))
			}

			// A guard must not change that: the guard segment is appended by the branches, and a
			// degenerate return happens before it.
			if guarded := actionContentKey(rules.Action{Type: at, Guard: "value > 1"}); guarded != string(at) {
				t.Errorf("nil %s variant with a guard: got %q, want %q", at, guarded, string(at))
			}
		})
	}
}

// declaredActionTypes reads the action-type discriminants off rules.Action's variant fields. Each
// variant is a pointer field whose json tag is byte-identical to its ActionType constant
// (`raiseAlarm`, `sendCommand`, `httpCall`, `publish`), which is what makes the derivation sound —
// and if that ever stops being true, the equality assertion in the caller fails rather than the
// derivation silently returning a wrong set.
func declaredActionTypes(t *testing.T) []rules.ActionType {
	t.Helper()

	rt := reflect.TypeOf(rules.Action{})
	var out []rules.ActionType
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Ptr {
			continue // Type and Guard are scalars; only the variants are pointers
		}
		tag := f.Tag.Get("json")
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				tag = tag[:j]
				break
			}
		}
		if tag == "" || tag == "-" {
			t.Fatalf("variant field %s carries no usable json tag, so the action type cannot be derived", f.Name)
		}
		out = append(out, rules.ActionType(tag))
	}
	return out
}

// TestMalformedSendCommandActionIsDroppedNotPanicked is the sibling of
// TestMalformedRaiseAlarmActionIsDroppedNotPanicked, and it exists because that one shipped alone.
//
// A rule declaring type sendCommand with no sendCommand payload cannot come through the publish
// gate — but the gate's variant check is not re-run when a rule is decoded from the durable
// projection, so a hand-edited or forged row reaches dispatchAction and hits the bare dereference in
// the CommandRequest literal. A panic there is not one lost command: there is no recover on the
// shared consumer loop, so it crash-loops on every redelivery of that event.
//
// 🔑 The raiseAlarm and connector branches were both guarded before this; sendCommand was the one
// left. Guarding a variant without guarding its siblings leaves the class open, which is the whole
// reason this test names the class rather than the instance.
func TestMalformedSendCommandActionIsDroppedNotPanicked(t *testing.T) {
	sink := &fakeSink{}
	m := newFakeMetrics()
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionSendCommand}}} // declared type, absent payload
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, sink, nil, nil, nil, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done (malformed action dropped), got %v", out)
	}
	if len(sink.sent) != 0 {
		t.Fatalf("a malformed sendCommand must not dispatch, got %d", len(sink.sent))
	}
	if sink.calls != 0 {
		t.Fatalf("the command sink was called %d times for a malformed action", sink.calls)
	}
}
