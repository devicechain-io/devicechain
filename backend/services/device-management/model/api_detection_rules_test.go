// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
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
