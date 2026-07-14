// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

const profile = "thermostat"

// cfg marshals a node config map to the opaque JSON the wire form carries.
func cfg(m map[string]interface{}) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// src is the standard profile-scoped source node feeding a canvas.
func src(id string) Node {
	return Node{ID: id, Type: NodeSource, Config: cfg(map[string]interface{}{
		"scope": map[string]interface{}{"kind": "profile", "profileToken": profile},
	})}
}

// compileOne compiles a canvas expected to yield exactly one rule and returns it.
func compileOne(t *testing.T, def CanvasDefinition) LoweredRule {
	t.Helper()
	res, err := Compile(def, profile, rules.DefaultLimits())
	if err != nil {
		t.Fatalf("Compile: unexpected error: %v", err)
	}
	if len(res.Rules) != 1 {
		t.Fatalf("Compile: got %d rules, want 1", len(res.Rules))
	}
	return res.Rules[0]
}

func f64(v float64) *float64 { return &v }

// canvas assembles a definition from nodes + edges at the current schema version.
func canvas(nodes []Node, edges ...Edge) CanvasDefinition {
	return CanvasDefinition{SchemaVersion: SchemaVersion, Nodes: nodes, Edges: edges}
}

// TestByteIdentity is the §3.2 done-when: each canvas graph lowers to a rules.Rule
// byte-identical (after canonical marshalling) to the equivalent hand-written form rule. The
// stored definition carries no transient id, exactly as the form builder emits it, so the two
// decode to the same struct and re-marshal to the same bytes.
func TestByteIdentity(t *testing.T) {
	cases := []struct {
		name   string
		canvas CanvasDefinition
		want   rules.Rule
	}{
		{
			name: "threshold + raiseAlarm",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "hot", "severity": "critical",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm", "alarmKey": "overheat"})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			want: rules.Rule{
				Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
				When:    rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
				Actions: []rules.Action{{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}}},
			},
		},
		{
			name: "threshold dynamic attribute + sendCommand",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "over-limit",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "attribute", "attribute": "tempLimit"}},
					})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "sendCommand", "command": "cool", "payload": `{"level":2}`})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			want: rules.Rule{
				Name: "over-limit", Type: rules.TypeThreshold,
				When:    rules.Condition{Metric: "tempC", Op: rules.OpGt, ThresholdAttr: "tempLimit"},
				Actions: []rules.Action{{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "cool", Payload: `{"level":2}`}}},
			},
		},
		{
			name: "threshold + httpCall webhook (ADR-060)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "hot", "severity": "critical",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{
						"action": "httpCall", "url": "https://example.com/hook", "bodyTemplate": `"overheat"`,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			want: rules.Rule{
				Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
				When:    rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
				Actions: []rules.Action{{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://example.com/hook", BodyTemplate: `"overheat"`}}},
			},
		},
		{
			name: "httpCall with headers + secret + timeout (ADR-060, map-sort byte-identity)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "hot", "severity": "warning",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{
						"action": "httpCall", "url": "https://example.com/hook",
						"headers":   map[string]interface{}{"X-Env": "prod", "Content-Type": "application/json"},
						"secretRef": "hook-token", "timeoutMs": 5000,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			want: rules.Rule{
				Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityWarning,
				When: rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
				Actions: []rules.Action{{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{
					URL: "https://example.com/hook", Headers: map[string]string{"X-Env": "prod", "Content-Type": "application/json"},
					SecretRef: "hook-token", TimeoutMs: 5000,
				}}},
			},
		},
		{
			name: "threshold + publish to connector (ADR-060)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "hot", "severity": "major",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{
						"action": "publish", "connectorRef": "pager", "payloadTemplate": `"overheat"`,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			want: rules.Rule{
				Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
				When:    rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
				Actions: []rules.Action{{Type: rules.ActionPublish, Publish: &rules.PublishAction{ConnectorRef: "pager", PayloadTemplate: `"overheat"`}}},
			},
		},
		{
			name: "duration, no action (emit-derived-event)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeDuration, Config: cfg(map[string]interface{}{
						"name": "sustained-hot", "severity": "warning", "holdMs": 600000,
						"when": map[string]interface{}{"metric": "tempC", "op": "ge", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "sustained-hot", Type: rules.TypeDuration, Severity: rules.SeverityWarning,
				When: rules.Condition{Metric: "tempC", Op: rules.OpGe, Threshold: f64(30)},
				Hold: rules.Duration(600000 * 1e6),
			},
		},
		{
			name: "absence (no when leaf)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeAbsence, Config: cfg(map[string]interface{}{
						"name": "went-silent", "severity": "major", "timeoutMs": 300000,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "went-silent", Type: rules.TypeAbsence, Severity: rules.SeverityMajor,
				Ttl: rules.Duration(300000 * 1e6),
			},
		},
		{
			name: "aggregate tumbling avg",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeAggregate, Config: cfg(map[string]interface{}{
						"name": "avg-hot", "agg": "avg", "windowMode": "tumbling", "metric": "tempC",
						"windowMs": 60000, "op": "gt", "threshold": 25,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "avg-hot", Type: rules.TypeAggregate, Agg: rules.AggAvg, Mode: rules.ModeTumbling,
				Metric: "tempC", Window: rules.Duration(60000 * 1e6), Op: rules.OpGt, Threshold: f64(25),
			},
		},
		{
			name: "deltaRate per-second (no window)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeDeltaRate, Config: cfg(map[string]interface{}{
						"name": "rising-fast", "metric": "tempC", "rate": true, "op": "gt", "threshold": 5,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "rising-fast", Type: rules.TypeDeltaRate, Metric: "tempC", Rate: true,
				Op: rules.OpGt, Threshold: f64(5),
			},
		},
		{
			name: "repeating count-window",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeRepeating, Config: cfg(map[string]interface{}{
						"name": "flapping", "count": 3, "windowMs": 60000,
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "flapping", Type: rules.TypeRepeating,
				When:  rules.Condition{Metric: "tempC", Op: rules.OpGt, Threshold: f64(30)},
				Count: 3, Window: rules.Duration(60000 * 1e6),
			},
		},
		{
			name: "correlation (no action allowed)",
			canvas: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeCorrelation, Config: cfg(map[string]interface{}{
						"name": "zone-outage", "anchorType": "zone", "count": 3, "windowMs": 60000,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			want: rules.Rule{
				Name: "zone-outage", Type: rules.TypeCorrelation, AnchorType: "zone",
				Count: 3, Window: rules.Duration(60000 * 1e6),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compileOne(t, tc.canvas)

			// The lowered rule struct equals the hand-written form rule.
			if !reflect.DeepEqual(got.Rule, tc.want) {
				t.Fatalf("lowered rule mismatch\n got: %+v\nwant: %+v", got.Rule, tc.want)
			}
			// The stored definition round-trips through the DETECT decoder to the same struct.
			decoded, err := rules.Decode([]byte(got.Definition))
			if err != nil {
				t.Fatalf("rules.Decode(definition): %v", err)
			}
			if !reflect.DeepEqual(decoded, tc.want) {
				t.Fatalf("decoded definition mismatch\n got: %+v\nwant: %+v", decoded, tc.want)
			}
			// The canonical bytes are identical to the marshalled form rule (§3.2 byte-identity).
			wantBytes, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if got.Definition != string(wantBytes) {
				t.Fatalf("definition bytes mismatch\n got: %s\nwant: %s", got.Definition, wantBytes)
			}
		})
	}
}

// TestMultiCondition: two conditions fanning off one source lower to two independent rules,
// each with its own action (§3.3 allowed / §9a done-when).
func TestMultiCondition(t *testing.T) {
	def := canvas(
		[]Node{
			src("s"),
			{ID: "c1", Type: NodeThreshold, Config: cfg(map[string]interface{}{
				"name": "hot", "severity": "critical",
				"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
			})},
			{ID: "c2", Type: NodeThreshold, Config: cfg(map[string]interface{}{
				"name": "cold", "severity": "warning",
				"when": map[string]interface{}{"metric": "tempC", "op": "lt", "threshold": map[string]interface{}{"kind": "literal", "value": 0}},
			})},
			{ID: "a1", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm"})},
			{ID: "a2", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm"})},
		},
		Edge{From: "s:out", To: "c1:in"},
		Edge{From: "s:out", To: "c2:in"},
		Edge{From: "c1:signal", To: "a1:in"},
		Edge{From: "c2:signal", To: "a2:in"},
	)
	res, err := Compile(def, profile, rules.DefaultLimits())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(res.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(res.Rules))
	}
	byNode := map[string]LoweredRule{}
	for _, r := range res.Rules {
		byNode[r.NodeID] = r
	}
	if byNode["c1"].Rule.Name != "hot" || byNode["c2"].Rule.Name != "cold" {
		t.Fatalf("rules not keyed to their condition nodes: %+v", byNode)
	}
	if len(byNode["c1"].Rule.Actions) != 1 || len(byNode["c2"].Rule.Actions) != 1 {
		t.Fatalf("each rule should carry its own single action")
	}
}

// TestRejects covers the §3.4 fail-closed constructs: each must reject with a surfaced,
// node-anchored message rather than lower to a rule.
func TestRejects(t *testing.T) {
	th := func(id, name string) Node {
		return Node{ID: id, Type: NodeThreshold, Config: cfg(map[string]interface{}{
			"name": name,
			"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
		})}
	}
	cases := []struct {
		name       string
		def        CanvasDefinition
		wantNodeID string
	}{
		{
			name:       "no condition node",
			def:        canvas([]Node{src("s")}),
			wantNodeID: "",
		},
		{
			name: "unknown node type",
			def: canvas([]Node{
				src("s"),
				{ID: "x", Type: NodeType("frobnicate"), Config: cfg(map[string]interface{}{})},
			}),
			wantNodeID: "x",
		},
		{
			name: "cross-typed edge (signal into a stream port)",
			def: canvas(
				[]Node{src("s"), th("c1", "a"), th("c2", "b")},
				Edge{From: "s:out", To: "c1:in"},
				Edge{From: "c1:signal", To: "c2:in"}, // signal → stream: rejected
			),
			wantNodeID: "c2",
		},
		{
			name: "two sources feed one condition",
			def: canvas(
				[]Node{src("s1"), src("s2"), th("c", "a")},
				Edge{From: "s1:out", To: "c:in"},
				Edge{From: "s2:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "condition with no source",
			def: canvas(
				[]Node{th("c", "a")},
			),
			wantNodeID: "c",
		},
		{
			name: "action with no detection feeding it",
			def: canvas(
				[]Node{src("s"), th("c", "a"), {ID: "act", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm"})}},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "act",
		},
		{
			name: "cross-window join (action fed by two conditions)",
			def: canvas(
				[]Node{src("s"), th("c1", "a"), th("c2", "b"), {ID: "act", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm"})}},
				Edge{From: "s:out", To: "c1:in"},
				Edge{From: "s:out", To: "c2:in"},
				Edge{From: "c1:signal", To: "act:in"},
				Edge{From: "c2:signal", To: "act:in"},
			),
			wantNodeID: "act",
		},
		{
			name: "source scope mismatch",
			def: canvas(
				[]Node{
					{ID: "s", Type: NodeSource, Config: cfg(map[string]interface{}{"scope": map[string]interface{}{"kind": "profile", "profileToken": "other"}})},
					th("c", "a"),
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "s",
		},
		{
			name: "derived-subject source (deferred)",
			def: canvas(
				[]Node{
					{ID: "s", Type: NodeSource, Config: cfg(map[string]interface{}{"scope": map[string]interface{}{"kind": "derivedSubject"}})},
					th("c", "a"),
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "s",
		},
		{
			name: "unknown config field (fail closed)",
			def: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "a", "bogus": 1,
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "duplicate node id",
			def: canvas(
				[]Node{src("s"), th("c", "a"), th("c", "b")},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "malformed edge endpoint",
			def: canvas(
				[]Node{src("s"), th("c", "a")},
				Edge{From: "s", To: "c:in"},
			),
			wantNodeID: "",
		},
		{
			name: "action carries a foreign variant field (httpCall with publish's payloadTemplate)",
			def: canvas(
				[]Node{
					src("s"), th("c", "a"),
					{ID: "act", Type: NodeAction, Config: cfg(map[string]interface{}{
						"action": "httpCall", "url": "https://example.com/hook", "payloadTemplate": `"x"`,
					})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "act:in"},
			),
			wantNodeID: "act",
		},
		{
			name: "correlation cannot carry actions",
			def: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeCorrelation, Config: cfg(map[string]interface{}{"name": "z", "anchorType": "zone", "count": 3, "windowMs": 60000})},
					{ID: "a", Type: NodeAction, Config: cfg(map[string]interface{}{"action": "raiseAlarm"})},
				},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "a:in"},
			),
			wantNodeID: "c", // rejected by rules.validateReact, anchored to the condition
		},
		{
			name: "duration overflow (wraps to a small positive)",
			def: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeDuration, Config: cfg(map[string]interface{}{
						"name": "x", "holdMs": int64(1) << 62, // > maxDurationMs; would wrap without the guard
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "negative timeout rejected before the compiler sees it",
			def: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeAbsence, Config: cfg(map[string]interface{}{"name": "x", "timeoutMs": -5})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "incoherent bound (literal + attribute both set)",
			def: canvas(
				[]Node{
					src("s"),
					{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
						"name": "x",
						"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30, "attribute": "tempLimit"}},
					})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "c",
		},
		{
			name: "poisoned dangling source (wrong profile, unwired) still rejected",
			def: canvas(
				[]Node{
					src("s"), // the wired, good source
					th("c", "a"),
					{ID: "bad", Type: NodeSource, Config: cfg(map[string]interface{}{"scope": map[string]interface{}{"kind": "profile", "profileToken": "other"}})},
				},
				Edge{From: "s:out", To: "c:in"},
			),
			wantNodeID: "bad",
		},
		{
			name: "edge to a phantom node is graph-level",
			def: canvas(
				[]Node{src("s"), th("c", "a")},
				Edge{From: "s:out", To: "c:in"},
				Edge{From: "c:signal", To: "ghost:in"},
			),
			wantNodeID: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.def, profile, rules.DefaultLimits())
			if err == nil {
				t.Fatal("expected a compile error, got nil")
			}
			var ce *CompileError
			if !errors.As(err, &ce) {
				t.Fatalf("expected *CompileError, got %T: %v", err, err)
			}
			if len(ce.Diagnostics) == 0 {
				t.Fatal("CompileError carried no diagnostics")
			}
			if ce.Diagnostics[0].NodeID != tc.wantNodeID {
				t.Fatalf("diagnostic node id = %q, want %q (msg: %s)", ce.Diagnostics[0].NodeID, tc.wantNodeID, ce.Diagnostics[0].Message)
			}
			if ce.Diagnostics[0].Message == "" {
				t.Fatal("diagnostic carried no message")
			}
		})
	}
}

// TestDetectCycle exercises the DAG guard directly. In the 9a catalog a cycle is
// unconstructible through typed ports (stream flows only source→condition, signal only
// condition→action), so the detector is defensive; a hand-built typedEdge cycle proves it
// still fires.
func TestDetectCycle(t *testing.T) {
	nodes := []Node{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	// a→b→c→a is a cycle.
	edges := []typedEdge{
		{fromNode: "a", toNode: "b", ptype: PortStream},
		{fromNode: "b", toNode: "c", ptype: PortStream},
		{fromNode: "c", toNode: "a", ptype: PortStream},
	}
	if err := detectCycle(nodes, edges); err == nil {
		t.Fatal("expected a cycle to be rejected")
	}
	// a→b→c (no back edge) is acyclic.
	acyclic := []typedEdge{
		{fromNode: "a", toNode: "b", ptype: PortStream},
		{fromNode: "b", toNode: "c", ptype: PortStream},
	}
	if err := detectCycle(nodes, acyclic); err != nil {
		t.Fatalf("acyclic graph rejected: %v", err)
	}
}

// TestSchemaVersionRejected: a forward/unknown schema version fails closed.
func TestSchemaVersionRejected(t *testing.T) {
	def := CanvasDefinition{SchemaVersion: 999, Nodes: []Node{src("s")}}
	_, err := Compile(def, profile, rules.DefaultLimits())
	if err == nil {
		t.Fatal("expected rejection of an unsupported schema version")
	}
}

// TestEstimatedCost: a compiled rule carries a positive predicate cost estimate, which the
// resolver surfaces (and the preview harness reuses).
func TestEstimatedCost(t *testing.T) {
	lr := compileOne(t, canvas(
		[]Node{
			src("s"),
			{ID: "c", Type: NodeThreshold, Config: cfg(map[string]interface{}{
				"name": "hot",
				"when": map[string]interface{}{"metric": "tempC", "op": "gt", "threshold": map[string]interface{}{"kind": "literal", "value": 30}},
			})},
		},
		Edge{From: "s:out", To: "c:in"},
	))
	if lr.Compiled == nil || lr.Compiled.Predicate == nil {
		t.Fatal("expected a compiled predicate")
	}
	if lr.Compiled.Predicate.CostMax() == 0 {
		t.Fatal("expected a positive predicate cost estimate")
	}
}

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		node    string
		port    string
		wantErr bool
	}{
		{"src1:out", "src1", "out", false},
		{"a:b:signal", "a:b", "signal", false}, // node id may contain a colon
		{"noport", "", "", true},
		{":out", "", "", true},
		{"node:", "", "", true},
	}
	for _, c := range cases {
		n, p, err := parseEndpoint(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseEndpoint(%q): expected error", c.in)
			}
			continue
		}
		if err != nil || n != c.node || p != c.port {
			t.Errorf("parseEndpoint(%q) = (%q,%q,%v), want (%q,%q,nil)", c.in, n, p, err, c.node, c.port)
		}
	}
}
