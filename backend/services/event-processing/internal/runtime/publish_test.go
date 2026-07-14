// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

const validRuleDef = `{"name":"n","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`

// CompilePublishedRules composes the runtime id "{tenant}/{profileVersionToken}/{ruleToken}"
// and scopes each compiled rule to its owning tenant + version.
func TestCompilePublishedRulesComposesIdAndScope(t *testing.T) {
	scoped, failed := CompilePublishedRules("acme", "prof@3", []dmmodel.PublishedDetectionRule{
		{Token: "hot", Definition: validRuleDef},
	})
	if failed != 0 {
		t.Fatalf("compile failures = %d, want 0", failed)
	}
	if len(scoped) != 1 {
		t.Fatalf("scoped = %d, want 1", len(scoped))
	}
	sr := scoped[0]
	if sr.Compiled.ID != "acme/prof@3/hot" {
		t.Fatalf("composed id = %q, want acme/prof@3/hot", sr.Compiled.ID)
	}
	if sr.Tenant != "acme" || sr.ProfileVersionToken != "prof@3" {
		t.Fatalf("scope = (%q,%q), want (acme,prof@3)", sr.Tenant, sr.ProfileVersionToken)
	}
	// The composed id round-trips through the tenant backstop's extractor.
	if tn, ok := RuleTenant(sr.Compiled.ID); !ok || tn != "acme" {
		t.Fatalf("RuleTenant(%q) = (%q,%v), want (acme,true)", sr.Compiled.ID, tn, ok)
	}
}

// An uncompilable rule is skipped and counted, never fatal — the rest of the fact still loads
// (defense-in-depth behind the publish gate that should have rejected it).
func TestCompilePublishedRulesSkipsUncompilable(t *testing.T) {
	scoped, failed := CompilePublishedRules("acme", "prof@1", []dmmodel.PublishedDetectionRule{
		{Token: "ok", Definition: validRuleDef},
		{Token: "garbage", Definition: `{"type":`}, // undecodable
		{Token: "no-hold", Definition: `{"name":"nh","type":"duration","when":{"metric":"t","op":"gt","threshold":1}}`}, // duration needs a positive hold
	})
	if failed != 2 {
		t.Fatalf("compile failures = %d, want 2", failed)
	}
	if len(scoped) != 1 || scoped[0].Compiled.ID != "acme/prof@1/ok" {
		t.Fatalf("only the compilable rule should survive: %+v", scoped)
	}
}

// An empty fact (no enabled rules) yields no rules and no failures.
func TestCompilePublishedRulesEmpty(t *testing.T) {
	scoped, failed := CompilePublishedRules("acme", "prof@1", nil)
	if len(scoped) != 0 || failed != 0 {
		t.Fatalf("empty fact should yield (0,0); got (%d,%d)", len(scoped), failed)
	}
}

// The group scope (ADR-062 S4) rides the fact onto the ScopedRule so Plan can set-test it.
func TestCompilePublishedRulesCarriesGroupScope(t *testing.T) {
	scoped, failed := CompilePublishedRules("acme", "prof@1", []dmmodel.PublishedDetectionRule{
		{Token: "hot", Definition: validRuleDef, EntityGroupToken: "arid-areas", EntityGroupVersion: 4},
		{Token: "wide", Definition: validRuleDef}, // unscoped
	})
	if failed != 0 || len(scoped) != 2 {
		t.Fatalf("both rules should compile; failed=%d scoped=%d", failed, len(scoped))
	}
	byID := map[string]ScopedRule{}
	for _, sr := range scoped {
		byID[sr.Compiled.ID] = sr
	}
	if sr := byID["acme/prof@1/hot"]; sr.GroupToken != "arid-areas" || sr.GroupVersion != 4 {
		t.Fatalf("scoped rule must carry its group pin; got (%q,%d)", sr.GroupToken, sr.GroupVersion)
	}
	if sr := byID["acme/prof@1/wide"]; sr.GroupToken != "" {
		t.Fatalf("unscoped rule must carry no group; got %q", sr.GroupToken)
	}
}

// A group scope on a kind the scope cannot apply to (absence timer-driven, correlation
// anchor-keyed) is refused at load — fail closed, the rule does not run (ADR-062 S4).
func TestCompilePublishedRulesRejectsScopeOnUnsupportedKind(t *testing.T) {
	absence := `{"name":"gone","type":"absence","timeout":"5m"}`
	correlation := `{"name":"co","type":"correlation","anchorType":"site","count":3,"window":"100s"}`
	scoped, failed := CompilePublishedRules("acme", "prof@1", []dmmodel.PublishedDetectionRule{
		{Token: "ok", Definition: validRuleDef, EntityGroupToken: "arid-areas", EntityGroupVersion: 1},
		{Token: "abs", Definition: absence, EntityGroupToken: "arid-areas", EntityGroupVersion: 1},
		{Token: "corr", Definition: correlation, EntityGroupToken: "arid-areas", EntityGroupVersion: 1},
	})
	if failed != 2 {
		t.Fatalf("the scoped absence + correlation rules must be refused; failed=%d", failed)
	}
	if len(scoped) != 1 || scoped[0].Compiled.ID != "acme/prof@1/ok" {
		t.Fatalf("only the supported scoped rule should load; got %+v", scoped)
	}
}

// The SAME absence/correlation rules load fine when UNSCOPED — the refusal is specific to
// having a group scope, not to the kind itself.
func TestCompilePublishedRulesAllowsUnscopedAbsenceCorrelation(t *testing.T) {
	absence := `{"name":"gone","type":"absence","timeout":"5m"}`
	correlation := `{"name":"co","type":"correlation","anchorType":"site","count":3,"window":"100s"}`
	scoped, failed := CompilePublishedRules("acme", "prof@1", []dmmodel.PublishedDetectionRule{
		{Token: "abs", Definition: absence},
		{Token: "corr", Definition: correlation},
	})
	if failed != 0 || len(scoped) != 2 {
		t.Fatalf("unscoped absence + correlation must load; failed=%d scoped=%d", failed, len(scoped))
	}
}
