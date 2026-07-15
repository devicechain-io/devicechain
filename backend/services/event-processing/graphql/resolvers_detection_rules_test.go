// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// authedContext returns a context carrying a claims set with the given authorities, as the
// GraphQL auth middleware would populate from a validated bearer token.
func authedContext(authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(context.Background(), &auth.Claims{Authorities: strs})
}

// A well-formed threshold rule compiles; the gate reports valid with no errors.
func TestValidateDetectionRules_Valid(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "hot", Definition: `{"name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid() {
		t.Fatalf("expected valid, got errors: %+v", res.Errors())
	}
	if len(res.Errors()) != 0 {
		t.Fatalf("expected no errors, got %d", len(res.Errors()))
	}
}

// A batch mixing a good rule, an undecodable blob, and a structurally-invalid rule reports
// invalid and returns one anchored error per rejection (positional index + token), while
// the good rule is not reported.
func TestValidateDetectionRules_MixedRejections(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "ok", Definition: `{"name":"ok","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`},
			{Token: "garbage", Definition: `{"type":`}, // undecodable
			{Token: "no-hold", Definition: `{"name":"nh","type":"duration","when":{"metric":"t","op":"gt","threshold":1}}`}, // duration needs a positive hold
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid() {
		t.Fatalf("expected invalid")
	}
	if len(res.Errors()) != 2 {
		t.Fatalf("expected 2 errors, got %d: %+v", len(res.Errors()), res.Errors())
	}
	// Errors are anchored to the offending input by index + token, in submission order.
	if res.Errors()[0].Index() != 1 || res.Errors()[0].Token() != "garbage" {
		t.Errorf("first error not anchored to the garbage blob: idx=%d token=%q", res.Errors()[0].Index(), res.Errors()[0].Token())
	}
	if res.Errors()[1].Index() != 2 || res.Errors()[1].Token() != "no-hold" {
		t.Errorf("second error not anchored to the no-hold rule: idx=%d token=%q", res.Errors()[1].Index(), res.Errors()[1].Token())
	}
	if res.Errors()[1].Message() == "" {
		t.Errorf("expected a non-empty failure reason")
	}
}

// A group scope (ADR-062 S4) on an absence or correlation rule is rejected with an
// author-facing error; the same rule UNSCOPED is accepted (the refusal is about the scope,
// not the kind), and a scoped threshold is accepted (a supported kind).
func TestValidateDetectionRules_RejectsScopeOnUnsupportedKind(t *testing.T) {
	r := &SchemaResolver{}
	absence := `{"name":"gone","type":"absence","timeout":"5m"}`
	correlation := `{"name":"co","type":"correlation","anchorType":"site","count":3,"window":"100s"}`
	threshold := `{"name":"hot","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "scoped-abs", Definition: absence, GroupScoped: true},
			{Token: "scoped-corr", Definition: correlation, GroupScoped: true},
			{Token: "unscoped-abs", Definition: absence, GroupScoped: false},
			{Token: "scoped-thr", Definition: threshold, GroupScoped: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid() {
		t.Fatal("expected invalid (two scoped rules on unsupported kinds)")
	}
	if len(res.Errors()) != 2 {
		t.Fatalf("expected exactly 2 rejections (the scoped absence + correlation); got %d: %+v", len(res.Errors()), res.Errors())
	}
	if res.Errors()[0].Token() != "scoped-abs" || res.Errors()[1].Token() != "scoped-corr" {
		t.Fatalf("rejections must anchor to the scoped absence + correlation; got %q, %q",
			res.Errors()[0].Token(), res.Errors()[1].Token())
	}
	if res.Errors()[0].Message() == "" {
		t.Error("expected an author-facing reason")
	}
}

// A definition with no id still validates: the resolver forces the token as the compile-time
// id, so a rule the author stored without the runtime-composed id is not spuriously rejected.
func TestValidateDetectionRules_TokenSuppliesId(t *testing.T) {
	r := &SchemaResolver{}
	res, err := r.ValidateDetectionRules(authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{
			{Token: "the-token", Definition: `{"name":"n","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid() {
		t.Fatalf("a definition without an id should validate (token supplies it); got %+v", res.Errors())
	}
}

// The gate is authz-gated: a caller without device:read is refused before any compilation.
func TestValidateDetectionRules_RequiresAuth(t *testing.T) {
	r := &SchemaResolver{}
	_, err := r.ValidateDetectionRules(authedContext(auth.CommandRead), struct{ Rules []detectionRuleInput }{
		Rules: []detectionRuleInput{{Token: "x", Definition: `{"name":"n","type":"threshold"}`}},
	})
	if err == nil {
		t.Fatalf("expected an authorization error without device:read")
	}
}
