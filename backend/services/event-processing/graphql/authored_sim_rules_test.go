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
		// GroupScoped false: the sim has no entity-group-scope concept (sim.DetectionRuleSpec
		// declares no such field), so every rule it authors is unscoped and this matches what
		// device-management forwards. If the sim ever gains scoping, the producer gains the
		// field and this decode fails closed on the unknown key rather than quietly asking the
		// gate a laxer question than the real publish path does.
		inputs = append(inputs, detectionRuleInput{
			Token: r.RuleToken, Definition: r.Definition, GroupScoped: false})
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
		// NOTE on `enabled`: the real publish gate validates only ENABLED rules, so a
		// scenario shipping a disabled one has authored something nothing validates on a
		// real cluster. This test deliberately validates it anyway — stricter than
		// production, which is the safe direction — so the flag changes nothing here and
		// is recorded in the fixture for the reader rather than branched on.
	}

	res := validateAuthored(t, authored)
	if !res.Valid() {
		byToken := map[string]authoredRule{}
		for _, r := range authored {
			byToken[r.RuleToken] = r
		}
		for _, e := range res.Errors() {
			t.Errorf("the sim publishes a rule the publish gate REJECTS: %s (rule %s on "+
				"profile %s): %s\nWith the gate wired the profile fails to publish and the "+
				"scenario cannot bootstrap at all; with it unwired the version publishes and "+
				"the engine drops the rule at load with only a log line — the alarm silently "+
				"never fires.",
				byToken[e.Token()].Producer, e.Token(), byToken[e.Token()].ProfileToken,
				e.Message())
		}
	}
}

// 🔴 The gate above is worth exactly what the validator rejects. If it ever stopped
// rejecting a mistyped key, every fixture rule would still pass and the typo class this
// whole arrangement exists to catch would sail through green.
//
// So mutate the REAL fixture definition, not a hand-written stand-in: the mutation is
// applied to the same bytes the gate just accepted, or it proves something about a
// string literal in this file instead.
//
// 🔴 WHAT REJECTS A MISTYPED KEY IS TWO LAYERS, NOT ONE — measured, and this paragraph
// replaces one that named only the first. An earlier version said the control stood
// behind `DisallowUnknownFields` specifically, so that dropping it would turn this test
// green. Deleting `dec.DisallowUnknownFields()` from rules.Decode was tried, and the
// misspelling mutations below stayed RED: the strict decode rejects the unknown field,
// and failing that, the dropped field leaves a hole SEMANTIC validation rejects on its
// own (`when: a structured comparison needs a threshold…`, `actions: action N command:
// identifier "" is not valid…`).
//
// That is defence in depth and it is welcome, but it means neither misspelling mutation
// pins the decode by itself. The third entry in the table below exists to close that: it
// injects an unknown field rather than corrupting a known one, so there is no hole for
// semantics to find and the fail-closed decode is the only thing left that can say no.
// Anyone tempted to relax either layer on the grounds that "the other one covers it"
// should note that this file now measures both.
//
// 🔴 EVERY KEY IS TARGETED WITH ITS COLON, and that is not a style choice — it is the
// difference between testing the unknown-FIELD path and testing something else entirely.
// Replacing bare `"threshold"` looks equivalent and is not: `"type":"threshold"` carries
// the same quoted text as a VALUE, so the first replacement lands there and produces an
// unknown rule TYPE. It did exactly that on the first run of this file. The `survives`
// list below is how each mutation now PROVES it missed its own decoy rather than
// asserting it in a comment.
//
// 🔴 AND IT COVERS EVERY RULE, NOT THE FIRST ONE. This loop used to `return` after the
// first match, which was buildingpulse — so the two-action documents (sitepulse's
// low-fuel rule and the load-test command harness's) had NO committed fail-closed
// control at all, while the loop read exactly as though it covered them. A control that
// stops at the first subject is a control over one subject.
type strayKeyMutation struct {
	// what names the rule shape this mutation exercises, for the failure message.
	what string
	// key is the field key, WITH its colon; typo is the misspelling swapped in.
	key, typo string
	// survives are substrings that must be present BEFORE and UNCHANGED AFTER the
	// replacement — the decoys this mutation must not have hit. For the command key
	// those are the sendCommand discriminator in both of its spellings, since the
	// envelope repeats it as a key AND as a value and either would be the wrong target.
	survives []string
}

var strayKeyMutations = []strayKeyMutation{
	{
		what: "the leaf comparison's bound",
		key:  `"threshold":`, typo: `"treshold":`,
		survives: []string{`"type":"threshold"`},
	},
	{
		// The two-action shape. It reached the fixture with sitepulse and is carried by
		// the load-test command harness too; before this entry existed, neither had a
		// per-rule control and the only verdict the shape had ever had was a by-hand run.
		what: "sendCommand's command key",
		key:  `"command":`, typo: `"commnd":`,
		survives: []string{`"sendCommand":`, `"type":"sendCommand"`},
	},
	{
		// 🔴 THE ONLY ONE OF THE THREE THAT ISOLATES THE STRICT DECODE, and it is here
		// because MEASUREMENT contradicted this file's own long-standing claim. The header
		// above says that if the reader "stopped failing closed — a DisallowUnknownFields
		// dropped — every fixture rule would still pass". That was tested by deleting
		// `dec.DisallowUnknownFields()` from rules.Decode, and BOTH mutations above stayed
		// red, so the control did not do what it said.
		//
		// The reason is defence in depth that nobody had written down: a MISSPELLED key is
		// caught twice over. The strict decode rejects the unknown field; and if it did
		// not, dropping the field leaves a hole that SEMANTIC validation then rejects on
		// its own — measured as `when: a structured comparison needs a threshold or a
		// threshold attribute` and `actions: action N command: identifier "" is not valid`.
		// Good news for the platform, useless for a control that means to pin one layer.
		//
		// So this mutation INJECTS a wholly unknown field instead of misspelling a known
		// one. It has no semantic footprint — nothing downstream misses a value, because
		// nothing was taken away — which leaves the fail-closed decode as the only thing
		// that can reject it. Delete DisallowUnknownFields and THIS entry goes red alone.
		//
		// `{"actions":` is the anchor because the definitions are marshalled from a Go map,
		// so their keys are sorted and "actions" is first. If a rule ever gains a key
		// sorting ahead of it the anchor stops matching, and the counter control below
		// fails loudly rather than quietly exercising nothing.
		what: "a wholly unknown top-level field, which only the strict decode can reject",
		key:  `{"actions":`, typo: `{"strayField":true,"actions":`,
		survives: []string{`"actions":`, `"type":"threshold"`},
	},
}

func TestThePublishGateStillRejectsAStrayKey(t *testing.T) {
	rules := loadAuthoredSimRules(t)

	for _, m := range strayKeyMutations {
		mutated := 0
		for _, r := range rules {
			// Exactly one occurrence, or `strings.Replace(..., 1)` is choosing between
			// candidates. Two occurrences would leave the second one valid, so the document
			// might be rejected for a reason this test did not author — and the count is
			// cheap, so there is no reason to find out later.
			switch n := strings.Count(r.Definition, m.key); n {
			case 0:
				continue
			case 1:
			default:
				t.Fatalf("%s/%s carries %s %d times; a single-replacement mutation would be "+
					"ambiguous about which one it hit", r.Producer, r.RuleToken, m.key, n)
			}

			// The decoys must be present before the replacement, or "unchanged after" is a
			// claim about nothing.
			before := map[string]int{}
			for _, s := range m.survives {
				before[s] = strings.Count(r.Definition, s)
			}

			def := strings.Replace(r.Definition, m.key, m.typo, 1)
			// Prove the bytes moved, rather than trusting the replace.
			if def == r.Definition {
				t.Fatalf("the %s mutation did not change %s/%s, so it proved nothing",
					m.key, r.Producer, r.RuleToken)
			}
			if !strings.Contains(def, m.typo) {
				t.Fatalf("the %s mutation did not leave %s in %s/%s", m.key, m.typo, r.Producer, r.RuleToken)
			}
			// Prove it hit the FIELD KEY and not a decoy carrying the same text.
			for _, s := range m.survives {
				if got := strings.Count(def, s); got != before[s] {
					t.Fatalf("the %s mutation changed %q in %s/%s (%d -> %d): it landed on the "+
						"decoy rather than the field key, so this exercises a different "+
						"rejection path than the unknown-field one it claims to",
						m.key, s, r.Producer, r.RuleToken, before[s], got)
				}
			}
			mutated++

			if res := validateAuthored(t, []authoredRule{{
				Producer: r.Producer, RuleToken: r.RuleToken,
				ProfileToken: r.ProfileToken, Enabled: r.Enabled, Definition: def,
			}}); res.Valid() {
				// Worded for BOTH kinds this table now carries — two of the entries misspell
				// an existing key and the third injects one that was never there, and calling
				// the injection a "misspelling" would send a reader looking for a typo that
				// is not in the document.
				t.Errorf("the publish gate ACCEPTED a mutated rule: %s (%s -> %s, %s/%s) — it "+
					"is no longer failing closed, so TestAuthoredSimRulesPassThePublishGate is "+
					"now green over anything:\n%s",
					m.what, m.key, m.typo, r.Producer, r.RuleToken, def)
			}
		}

		// 🔴 THE COUNTER-BASED NEGATIVE CONTROL. A mutation whose key no longer appears in
		// any authored rule silently exercises nothing, and the loop above reports success
		// by having found nothing to complain about — the precise shape of vacuous green
		// this whole file exists to refuse. It has to say so about ITSELF.
		if mutated == 0 {
			t.Errorf("no authored rule carries %s (%s), so that stray-key control never ran "+
				"and the gate's fail-closed behaviour is unpinned for this rule shape. Either "+
				"the producer stopped emitting the key, or the fixture is stale.", m.key, m.what)
		}
		t.Logf("%s (%s): mutated and rejected %d of %d authored rules", m.key, m.what, mutated, len(rules))
	}
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
