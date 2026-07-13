// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// fakeConnectorSink records connector-dispatch requests and can fail.
type fakeConnectorSink struct {
	got  []ConnectorRequest
	fail bool
}

func (s *fakeConnectorSink) Dispatch(_ context.Context, req ConnectorRequest) error {
	if s.fail {
		return errors.New("connector publish failed")
	}
	s.got = append(s.got, req)
	return nil
}

func httpCallRule(a rules.HTTPCallAction) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionHTTPCall, HTTPCall: &a}}}
}

func publishRule(a rules.PublishAction) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionPublish, Publish: &a}}}
}

// TestDispatchHTTPCall proves an httpCall action on a rising edge reaches the connector sink with the
// event context, the resolved action, a rendered payload, and a deterministic token; and is Done.
func TestDispatchHTTPCall(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	rule := httpCallRule(rules.HTTPCallAction{
		URL:          "https://hooks.example/x",
		Headers:      map[string]string{"X-Env": "prod"},
		BodyTemplate: `'{"series":"' + series + '"}'`,
		SecretRef:    "httpcall/acme/auth",
	})
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, nil, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 1 {
		t.Fatalf("want 1 connector dispatch, got %d", len(sink.got))
	}
	r := sink.got[0]
	if r.Tenant != "acme" || r.DeviceToken != "device-1" || r.RuleID != "acme/p@1/r1" {
		t.Fatalf("event context wrong: %+v", r)
	}
	if r.Edge != runtime.EdgeRaised {
		t.Fatalf("rising edge should carry raised, got %q", r.Edge)
	}
	if r.Action.Type != rules.ActionHTTPCall || r.Action.HTTPCall == nil {
		t.Fatalf("resolved action not carried: %+v", r.Action)
	}
	if r.Payload != `{"series":"device-1"}` {
		t.Fatalf("payload not rendered against the event: %q", r.Payload)
	}
	if r.Token == "" {
		t.Fatalf("connector dispatch must carry a deterministic idempotency token")
	}
	if m.dispatched["httpCall"] != 1 {
		t.Fatalf("httpCall dispatched metric not recorded: %+v", m.dispatched)
	}
}

// TestDispatchPublishRendersPayload proves a publish action renders its payload template and carries
// the connectorRef through to the sink.
func TestDispatchPublishRendersPayload(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	rule := publishRule(rules.PublishAction{ConnectorRef: "kafka-main", PayloadTemplate: `'v=' + string(value)`})
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, nil, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(sink.got))
	}
	r := sink.got[0]
	if r.Action.Publish == nil || r.Action.Publish.ConnectorRef != "kafka-main" {
		t.Fatalf("connectorRef not carried: %+v", r.Action)
	}
	// evt() carries no value → value binds 0.0.
	if r.Payload != "v=0" {
		t.Fatalf("publish payload = %q", r.Payload)
	}
	if m.dispatched["publish"] != 1 {
		t.Fatalf("publish dispatched metric not recorded: %+v", m.dispatched)
	}
}

// TestDispatchHTTPCallNoBody proves an httpCall with no body template dispatches with an empty payload
// (no build, no error) rather than being skipped.
func TestDispatchHTTPCallNoBody(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true}, nil, nil, sink, nil, m)
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 1 || sink.got[0].Payload != "" {
		t.Fatalf("empty-body httpCall should dispatch an empty payload, got %+v", sink.got)
	}
}

// TestDispatchConnectorResolvedSkipped proves a connector action is a ONE-SHOT rising-edge side effect:
// a Resolved (falling) edge dispatches nothing (like sendCommand, no falling-edge twin).
func TestDispatchConnectorResolvedSkipped(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true}, nil, nil, sink, nil, m)
	ev := evt()
	ev.Edge = runtime.EdgeResolved
	if out := d.Dispatch(context.Background(), ev); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 0 {
		t.Fatalf("a resolved edge must not dispatch a connector action, got %d", len(sink.got))
	}
	if m.dispatched["httpCall"] != 0 {
		t.Fatalf("no connector dispatch should be counted on a resolved edge: %+v", m.dispatched)
	}
}

// TestDispatchConnectorNotEnabled proves a connector action with a NIL connector sink is
// recognized-but-inert: no dispatch, counted not-enabled, and the event is still Done.
func TestDispatchConnectorNotEnabled(t *testing.T) {
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true}, nil, nil, nil, nil, m)
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if m.notEnabled["httpCall"] != 1 {
		t.Fatalf("disabled connector action should be counted not-enabled: %+v", m.notEnabled)
	}
}

// TestDispatchConnectorRetryOnSinkFailure proves a connector publish failure is a Retry (the event
// redelivers), leaving nothing acked — the idempotency token makes the re-run safe.
func TestDispatchConnectorRetryOnSinkFailure(t *testing.T) {
	sink := &fakeConnectorSink{fail: true}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true}, nil, nil, sink, nil, m)
	if out := d.Dispatch(context.Background(), evt()); out != Retry {
		t.Fatalf("want Retry on sink failure, got %v", out)
	}
	if m.dispatched["httpCall"] != 0 {
		t.Fatalf("a failed dispatch must not be counted as dispatched: %+v", m.dispatched)
	}
}

// TestDispatchConnectorGuardBlocks proves a per-action branch guard that evaluates false skips the
// connector dispatch (a routine, deterministic non-effect) without failing the event.
func TestDispatchConnectorGuardBlocks(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	rule := httpCallRule(rules.HTTPCallAction{URL: "https://x/y"})
	rule.Actions[0].Guard = "hasValue" // evt() carries no value → guard false
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, nil, m)
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 0 {
		t.Fatalf("a false guard must skip the connector dispatch, got %d", len(sink.got))
	}
}

// TestDispatchConnectorMalformedVariantDropped proves a forged/hand-edited resolved rule whose
// declared type has no matching payload variant is DROPPED fail-closed (Done, nothing dispatched)
// rather than nil-panicking the consumer loop — the publish gate's variant check is not re-run when a
// rule is decoded from the durable projection, so the dispatcher must not trust the resolved shape.
func TestDispatchConnectorMalformedVariantDropped(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	// type=httpCall but HTTPCall is nil (and no publish either).
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionHTTPCall}}}
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, nil, m)
	// Must not panic; must drop.
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done (malformed action dropped), got %v", out)
	}
	if len(sink.got) != 0 {
		t.Fatalf("a malformed connector action must not dispatch, got %d", len(sink.got))
	}
	// And the token helper is itself total (no panic) for a nil variant.
	if actionContentKey(rule.Actions[0]) == "" {
		t.Fatal("actionContentKey must return a non-empty key for a nil-variant action")
	}
}

// fakeGate is a source-side cost-gate that admits or sheds deterministically and records the tenants
// it was charged for.
type fakeGate struct {
	admit   bool
	charged []string
}

func (g *fakeGate) Allow(tenant string) bool {
	g.charged = append(g.charged, tenant)
	return g.admit
}

// TestDispatchConnectorSourceGateSheds proves the SOURCE-side egress cost-gate (ADR-060 SD-3) drops a
// connector action when the tenant is over quota: nothing reaches the sink, the event is still Done
// (ack-progress, NOT a Retry that would loop under overload), and the shed is counted (not counted as
// a dispatch).
func TestDispatchConnectorSourceGateSheds(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	gate := &fakeGate{admit: false}
	rule := httpCallRule(rules.HTTPCallAction{URL: "https://x/y", BodyTemplate: `'{"s":"' + series + '"}'`})
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, gate, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("a source-shed connector action must be Done (ack-progress), got %v", out)
	}
	if len(sink.got) != 0 {
		t.Fatalf("an over-quota connector action must not reach the sink, got %d", len(sink.got))
	}
	if m.connectorShed["httpCall"] != 1 {
		t.Fatalf("shed metric not recorded: %+v", m.connectorShed)
	}
	if m.dispatched["httpCall"] != 0 {
		t.Fatalf("a shed action must not be counted as dispatched: %+v", m.dispatched)
	}
	if len(gate.charged) != 1 || gate.charged[0] != "acme" {
		t.Fatalf("the gate must be charged for the event tenant, got %+v", gate.charged)
	}
}

// TestDispatchConnectorSourceGateAdmits proves that when the gate admits, the connector action
// dispatches normally (the gate is charged, then the action reaches the sink and is counted).
func TestDispatchConnectorSourceGateAdmits(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	gate := &fakeGate{admit: true}
	d := NewDispatcher(fakeResolver{rule: httpCallRule(rules.HTTPCallAction{URL: "https://x/y"}), found: true}, nil, nil, sink, gate, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.got) != 1 {
		t.Fatalf("an admitted connector action must reach the sink, got %d", len(sink.got))
	}
	if m.connectorShed["httpCall"] != 0 {
		t.Fatalf("an admitted action must not be counted as shed: %+v", m.connectorShed)
	}
	if m.dispatched["httpCall"] != 1 {
		t.Fatalf("dispatched metric not recorded: %+v", m.dispatched)
	}
	if len(gate.charged) != 1 {
		t.Fatalf("the gate must be charged exactly once, got %+v", gate.charged)
	}
}

// TestDispatchConnectorGuardedOutNotCharged proves the gate is charged only for an action that would
// actually emit: a branch-guarded-out connector action is a non-effect and must NOT consume a token
// (else a guarded-false action would drain the tenant's egress budget for a dispatch it never makes).
func TestDispatchConnectorGuardedOutNotCharged(t *testing.T) {
	sink := &fakeConnectorSink{}
	m := newFakeMetrics()
	gate := &fakeGate{admit: true}
	rule := httpCallRule(rules.HTTPCallAction{URL: "https://x/y"})
	rule.Actions[0].Guard = "hasValue" // evt() carries no value → guard false
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, nil, sink, gate, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(gate.charged) != 0 {
		t.Fatalf("a guarded-out connector action must not charge the egress gate, got %+v", gate.charged)
	}
	if len(sink.got) != 0 {
		t.Fatalf("a guarded-out action must not dispatch, got %d", len(sink.got))
	}
}

// TestConnectorContentKeyNormalization proves the durable idempotency token applies the SAME
// normalization as the authoring gate (rules.ActionDedupKey): semantically identical actions (empty
// vs "POST" method, header-name case) map to ONE token, so a redelivery/replay after a
// semantics-preserving republish dedups downstream rather than double-executing.
func TestConnectorContentKeyNormalization(t *testing.T) {
	ev := evt()
	implicit := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"x-env": "a"}}}
	explicit := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Method: "POST", Headers: map[string]string{"X-Env": "a"}}}
	if idempotencyToken(ev, implicit) != idempotencyToken(ev, explicit) {
		t.Fatal("empty-method/lower-header and POST/canonical-header must share one idempotency token")
	}
	// A different URL is a distinct dispatch → distinct token.
	other := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/z"}}
	if idempotencyToken(ev, implicit) == idempotencyToken(ev, other) {
		t.Fatal("a different URL must yield a different token")
	}
	// A publish action's token differs by connectorRef.
	p1 := rules.Action{Type: rules.ActionPublish, Publish: &rules.PublishAction{ConnectorRef: "a"}}
	p2 := rules.Action{Type: rules.ActionPublish, Publish: &rules.PublishAction{ConnectorRef: "b"}}
	if idempotencyToken(ev, p1) == idempotencyToken(ev, p2) {
		t.Fatal("publish token must differ per connectorRef")
	}
}

// TestConnectorContentKeyMirrorsGate proves the react content key induces the SAME equivalence as the
// authoring gate's dedup key: two actions collide in the token iff the gate would call them duplicates.
// This is the load-bearing lockstep — they share rules.MethodOrPost / rules.HeaderKey, so they cannot
// drift. We assert equality/inequality of the react key tracks equality/inequality of the gate key.
func TestConnectorContentKeyMirrorsGate(t *testing.T) {
	pairs := []struct {
		name string
		a, b rules.Action
	}{
		{"method+header normalization",
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"x-env": "a"}}},
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Method: "POST", Headers: map[string]string{"X-Env": "a"}}}},
		{"distinct url",
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y"}},
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/z"}}},
		{"distinct header value",
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"X-Env": "a"}}},
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"X-Env": "b"}}}},
		{"distinct guard",
			rules.Action{Type: rules.ActionHTTPCall, Guard: "hasValue", HTTPCall: &rules.HTTPCallAction{URL: "https://x/y"}},
			rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x/y"}}},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			gateEqual := rules.ActionDedupKey(p.a) == rules.ActionDedupKey(p.b)
			keyEqual := actionContentKey(p.a) == actionContentKey(p.b)
			if gateEqual != keyEqual {
				t.Fatalf("react content-key equivalence (%v) diverged from the gate dedup-key equivalence (%v)", keyEqual, gateEqual)
			}
		})
	}
}
