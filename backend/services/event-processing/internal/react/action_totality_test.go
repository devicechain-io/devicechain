// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"reflect"
	"strings"
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

			// 🔴 The counterweight, and without it the whole test is satisfiable by an action type
			// that reaches no branch at all. Populate the variant and require a key that is NOT the
			// bare type string: that can only happen if the switch actually has a case for this
			// type, which can only happen if the json tag the derivation read really is the
			// ActionType constant. A fifth variant whose tag and constant disagree fails HERE,
			// rather than passing the nil case vacuously while its branch dereferences unguarded.
			if live := actionContentKey(populatedAction(t, at)); live == string(at) {
				t.Errorf("a POPULATED %s action still produced the degenerate key %q — actionContentKey "+
					"has no case for this type, so the nil check above proved nothing about it. Either "+
					"the branch is missing, or the json tag this type was derived from does not match "+
					"its ActionType constant.", at, live)
			}
		})
	}
}

// declaredActionTypes reads the action-type discriminants off rules.Action's variant fields. Each
// variant is a pointer field whose json tag is byte-identical to its ActionType constant
// (`raiseAlarm`, `sendCommand`, `httpCall`, `publish`), which is what makes the derivation sound.
//
// 🔴 THAT SOUNDNESS IS NOT SELF-CHECKING, AND AN EARLIER VERSION OF THIS COMMENT CLAIMED IT WAS.
// It said a tag that stopped matching its constant would fail the caller's equality assertion. It
// would not: a mismatched tag yields an ActionType no `case` matches, so actionContentKey falls to
// `default` and returns the bare type string — exactly what that assertion wants. A fifth variant
// tagged `fooX` while its constant is `foo` would therefore PASS while its branch dereferenced
// bare, which is the whole defect this file exists to prevent.
//
// So the caller carries a second assertion that closes it: for each derived type it also builds an
// action with that variant POPULATED and requires the key to differ from the bare type string.
// That is only true when the switch has a live branch for the type, which is only true when the tag
// really is the constant. The derivation is checked against the code rather than trusted.
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

// populatedAction builds an Action of the given type with its matching variant pointer allocated,
// found by the same json-tag correspondence declaredActionTypes uses. Allocating through reflection
// rather than a hand-written switch keeps this helper from being the place a fifth action type gets
// forgotten — the thing it is here to detect.
func populatedAction(t *testing.T, at rules.ActionType) rules.Action {
	t.Helper()

	a := rules.Action{Type: at}
	v := reflect.ValueOf(&a).Elem()
	rt := v.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Ptr {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if rules.ActionType(tag) != at {
			continue
		}
		v.Field(i).Set(reflect.New(f.Type.Elem()))
		return a
	}
	t.Fatalf("no variant field on rules.Action carries the json tag %q", at)
	return a
}
