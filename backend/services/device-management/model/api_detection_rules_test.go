// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// A detection rule rides in the profile version snapshot as an opaque JSON document.
// The publish→resolve path depends on the rule body surviving the Go-internal
// marshal/parse round-trip byte-for-byte (event-processing decodes it strictly), so pin
// the identity, the definition blob, and the enabled flag.
func TestDetectionRuleSnapshotRoundTrip(t *testing.T) {
	def := datatypes.JSON([]byte(`{"name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`))
	rule := &DetectionRule{
		Model:           gorm.Model{ID: 91},
		TenantScoped:    rdb.TenantScoped{TenantId: "acme"},
		TokenReference:  rdb.TokenReference{Token: "hot"},
		DeviceProfileId: 7,
		Definition:      def,
		Enabled:         true,
	}

	raw, err := json.Marshal(ProfileSnapshot{Rules: []*DetectionRule{rule}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := parseProfileSnapshot(datatypes.JSON(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("rules len = %d, want 1", len(got.Rules))
	}
	gr := got.Rules[0]
	if gr.ID != 91 || gr.Token != "hot" || !gr.Enabled {
		t.Errorf("rule identity lost: id=%d token=%q enabled=%v", gr.ID, gr.Token, gr.Enabled)
	}
	if string(gr.Definition) != string(def) {
		t.Errorf("rule definition blob changed:\n got %s\nwant %s", gr.Definition, def)
	}
}

// parseProfileSnapshot normalizes a missing rules list to a non-nil empty slice, so a
// version published before rules existed (or with no rules) never nil-derefs downstream.
func TestParseProfileSnapshotRulesEmpty(t *testing.T) {
	for name, raw := range map[string]datatypes.JSON{
		"nil":        nil,
		"empty":      datatypes.JSON([]byte(``)),
		"emptyLists": datatypes.JSON([]byte(`{}`)),
		"noRulesKey": datatypes.JSON([]byte(`{"metrics":[]}`)),
	} {
		got, err := parseProfileSnapshot(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if got.Rules == nil {
			t.Errorf("%s: rules nil", name)
		}
		if len(got.Rules) != 0 {
			t.Errorf("%s: rules non-empty: %+v", name, got.Rules)
		}
	}
}

// validateDetectionRuleDefinition guards only JSON well-formedness — the authoritative
// taxonomy/cost validation is event-processing's at publish. It must reject the common
// malformed-blob mistakes (empty, bad JSON, non-object) and accept a JSON object without
// parsing the rule.
func TestValidateDetectionRuleDefinition(t *testing.T) {
	cases := []struct {
		name  string
		def   string
		valid bool
	}{
		{"empty", "", false},
		{"malformed", `{"type":`, false},
		{"array", `[{"type":"threshold"}]`, false},
		{"scalar", `"threshold"`, false},
		{"number", `42`, false},
		{"null", `null`, false},
		{"bool", `true`, false},
		{"object", `{"name":"hot","type":"threshold"}`, true},
		{"emptyObject", `{}`, true}, // shape-valid; event-processing rejects it at compile
	}
	for _, c := range cases {
		err := validateDetectionRuleDefinition(c.def)
		if c.valid && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// validateAuthoringGraph guards only JSON-object shape (the taxonomy stays single-homed in
// event-processing): nil is valid (form-authored), a JSON object is valid, everything else is
// rejected before it is stored.
func TestValidateAuthoringGraph(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name  string
		graph *string
		valid bool
	}{
		{"nil", nil, true},
		{"object", str(`{"schemaVersion":1,"nodes":[],"edges":[]}`), true},
		{"emptyObject", str(`{}`), true},
		{"malformed", str(`{"nodes":`), false},
		{"array", str(`[]`), false},
		{"scalar", str(`3`), false},
		{"null", str(`null`), false},
		{"emptyString", str(``), false},
	}
	for _, c := range cases {
		err := validateAuthoringGraph(c.graph)
		if c.valid && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// The authoring graph is authoring metadata ONLY — it must NOT ride into the publish snapshot
// (json:"-"), so the frozen version carries just the runtime definition. A snapshot bloated
// with every rule's canvas would waste storage and confuse the event-processing consumer.
func TestAuthoringGraphExcludedFromSnapshot(t *testing.T) {
	rule := &DetectionRule{
		Model:          gorm.Model{ID: 5},
		TokenReference: rdb.TokenReference{Token: "hot"},
		Definition:     datatypes.JSON([]byte(`{"name":"hot","type":"threshold"}`)),
		AuthoringGraph: datatypes.JSON([]byte(`{"schemaVersion":1,"nodes":[{"id":"secret"}],"edges":[]}`)),
		Enabled:        true,
	}
	raw, err := json.Marshal(ProfileSnapshot{Rules: []*DetectionRule{rule}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "authoringGraph") || strings.Contains(string(raw), "AuthoringGraph") || strings.Contains(string(raw), "secret") {
		t.Fatalf("authoring graph leaked into the publish snapshot: %s", raw)
	}
	// The definition still survives the round-trip.
	got, err := parseProfileSnapshot(datatypes.JSON(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Rules) != 1 || string(got.Rules[0].Definition) != string(rule.Definition) {
		t.Fatalf("definition lost from snapshot: %+v", got.Rules)
	}
}

// The publish gate (ADR-044 slice 4b) compiles only ENABLED draft rules: a disabled rule
// is inert (the runtime skips it), so it must not be submitted for validation and thus
// cannot block a publish. The projection also carries the token + definition verbatim.
func TestEnabledRulesToValidate(t *testing.T) {
	rules := []*DetectionRule{
		{TokenReference: rdb.TokenReference{Token: "on1"}, Definition: datatypes.JSON([]byte(`{"a":1}`)), Enabled: true},
		{TokenReference: rdb.TokenReference{Token: "off"}, Definition: datatypes.JSON([]byte(`{"b":2}`)), Enabled: false},
		{TokenReference: rdb.TokenReference{Token: "on2"}, Definition: datatypes.JSON([]byte(`{"c":3}`)), Enabled: true},
	}
	got := enabledRulesToValidate(rules)
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled rules, got %d: %+v", len(got), got)
	}
	if got[0].Token != "on1" || got[0].Definition != `{"a":1}` {
		t.Errorf("first enabled rule wrong: %+v", got[0])
	}
	if got[1].Token != "on2" || got[1].Definition != `{"c":3}` {
		t.Errorf("second enabled rule wrong: %+v", got[1])
	}
}

// An all-disabled (or empty) set yields nothing to validate, so the publish gate is a
// no-op rather than a spurious empty service call.
func TestEnabledRulesToValidate_NoneEnabled(t *testing.T) {
	if got := enabledRulesToValidate(nil); len(got) != 0 {
		t.Errorf("nil rules: expected empty, got %+v", got)
	}
	disabled := []*DetectionRule{{TokenReference: rdb.TokenReference{Token: "x"}, Enabled: false}}
	if got := enabledRulesToValidate(disabled); len(got) != 0 {
		t.Errorf("all-disabled: expected empty, got %+v", got)
	}
}

// fakeRuleValidator records the rules it was handed and returns canned results, so the
// publish gate (validateSnapshotDetectionRules) can be exercised without a live
// event-processing or a database.
type fakeRuleValidator struct {
	called   bool
	seen     []RuleToValidate
	failures []DetectionRuleValidationFailure
	err      error
}

func (f *fakeRuleValidator) ValidateDetectionRules(_ context.Context, rules []RuleToValidate) ([]DetectionRuleValidationFailure, error) {
	f.called = true
	f.seen = rules
	return f.failures, f.err
}

// snapshotOf marshals a set of detection rules into a profile snapshot blob the same way
// buildProfileSnapshot does, so the gate validates the exact bytes a publish would freeze.
func snapshotOf(rules ...*DetectionRule) datatypes.JSON {
	raw, err := json.Marshal(ProfileSnapshot{Rules: rules})
	if err != nil {
		panic(err)
	}
	return datatypes.JSON(raw)
}

func enabledRule(token string) *DetectionRule {
	return &DetectionRule{
		TokenReference: rdb.TokenReference{Token: token},
		Definition:     datatypes.JSON([]byte(`{"name":"n","type":"threshold"}`)),
		Enabled:        true,
	}
}

// A nil validator (service secret unset) skips the gate: publish must not depend on
// event-processing being wired.
func TestValidateSnapshotDetectionRules_NilValidatorSkips(t *testing.T) {
	api := &Api{}
	if err := api.validateSnapshotDetectionRules(context.Background(), snapshotOf(enabledRule("x"))); err != nil {
		t.Fatalf("nil validator should skip, got: %v", err)
	}
}

// The gate submits only ENABLED rules from the snapshot and passes when all compile.
func TestValidateSnapshotDetectionRules_EnabledOnlyAndPasses(t *testing.T) {
	disabled := enabledRule("off")
	disabled.Enabled = false
	fake := &fakeRuleValidator{}
	api := &Api{DetectionRuleValidator: fake}

	err := api.validateSnapshotDetectionRules(context.Background(), snapshotOf(enabledRule("on"), disabled))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.called || len(fake.seen) != 1 || fake.seen[0].Token != "on" {
		t.Fatalf("gate did not submit exactly the enabled rule: called=%v seen=%+v", fake.called, fake.seen)
	}
}

// A snapshot with no enabled rules short-circuits without calling the validator.
func TestValidateSnapshotDetectionRules_NoEnabledSkipsCall(t *testing.T) {
	disabled := enabledRule("off")
	disabled.Enabled = false
	fake := &fakeRuleValidator{}
	api := &Api{DetectionRuleValidator: fake}

	if err := api.validateSnapshotDetectionRules(context.Background(), snapshotOf(disabled)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.called {
		t.Fatal("validator called for an all-disabled snapshot")
	}
}

// A rejected rule fails the publish closed, and the author-facing error names the rule and
// carries the compiler's reason.
func TestValidateSnapshotDetectionRules_RejectionFailsClosed(t *testing.T) {
	fake := &fakeRuleValidator{failures: []DetectionRuleValidationFailure{{Token: "bad", Message: "no hold"}}}
	api := &Api{DetectionRuleValidator: fake}

	err := api.validateSnapshotDetectionRules(context.Background(), snapshotOf(enabledRule("bad")))
	if err == nil {
		t.Fatal("expected a fail-closed error for a rejected rule")
	}
	if got := err.Error(); !strings.Contains(got, "bad") || !strings.Contains(got, "no hold") {
		t.Errorf("error should name the rule and reason, got: %q", got)
	}
}

// A transport/availability failure fails the publish closed with a SANITIZED message — the
// raw error (which may name in-cluster hosts) must not reach the API client.
func TestValidateSnapshotDetectionRules_TransportFailsClosedSanitized(t *testing.T) {
	fake := &fakeRuleValidator{err: fmt.Errorf("dial tcp 10.1.2.3:8080: connection refused")}
	api := &Api{DetectionRuleValidator: fake}

	err := api.validateSnapshotDetectionRules(context.Background(), snapshotOf(enabledRule("x")))
	if err == nil {
		t.Fatal("expected a fail-closed error on a transport failure")
	}
	if strings.Contains(err.Error(), "10.1.2.3") || strings.Contains(err.Error(), "connection refused") {
		t.Errorf("transport detail leaked to the caller: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected a sanitized 'unavailable' message, got: %q", err.Error())
	}
}

// The rule token becomes part of a per-tenant subject via the runtime rule id, so it must
// satisfy the ADR-042 token grammar.
func TestValidateDetectionRuleToken(t *testing.T) {
	if err := validateDetectionRuleToken("hot-rule_1"); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	for _, bad := range []string{"", "has space", "has/slash", "-leading"} {
		if err := validateDetectionRuleToken(bad); err == nil {
			t.Errorf("invalid token %q accepted", bad)
		}
	}
}
