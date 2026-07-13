// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// A well-formed canvas compiles to one rule definition + a positive cost, no diagnostics.
func TestCompileCanvas_Ok(t *testing.T) {
	r := &SchemaResolver{}
	graph := `{"schemaVersion":1,
		"nodes":[
			{"id":"s","type":"source","config":{"scope":{"kind":"profile","profileToken":"thermostat"}}},
			{"id":"c","type":"threshold","config":{"name":"hot","severity":"critical","when":{"metric":"tempC","op":"gt","threshold":{"kind":"literal","value":30}}}},
			{"id":"a","type":"action","config":{"action":"raiseAlarm","alarmKey":"overheat"}}
		],
		"edges":[{"from":"s:out","to":"c:in"},{"from":"c:signal","to":"a:in"}]}`
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: graph, ProfileToken: "thermostat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("expected ok, got diagnostics: %+v", res.Diagnostics())
	}
	if res.Definition() == nil || *res.Definition() == "" {
		t.Fatal("expected a compiled definition")
	}
	if res.EstimatedCost() == nil || *res.EstimatedCost() == 0 {
		t.Fatal("expected a positive estimated cost")
	}
	if len(res.Diagnostics()) != 0 {
		t.Fatalf("expected no diagnostics, got %+v", res.Diagnostics())
	}
}

// A compile rejection surfaces node-anchored diagnostics, not a transport error.
func TestCompileCanvas_NodeAnchoredDiagnostic(t *testing.T) {
	r := &SchemaResolver{}
	// A threshold with no source feeding it — rejected, anchored to the condition node.
	graph := `{"schemaVersion":1,
		"nodes":[{"id":"c","type":"threshold","config":{"name":"hot","when":{"metric":"tempC","op":"gt","threshold":{"kind":"literal","value":30}}}}],
		"edges":[]}`
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: graph, ProfileToken: "thermostat"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected !ok")
	}
	if len(res.Diagnostics()) == 0 {
		t.Fatal("expected a diagnostic")
	}
	if id := res.Diagnostics()[0].NodeId(); id == nil || *id != "c" {
		t.Fatalf("expected the diagnostic anchored to node c, got %v", id)
	}
}

// A graph with two condition nodes is rejected: the GA profile-homed canvas authors one rule.
func TestCompileCanvas_MultiConditionRejected(t *testing.T) {
	r := &SchemaResolver{}
	graph := `{"schemaVersion":1,
		"nodes":[
			{"id":"s","type":"source","config":{"scope":{"kind":"profile","profileToken":"thermostat"}}},
			{"id":"c1","type":"threshold","config":{"name":"hot","when":{"metric":"tempC","op":"gt","threshold":{"kind":"literal","value":30}}}},
			{"id":"c2","type":"threshold","config":{"name":"cold","when":{"metric":"tempC","op":"lt","threshold":{"kind":"literal","value":0}}}}
		],
		"edges":[{"from":"s:out","to":"c1:in"},{"from":"s:out","to":"c2:in"}]}`
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: graph, ProfileToken: "thermostat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected !ok for a multi-condition canvas")
	}
	if res.Definition() != nil {
		t.Fatal("expected no definition on rejection")
	}
	// A graph-level rejection carries a null nodeId.
	if len(res.Diagnostics()) == 0 || res.Diagnostics()[0].NodeId() != nil {
		t.Fatalf("expected a graph-level diagnostic, got %+v", res.Diagnostics())
	}
}

// Undecodable graph JSON is a graph-level diagnostic, not a transport error.
func TestCompileCanvas_MalformedGraph(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.CompileCanvas(authedContext(auth.DeviceRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: `{"schemaVersion":`, ProfileToken: "thermostat"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Ok() || len(res.Diagnostics()) == 0 {
		t.Fatal("expected a graph-level diagnostic for undecodable JSON")
	}
}

// The compile is authz-gated: a caller without device:read is refused.
func TestCompileCanvas_RequiresAuth(t *testing.T) {
	r := &SchemaResolver{}
	_, err := r.CompileCanvas(authedContext(auth.CommandRead), struct {
		Graph        string
		ProfileToken string
	}{Graph: `{"schemaVersion":1,"nodes":[],"edges":[]}`, ProfileToken: "thermostat"})
	if err == nil {
		t.Fatal("expected an authorization error without device:read")
	}
}
