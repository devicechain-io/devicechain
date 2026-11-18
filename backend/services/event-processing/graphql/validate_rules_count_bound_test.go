// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/limit"
)

// validateDetectionRules needs only device:read, and it decoded and CEL-compiled every
// entry it was sent: one request body of tiny rules carried thousands of compiles. Past
// MaxRulesPerValidation the whole call is refused before the first rule is touched.
func TestValidateDetectionRules_RefusesMoreThanTheBoundBeforeCompiling(t *testing.T) {
	inputs := make([]detectionRuleInput, MaxRulesPerValidation+1)
	for i := range inputs {
		inputs[i] = detectionRuleInput{Token: fmt.Sprintf("r%d", i), Definition: `{"type":`}
	}
	// On main this returns a result carrying 1,001 per-rule decode errors and no
	// refusal: every entry was processed. A refusal with no result proves none was.
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{Rules: inputs})
	if res != nil {
		t.Fatalf("expected no result for an oversized batch, got one carrying %d per-rule errors", len(res.Errors()))
	}
	if err == nil {
		t.Fatalf("expected a limit refusal")
	}
	le, ok := limit.As(err)
	if !ok {
		t.Fatalf("expected a limit refusal, got: %v", err)
	}
	if le.Got != MaxRulesPerValidation+1 || le.Max != MaxRulesPerValidation {
		t.Fatalf("unexpected limit refusal: %+v", le)
	}
	ext, ok := err.(interface{ Extensions() map[string]any })
	if !ok {
		t.Fatalf("expected GraphQL extensions on the refusal, got: %T", err)
	}
	if got := ext.Extensions()["code"]; got != "LIMIT_EXCEEDED" {
		t.Fatalf("expected LIMIT_EXCEEDED, got: %v", got)
	}
}

// The bound is inclusive: a list of exactly MaxRulesPerValidation rules compiles as before.
func TestValidateDetectionRules_AcceptsExactlyTheBound(t *testing.T) {
	inputs := make([]detectionRuleInput, MaxRulesPerValidation)
	for i := range inputs {
		inputs[i] = detectionRuleInput{
			Token:      fmt.Sprintf("r%d", i),
			Definition: `{"name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`,
		}
	}
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{Rules: inputs})
	if err != nil {
		t.Fatalf("unexpected error at the bound: %v", err)
	}
	if res == nil {
		t.Fatalf("expected a validation result at the bound")
	}
	if !res.Valid() {
		t.Fatalf("expected valid, got errors: %+v", res.Errors())
	}
	if len(res.Errors()) != 0 {
		t.Fatalf("expected no errors, got %d", len(res.Errors()))
	}
}

// The schema description and the API reference both state 1,000.
func TestValidateDetectionRules_BoundIsOneThousand(t *testing.T) {
	if MaxRulesPerValidation != 1000 {
		t.Fatalf("expected the documented bound of 1000, got %d", MaxRulesPerValidation)
	}
}

// A caller without device:read learns nothing, not even that the list is too long.
func TestValidateDetectionRules_CountBoundComesAfterAuth(t *testing.T) {
	inputs := make([]detectionRuleInput, MaxRulesPerValidation+1)
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(), struct{ Rules []detectionRuleInput }{Rules: inputs})
	if res != nil {
		t.Fatalf("expected no result without device:read, got one carrying %d per-rule errors", len(res.Errors()))
	}
	if err == nil {
		t.Fatalf("expected an authorization error without device:read")
	}
	if _, ok := limit.As(err); ok {
		t.Fatalf("expected authorization before the count bound, got: %v", err)
	}
}
