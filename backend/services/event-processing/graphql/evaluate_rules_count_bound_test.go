// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// One source feeds every condition; each condition lowers to one rule.
func evaluationCountInput(t *testing.T, count int) evaluateRulesInput {
	t.Helper()
	def := graph.CanvasDefinition{
		SchemaVersion: graph.SchemaVersion,
		Nodes: []graph.Node{{
			ID: "source", Type: graph.NodeSource,
			Config: json.RawMessage(`{"scope":{"kind":"profile","profileToken":"thermostat"}}`),
		}},
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("condition%d", i)
		def.Nodes = append(def.Nodes, graph.Node{
			ID: id, Type: graph.NodeThreshold,
			Config: json.RawMessage(`{"name":"hot","when":{"metric":"temperature","op":"gt","threshold":{"kind":"literal","value":80}}}`),
		})
		def.Edges = append(def.Edges, graph.Edge{From: "source:out", To: id + ":in"})
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal canvas: %v", err)
	}
	canvas := string(data)
	return evaluateRulesInput{
		ProfileToken: "thermostat", RuleSet: ruleSetDraft, Graph: &canvas,
		Events: []evaluateEventInput{{
			DeviceToken: "device", OccurredAt: "2026-03-01T00:00:00Z",
			Measurements: []evaluateMeasurementInput{{Name: "temperature", Value: 20}},
		}},
	}
}

func TestEvaluateRules_RefusesMoreThanTheBoundBeforeCompiling(t *testing.T) {
	r := &SchemaResolver{}
	ctx := core.WithTenant(authedContext(auth.DeviceRead), "acme")
	in := evaluationCountInput(t, evaluate.MaxRules+1)
	// One threshold has an invalid config that only compilation rejects.
	canvas := strings.Replace(*in.Graph, `"metric":"temperature"`, `"metric":42`, 1)
	in.Graph = &canvas
	res, err := r.EvaluateRules(ctx, struct{ Input evaluateRulesInput }{
		Input: in,
	})
	if err != nil || res == nil {
		t.Fatalf("expected a refusal result, got result %v, error %v", res, err)
	}
	if res.Ok() {
		t.Fatal("expected ok false for an oversized draft graph")
	}
	if reason := res.Refusal(); reason == nil || *reason != "LIMIT_EXCEEDED" {
		t.Fatalf("expected LIMIT_EXCEEDED, got: %v", reason)
	}
	diags := res.Diagnostics()
	if len(diags) != 1 {
		t.Fatalf("expected one diagnostic, got: %v", diags)
	}
	if msg := diags[0].Message(); !strings.Contains(msg, "257") || !strings.Contains(msg, "256") {
		t.Fatalf("expected diagnostic to name 257 and 256, got: %q", msg)
	}
}

func TestEvaluateRules_CountBoundIsInclusive(t *testing.T) {
	r := &SchemaResolver{}
	ctx := core.WithTenant(authedContext(auth.DeviceRead), "acme")
	res, err := r.EvaluateRules(ctx, struct{ Input evaluateRulesInput }{
		Input: evaluationCountInput(t, evaluate.MaxRules),
	})
	if err != nil || res == nil {
		t.Fatalf("expected a result at the bound, got result %v, error %v", res, err)
	}
	if reason := res.Refusal(); reason != nil && *reason == "LIMIT_EXCEEDED" {
		t.Fatalf("unexpected LIMIT_EXCEEDED at the bound: %v", res.Diagnostics())
	}
	if !res.Ok() {
		t.Fatalf("expected ok true at the bound, got refusal %v, diagnostics %v", res.Refusal(), res.Diagnostics())
	}
}

func TestDraftEvaluationRules_LowersExactlyTheBound(t *testing.T) {
	lowered, warnings, diags, refusal := draftEvaluationRules(evaluationCountInput(t, evaluate.MaxRules))
	if refusal != nil {
		t.Fatalf("unexpected refusal at the bound: %v", refusal.Refusal())
	}
	if len(diags) != 0 || len(warnings) != 0 {
		t.Fatalf("expected no diagnostics, got errors %v, warnings %v", diags, warnings)
	}
	if len(lowered) != evaluate.MaxRules {
		t.Fatalf("expected %d lowered rules, got %d", evaluate.MaxRules, len(lowered))
	}
}
