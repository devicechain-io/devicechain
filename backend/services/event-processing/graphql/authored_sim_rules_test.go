// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The sim's authored DETECT rules, run through the REAL publish gate.
//
// dc-simulator hand-writes its rules as an untyped map[string]any and publishes the
// marshalled bytes as an opaque string. It CANNOT import this module — it is
// deliberately an untrusted external client of the platform, like a real device
// integration — so on that side a misspelled key is free: `treshold` marshals fine,
// publishes fine, the profile version goes active, and the rule never fires. The live
// ruleHealth post-assert catches that against a real cluster; until this file existed,
// no CI gate caught it at all.
//
// The two sides meet at a committed fixture the sim generates from the same values its
// provisioning paths publish (backend/sims/dc-simulator/loadtest/
// authored_rules_fixture_test.go writes it; a staleness gate there keeps it current).
//
// 🔴 THIS DRIVES ValidateDetectionRules, NOT A RECONSTRUCTION OF IT. Calling
// rules.Decode + rules.Compile here would have been a second implementation of the gate
// that has to agree with the first one — and it would already have been WRONG: the
// stored blob carries no rule id, and the resolver forces the token as the compile-time
// id before compiling. A hand-rolled version omitting that rejects every rule the
// platform accepts, which reads as "the sim is broken" and would have been fixed by
// weakening the check. Driving the resolver makes the question exactly "would a profile
// publish carrying this rule succeed?", which is the only question worth asking.

const authoredRuleFixturePath = "../../../testdata/authored-rules/authored-rules.json"

// authoredRule mirrors one fixture entry. It is decoded with DisallowUnknownFields, so a
// field added on the producing side fails here loudly rather than being silently
// dropped — the same fail-closed posture the rule decoder itself takes.
type authoredRule struct {
	Producer          string `json:"producer"`
	RuleToken         string `json:"ruleToken"`
	ProfileToken      string `json:"profileToken"`
	Enabled           bool   `json:"enabled"`
	AlarmSeverityWire string `json:"alarmSeverityWire"`
	Definition        string `json:"definition"`
	// GroupScoped mirrors the per-rule entity-group scope device-management forwards to
	// this gate (ADR-062 S4 rejects a scope on a kind that cannot carry one). No sim rule
	// is scoped today, so it is false throughout — but it is submitted rather than
	// hardcoded here, so that the day a scenario authors a scoped rule this test asks the
	// gate the same question the real publish path does instead of a laxer one.
	GroupScoped bool `json:"groupScoped"`
}

// authoredSimFixture is the whole committed file. Every section is declared because the
// decode fails closed; only the ones this module owns are judged. alarmState and
// commandStatus belong to device-management and command-delivery, whose own fixture tests
// hold them to their real enums — silently tolerating an unknown section here would be
// the exact posture this arrangement exists to reject.
type authoredSimFixture struct {
	Rules          []authoredRule `json:"rules"`
	WireVocabulary struct {
		AlarmState          json.RawMessage `json:"alarmState"`
		CommandStatus       json.RawMessage `json:"commandStatus"`
		DetectionRuleStatus struct {
			Active string `json:"active"`
		} `json:"detectionRuleStatus"`
	} `json:"wireVocabulary"`
}

func loadAuthoredSimFixture(t *testing.T) authoredSimFixture {
	t.Helper()
	raw, err := os.ReadFile(authoredRuleFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v\n\nThis fixture is the ONLY thing holding the sim's authored "+
			"rules to the publish gate. If it moved, re-point this test rather than deleting "+
			"it; regenerate it with:\n  cd backend/sims/dc-simulator && go test ./loadtest/ "+
			"-run TestAuthoredRuleFixtureIsCurrent -update-rule-fixture",
			authoredRuleFixturePath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fixture authoredSimFixture
	if err := dec.Decode(&fixture); err != nil {
		t.Fatalf("decode %s: %v (a field added on the producing side must be mirrored here, "+
			"not ignored)", authoredRuleFixturePath, err)
	}
	// Negative control: an empty fixture would make every loop below pass without
	// validating a single rule — green over nothing, which is the exact failure this
	// whole arrangement exists to prevent.
	if len(fixture.Rules) == 0 {
		t.Fatal("the fixture declares no rules, so every check here is vacuous")
	}
	return fixture
}

// loadAuthoredSimRules is the rules-only view, which is what most checks here want.
func loadAuthoredSimRules(t *testing.T) []authoredRule {
	t.Helper()
	return loadAuthoredSimFixture(t).Rules
}

// validateAuthored submits definitions through the real gate, anchored by rule token
// exactly as device-management's publish path does.
func validateAuthored(t *testing.T, rules []authoredRule) *DetectionRuleValidationResultResolver {
	t.Helper()
	inputs := make([]detectionRuleInput, 0, len(rules))
	for _, r := range rules {
		inputs = append(inputs, detectionRuleInput{
			Token: r.RuleToken, Definition: r.Definition, GroupScoped: r.GroupScoped})
	}
	res, err := (&SchemaResolver{}).ValidateDetectionRules(
		authedContext(auth.DeviceRead), struct{ Rules []detectionRuleInput }{Rules: inputs})
	if err != nil {
		t.Fatalf("the validation gate itself errored: %v", err)
	}
	return res
}

// TestAuthoredSimRulesPassThePublishGate is the gate. Every rule the sim publishes must
// survive the ADR-044 validation device-management runs at profile publish — because a
// rule that does not is one the sim provisions, reports as provisioned, and that then
// never fires, leaving alarm widgets that look configured and stay empty forever.
func TestAuthoredSimRulesPassThePublishGate(t *testing.T) {
	authored := loadAuthoredSimRules(t)
	for _, r := range authored {
		// A fixture entry missing an identifying field would be submitted with an empty
		// token and fail for a reason that has nothing to do with the sim's rule.
		if r.Producer == "" || r.RuleToken == "" || r.ProfileToken == "" {
			t.Fatalf("fixture entry is missing an identifying field: %+v", r)
		}
		if strings.TrimSpace(r.Definition) == "" {
			t.Fatalf("fixture entry %s/%s carries an empty rule definition", r.Producer, r.RuleToken)
		}
		// A DISABLED rule is published UNCHECKED in production — the publish gate only
		// validates enabled rules — so a scenario shipping one has authored something
		// nothing validates on a real cluster. Legitimate, but it must be visible.
		if !r.Enabled {
			t.Logf("NOTE: %s/%s is authored DISABLED, so the real publish gate skips it and "+
				"this test is its only check", r.Producer, r.RuleToken)
		}
	}

	res := validateAuthored(t, authored)
	if !res.Valid() {
		byToken := map[string]string{}
		for _, r := range authored {
			byToken[r.RuleToken] = r.Producer
		}
		for _, e := range res.Errors() {
			t.Errorf("the sim publishes a rule the publish gate REJECTS: %s/%s: %s\n"+
				"With the gate wired the profile fails to publish and the scenario cannot "+
				"bootstrap at all; with it unwired the version publishes and the engine drops "+
				"the rule at load with only a log line — the alarm silently never fires.",
				byToken[e.Token()], e.Token(), e.Message())
		}
	}
}

// 🔴 The gate above is worth exactly what the validator rejects. If the rule reader ever
// stopped failing closed — a DisallowUnknownFields dropped, a switch to a plain
// json.Unmarshal — every fixture rule would still pass and the typo class this whole
// arrangement exists to catch would sail through green.
//
// So mutate the REAL fixture definition, not a hand-written stand-in: the mutation is
// applied to the same bytes the gate just accepted, or it proves something about a
// string literal in this file instead.
//
// Note the mutation targets `"threshold":` WITH ITS COLON. Replacing bare `"threshold"`
// looks equivalent and is not — `"type":"threshold"` carries the same quoted text as a
// VALUE, so the first replacement lands there, produces an unknown rule TYPE rather than
// an unknown FIELD, and tests a different rejection path entirely. It did exactly that
// on the first run of this file.
func TestThePublishGateStillRejectsAStrayKey(t *testing.T) {
	for _, r := range loadAuthoredSimRules(t) {
		const key = `"threshold":`
		if !strings.Contains(r.Definition, key) {
			continue
		}
		mutated := r
		mutated.Definition = strings.Replace(r.Definition, key, `"treshold":`, 1)
		if mutated.Definition == r.Definition {
			t.Fatalf("the mutation did not apply to %s/%s, so this proved nothing",
				r.Producer, r.RuleToken)
		}
		if res := validateAuthored(t, []authoredRule{mutated}); res.Valid() {
			t.Errorf("the publish gate ACCEPTED a rule with a misspelled threshold key "+
				"(%s/%s) — it is no longer failing closed on unknown fields, so "+
				"TestAuthoredSimRulesPassThePublishGate is now green over anything:\n%s",
				r.Producer, r.RuleToken, mutated.Definition)
		}
		return
	}
	t.Fatal("no authored rule carries a threshold key, so the stray-key control never ran " +
		"and the gate's fail-closed behaviour is unpinned")
}

// The sim's post-publish liveness assert accepts exactly one rule-health status and
// treats every other value as a failure. That single literal is the whole gate between a
// scenario whose rules are running and one whose rules were dropped at load — so if it
// stops naming a status this resolver can actually emit, the assert becomes one that can
// never be satisfied (a loud bootstrap failure) or, worse, is loosened by whoever hits it
// until it is satisfied by anything.
func TestAuthoredSimRuleStatusIsARealRuleHealthStatus(t *testing.T) {
	mirrored := loadAuthoredSimFixture(t).WireVocabulary.DetectionRuleStatus.Active
	// Negative control: an empty value compares equal to nothing.
	if mirrored == "" {
		t.Fatal("the fixture carries no detection-rule status, so this check has nothing to " +
			"hold to the resolver's vocabulary")
	}
	if mirrored != statusActive {
		t.Errorf("the sim requires rule status %q before it considers a scenario bootstrapped, "+
			"but this resolver emits %q for a healthy rule — the post-publish assert can never "+
			"be satisfied", mirrored, statusActive)
	}
	// And it must be the HEALTHY one. Naming the compile-error status here would make the
	// assert pass precisely when the rule is broken.
	if mirrored == statusCompileError {
		t.Errorf("the sim treats %q as its healthy status, but that is the COMPILE ERROR "+
			"status — the liveness assert would pass exactly when the rule does not run",
			mirrored)
	}
}
