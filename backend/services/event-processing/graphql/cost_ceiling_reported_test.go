// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// TestCostCeilingIsReportedToTheAuthor pins what an author sees when a rule's condition is too
// expensive: the refusal reaches the publish gate's answer, anchored to the rule, and states the
// platform ceiling the author has to get under. The ceiling is a literal because it is a platform
// constant; a change to it is a deliberate edit here too.
//
// The counterweight is in the same test: a cheap raw-CEL condition passes the same gate, so a gate
// that refused every raw-CEL rule would fail here rather than read as a working ceiling.
func TestCostCeilingIsReportedToTheAuthor(t *testing.T) {
	rule := func(cel string) string {
		q, _ := json.Marshal(cel)
		return `{"name":"c","type":"threshold","when":{"cel":` + string(q) + `}}`
	}

	cheap, err := (&SchemaResolver{}).ValidateDetectionRules(authedContext(auth.DeviceRead),
		struct{ Rules []detectionRuleInput }{Rules: []detectionRuleInput{
			{Token: "cheap", Definition: rule(`"t" in m && m["t"] > 1.0`)},
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cheap.Valid() {
		t.Fatalf("a cheap raw-CEL condition was refused, so the refusal below proves nothing: %+v", cheap.Errors())
	}

	res, err := (&SchemaResolver{}).ValidateDetectionRules(authedContext(auth.DeviceRead),
		struct{ Rules []detectionRuleInput }{Rules: []detectionRuleInput{
			{Token: "costly", Definition: rule(`m.all(k, m[k] > 0.0)`)},
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid() {
		t.Fatal("a condition iterating every measurement must be refused at the platform cost ceiling")
	}
	errs := res.Errors()
	if len(errs) != 1 {
		t.Fatalf("want exactly one anchored error, got %d", len(errs))
	}
	if errs[0].Token() != "costly" {
		t.Fatalf("the rejection must carry the rule token, got %q", errs[0].Token())
	}
	msg := errs[0].Message()
	if !strings.Contains(msg, "ceiling of 100") {
		t.Fatalf("the message must state the ceiling the author has to get under, got: %q", msg)
	}
}
