// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// Exercise the served contract: typed compiler fields survive the batch gate,
// scope errors name the input flag, and malformed definitions stay rule-wide.
// Existing clients can continue querying the unchanged message/index/token fields.
func TestValidationFieldsOverGraphQL(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	response := schema.Exec(authedContext(auth.DeviceRead), `query($rules: [DetectionRuleInput!]!) {
		validateDetectionRules(rules: $rules) { valid errors { index token message field } }
	}`, "", map[string]any{"rules": []any{
		map[string]any{"token": "ok", "definition": `{"name":"ok","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`},
		map[string]any{"token": "hold", "definition": `{"name":"hold","type":"duration","when":{"metric":"t","op":"gt","threshold":1}}`},
		map[string]any{"token": "json", "definition": `{"type":`},
		map[string]any{"token": "scope", "definition": `{"name":"gone","type":"absence","timeout":"5m"}`, "groupScoped": true},
	}})
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors: %v", response.Errors)
	}
	var result struct {
		Validation struct {
			Valid  bool
			Errors []struct {
				Index          int
				Token, Message string
				Field          *string
			}
		} `json:"validateDetectionRules"`
	}
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	v := result.Validation
	if v.Valid || len(v.Errors) != 3 {
		t.Fatalf("expected three rejected rules: %s", response.Data)
	}
	for i, want := range []struct{ token, field string }{{"hold", "hold"}, {"json", ""}, {"scope", "groupScoped"}} {
		got := v.Errors[i]
		if got.Index != i+1 || got.Token != want.token || got.Message == "" {
			t.Errorf("lost existing error contract: %+v", got)
		}
		if want.field == "" {
			if got.Field != nil {
				t.Errorf("rule-wide error has field %q", *got.Field)
			}
		} else if got.Field == nil || *got.Field != want.field {
			t.Errorf("%s: expected field %q, got %v", want.token, want.field, got.Field)
		}
	}
}

func TestValidationFieldUsesWrappedMetadata(t *testing.T) {
	cause := &rules.ValidationError{RuleID: "rule", Field: "when", Msg: "bad predicate"}
	err := fmt.Errorf("compile: %w", cause)
	r := newValidationError(4, "rule", err)
	if r.Field() == nil || *r.Field() != "when" || r.Message() != err.Error() || r.Index() != 4 || r.Token() != "rule" {
		t.Fatalf("wrapped metadata or legacy fields lost: %+v", r)
	}
	for _, err := range []error{
		fmt.Errorf("invalid rule %q: when: bad predicate", "rule"),
		&rules.ValidationError{RuleID: "rule", Msg: "invalid definition"},
	} {
		if got := newValidationError(0, "rule", err).Field(); got != nil {
			t.Errorf("invented anchor %q for %v", *got, err)
		}
	}
}
