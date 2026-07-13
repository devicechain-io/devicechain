// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
)

// TestCompileValidConnectorActions proves well-formed httpCall and publish actions compile —
// minimal shapes and fully-populated ones (headers, secret handle, timeout, CEL templates).
func TestCompileValidConnectorActions(t *testing.T) {
	valid := []struct {
		name string
		a    Action
	}{
		{"httpCall minimal", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://hooks.example/x"}}},
		{"httpCall full", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{
			URL:          "https://hooks.example/x",
			Method:       "POST",
			Headers:      map[string]string{"X-Custom": "ok"},
			BodyTemplate: `'{"series":"' + series + '"}'`,
			SecretRef:    "httpcall/acme/0/auth",
			TimeoutMs:    5000,
		}}},
		{"publish minimal", Action{Type: ActionPublish, Publish: &PublishAction{ConnectorRef: "kafka-main"}}},
		{"publish full", Action{Type: ActionPublish, Publish: &PublishAction{
			ConnectorRef:    "kafka-main",
			PayloadTemplate: `'v=' + string(value)`,
			TimeoutMs:       2000,
		}}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(thresholdWith(SeverityCritical, tc.a), testLimits); err != nil {
				t.Fatalf("expected %s to compile, got %v", tc.name, err)
			}
		})
	}
}

// TestCompileRejectsMalformedConnectorActions sweeps the fail-closed cases for the new actions:
// bad URL/method/header/secret/timeout/template on httpCall, missing/bad connectorRef and bad
// template on publish, plus the type/variant mismatches the four-way exclusivity check must catch.
func TestCompileRejectsMalformedConnectorActions(t *testing.T) {
	cases := []struct {
		name string
		a    Action
	}{
		{"httpCall non-http url", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "ftp://x/y"}}},
		{"httpCall missing url", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{}}},
		{"httpCall non-post method", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", Method: "DELETE"}}},
		{"httpCall reserved header", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"Authorization": "Bearer forged"}}}},
		{"httpCall reserved x-dc header", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", Headers: map[string]string{"X-DC-Tenant": "victim"}}}},
		{"httpCall bad secret handle", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", SecretRef: "bad handle!"}}},
		{"httpCall timeout over cap", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", TimeoutMs: MaxActionTimeoutMs + 1}}},
		{"httpCall negative timeout", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", TimeoutMs: -1}}},
		{"httpCall boolean body template", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", BodyTemplate: "value > 10"}}},
		{"httpCall unparseable body template", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", BodyTemplate: "this is not cel ++"}}},
		{"publish missing connectorRef", Action{Type: ActionPublish, Publish: &PublishAction{}}},
		{"publish bad connectorRef token", Action{Type: ActionPublish, Publish: &PublishAction{ConnectorRef: "bad ref!"}}},
		{"publish boolean payload template", Action{Type: ActionPublish, Publish: &PublishAction{ConnectorRef: "c", PayloadTemplate: "hasValue"}}},
		{"httpCall type with publish payload", Action{Type: ActionHTTPCall, Publish: &PublishAction{ConnectorRef: "c"}}},
		{"httpCall multiply populated", Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y"}, Publish: &PublishAction{ConnectorRef: "c"}}},
		{"publish missing payload variant", Action{Type: ActionPublish}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(thresholdWith(SeverityCritical, tc.a), testLimits); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

// TestConnectorActionDedup proves exact-duplicate connector actions are rejected, while actions
// that differ in a meaningful field (a header, the connectorRef) are distinct and both allowed.
func TestConnectorActionDedup(t *testing.T) {
	call := func(hdr string) Action {
		h := &HTTPCallAction{URL: "https://x/y"}
		if hdr != "" {
			h.Headers = map[string]string{"X-Env": hdr}
		}
		return Action{Type: ActionHTTPCall, HTTPCall: h}
	}
	// Two identical httpCalls → duplicate rejected.
	if _, err := Compile(thresholdWith(SeverityCritical, call(""), call("")), testLimits); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("identical httpCalls should be a duplicate, got %v", err)
	}
	// Differ only by a header → distinct, both allowed (the header folds into the identity key).
	if _, err := Compile(thresholdWith(SeverityCritical, call("prod"), call("staging")), testLimits); err != nil {
		t.Fatalf("header-distinct httpCalls should compile, got %v", err)
	}
	// Two publishes differing only by connectorRef → distinct.
	p := func(ref string) Action {
		return Action{Type: ActionPublish, Publish: &PublishAction{ConnectorRef: ref}}
	}
	if _, err := Compile(thresholdWith(SeverityCritical, p("kafka-a"), p("kafka-b")), testLimits); err != nil {
		t.Fatalf("connectorRef-distinct publishes should compile, got %v", err)
	}
}

// TestCompileTemplateRequiresStringAndGatesCost proves the payload-template compiler requires a
// string output (rejecting a boolean) and enforces the cost ceiling.
func TestCompileTemplateRequiresStringAndGatesCost(t *testing.T) {
	// A constant string compiles.
	if _, err := CompileTemplate(`"literal"`, 100); err != nil {
		t.Fatalf("constant string template should compile: %v", err)
	}
	// A boolean is rejected — a template must render bytes, not a bit.
	if _, err := CompileTemplate("hasValue", 100); err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("boolean template should be rejected as non-string, got %v", err)
	}
	// Cost gate: a template that estimates above the ceiling is rejected.
	src := "series + series + series"
	cost, err := CompileTemplate(src, 100_000)
	if err != nil {
		t.Fatalf("template should compile under a generous ceiling: %v", err)
	}
	if cost == 0 {
		t.Fatal("expected a non-zero estimated cost for a concatenation template")
	}
	if _, err := CompileTemplate(src, cost-1); err == nil || !strings.Contains(err.Error(), "cost") {
		t.Fatalf("template over the ceiling should be rejected, got %v", err)
	}
}

// TestTemplateProgramRendersAgainstDerivedEvent proves a built template evaluates against the
// derived-event vocabulary (value / hasValue / series) to the expected rendered string.
func TestTemplateProgramRendersAgainstDerivedEvent(t *testing.T) {
	prog, err := BuildTemplateProgram(`'{"series":"' + series + '","hasValue":' + string(hasValue) + '}'`)
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	v := 42.0
	out, err := prog.Eval(GuardInput{Value: &v, Series: "device-7"})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if out != `{"series":"device-7","hasValue":true}` {
		t.Fatalf("rendered = %q", out)
	}
	// A nil value renders hasValue=false (no value on this detection).
	out, err = prog.Eval(GuardInput{Series: "device-7"})
	if err != nil {
		t.Fatalf("eval no-value: %v", err)
	}
	if !strings.Contains(out, `"hasValue":false`) {
		t.Fatalf("nil-value render should report hasValue=false, got %q", out)
	}
}

// TestBuildTemplateProgramRejectsNonString proves the dispatch-side builder also fails closed on a
// non-string template (a forged/hand-edited rule that skipped the publish gate).
func TestBuildTemplateProgramRejectsNonString(t *testing.T) {
	if _, err := BuildTemplateProgram("hasValue"); err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("non-string template should be rejected at build, got %v", err)
	}
}
