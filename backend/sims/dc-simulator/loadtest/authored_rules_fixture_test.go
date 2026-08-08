// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
)

// ---- The authored-rule fixture -------------------------------------------------
//
// The sim hand-writes its DETECT rules as an untyped map[string]any and publishes the
// marshalled bytes over GraphQL as an opaque string. The thing that actually decodes
// those bytes is rules.Rule in event-processing, which decodes with
// DisallowUnknownFields — and the tier the raiseAlarm action lands at on the durable
// row is model.AlarmSeverity in device-management. dc-simulator can import NEITHER
// module (it is deliberately an untrusted external client of the platform, like a real
// device integration), so nothing in this module can hold the JSON to either grammar.
//
// A misspelled key is therefore free here: `treshold` publishes cleanly, the profile
// version goes active, and the rule never fires. ruleHealth catches it against a LIVE
// cluster; no CI gate catches it at all.
//
// So the two sides meet at a committed artifact, exactly as the dashboard fixtures do
// across the Go/TypeScript boundary: this side writes what it publishes, and a test in
// each consuming module reads it and holds it to that module's real authority —
// rules.Decode + rules.Compile in event-processing, model.AlarmSeverity in
// device-management.
//
// 🔴 THE FIXTURE IS THE PUBLISHED DOCUMENT, not a copy of it. Each entry's Definition
// is the exact byte string handed to createDetectionRule — read back out of
// Manifest().Profiles for widgetlab and out of ruleDefinitionJSON for the harness,
// which are the same values ensureDetectionRule / Provision send. A fixture assembled
// by a test from the same ingredients but by a different route would give a green gate
// over a different document, and the failure mode is not that the gate breaks — it is
// that it passes while the rule nobody can see is broken.
//
// 🔴 IT LIVES IN THIS PACKAGE, NOT IN sim/, AND THAT IS FORCED. The load-test harnesses
// author their rules directly (ensureDetectionRule, ensureCommandRule) rather than through
// a manifest, via unexported ruleDefinitionJSON methods; loadtest imports sim, so the
// reverse would cycle. A test in package loadtest is the only place EVERY published rule
// document is reachable.
//
// 🔴 AND IT COLLECTS THE CLASS, NOT A LIST. The first version of this file enumerated the
// two producers it happened to know about and missed the command harness sitting in this
// same package — leaving a rule publishable with the exact typo class this fixture exists
// to catch, every gate green. Scenarios now come from sim.Registry, harness rules from
// harnessRuleBuilders, and TestEveryHarnessRuleBuilderIsCollected parses this package to
// hold that list to the ruleDefinitionJSON methods that actually exist.

var updateRuleFixture = flag.Bool("update-rule-fixture", false,
	"rewrite the authored-rule fixture from the current rule builders")

// authoredRuleFixtureDir is shared ground: outside every module, so neither consumer
// reads the other's testdata, and named testdata so the Go tool skips it.
const authoredRuleFixtureDir = "../../../testdata/authored-rules"

const authoredRuleFixtureFile = "authored-rules.json"

// authoredRuleFixtureSeed is the seed the scenario walk builds at. Fixed so the fixture
// is reproducible — and safe to fix only because a rule definition does not vary with the
// seed, which TestAuthoredRulesDoNotVaryWithSeedOrConfig asserts rather than assumes.
const authoredRuleFixtureSeed = 1

// authoredRuleFixture is the committed artifact.
//
// Each consuming module declares its own PARTIAL mirror — typing only the section it owns
// and holding the rest as json.RawMessage — and every one decodes with
// DisallowUnknownFields. That duplication is the mechanism, not an accident of the module
// boundary, and it is why a shared fixture type (in backend/core, which all four modules
// already depend on) would be worse rather than merely unnecessary:
//
//   - It would DELETE THE TRIPWIRE. Today a field added here breaks all three consumer
//     decodes, forcing each owning module to judge it or consciously park it. With one
//     shared struct the producer edits core once and all three go green without any owning
//     module seeing the change — the silent pass this artifact exists to prevent.
//   - Shared CONSTANTS would go further and make the oracle SELF-REFERENTIAL: platform
//     compared against platform, green by construction. A wire-breaking rename would move
//     the sim and the services in lockstep, keeping the sim green while every real
//     external client — the frontend's generated types, the C# SDK, a user integration —
//     breaks. The committed file makes that rename a reviewable diff at the surface that
//     actually constitutes the compatibility contract.
type authoredRuleFixture struct {
	Rules []authoredRule `json:"rules"`
	// WireVocabulary is every enum value the sim MIRRORS from a service it cannot
	// import. None of it is part of a rule, but it is the same class of hazard from the
	// same cause: a rename on the owning side leaves these syntactically fine and
	// semantically dead, turning a query that should match into one that cannot. Since
	// each appears in a comparison whose non-matching branch is the PASSING one, the
	// result is a green oracle over a check that can no longer fail.
	//
	// It is grouped by OWNING MODULE because that is who can judge it: each group is
	// read by a test in the module that declares the real enum.
	WireVocabulary wireVocabulary `json:"wireVocabulary"`
}

// wireVocabulary is the mirrored-enum section. Adding a group here means adding a
// consumer test in the module that owns it — a group nobody reads is decoration.
type wireVocabulary struct {
	// AlarmState — device-management model.AlarmState (Alarm.state).
	// Read by device-management/model/authored_sim_alarm_wire_test.go.
	AlarmState alarmStateVocabulary `json:"alarmState"`
	// CommandStatus — command-delivery model.CommandStatus.
	// Read by command-delivery/model/authored_sim_command_wire_test.go.
	CommandStatus commandStatusVocabulary `json:"commandStatus"`
	// DetectionRuleStatus — event-processing's ruleHealth RuleStatus.
	// Read by event-processing/graphql/authored_sim_rules_test.go.
	DetectionRuleStatus detectionRuleStatusVocabulary `json:"detectionRuleStatus"`
}

type alarmStateVocabulary struct {
	Active  string `json:"active"`
	Cleared string `json:"cleared"`
}

// commandStatusVocabulary carries only the three the command harness actually compares
// against. It is deliberately NOT the whole CommandStatus enum: the fixture records what
// the sim mirrors, and claiming coverage of TIMEOUT/EXPIRED/FAILED — which the sim never
// spells — would assert a breadth nothing here has.
type commandStatusVocabulary struct {
	Queued     string `json:"queued"`
	Sent       string `json:"sent"`
	Successful string `json:"successful"`
}

// detectionRuleStatusVocabulary carries the one status a bootstrap post-assert accepts.
// The comparison is deliberately "== ACTIVE" rather than a list of known-bad statuses,
// so this single value is the whole gate between a live rule and a silently dropped one.
type detectionRuleStatusVocabulary struct {
	Active string `json:"active"`
}

type authoredRule struct {
	// Producer names the scenario/harness that authors this rule — the label a
	// consumer's failure message needs to send a reader to the right file.
	Producer string `json:"producer"`
	// RuleToken / ProfileToken are the entity tokens the rule is created under.
	RuleToken    string `json:"ruleToken"`
	ProfileToken string `json:"profileToken"`
	// Enabled mirrors the `enabled` flag the create request carries. It is
	// load-bearing context for a reader: publish-time validation gates only ENABLED
	// rules, so a disabled rule is published unchecked.
	Enabled bool `json:"enabled"`
	// AlarmSeverityWire is the tier this producer expects on the DURABLE alarm row —
	// the uppercase form the raise-alarm consumer produces from the rule's lowercase
	// authoring severity, and the form an alarm widget's severity filter is written
	// in. Mirrored as a literal on this side; device-management's fixture test holds
	// it to the real AlarmSeverity enum AND to the severity inside Definition.
	AlarmSeverityWire string `json:"alarmSeverityWire"`
	// Definition is the opaque rule JSON VERBATIM — the exact string published, not a
	// re-marshal of it. Kept as a string rather than an embedded object precisely so
	// it round-trips byte-for-byte: json.MarshalIndent reformats an embedded
	// json.RawMessage, which would quietly make the fixture a pretty-printed
	// paraphrase of the document instead of the document.
	Definition string `json:"definition"`
}

// harnessRuleBuilder is one detection rule the LOAD-TEST HARNESSES author directly
// (not through a manifest), so it cannot be discovered by walking sim.Registry.
//
// Receiver names the config type whose ruleDefinitionJSON method builds it. It is not
// decoration: TestEveryHarnessRuleBuilderIsCollected parses this package and fails if a
// ruleDefinitionJSON method exists whose receiver is not listed here — which is why this
// is a registered list rather than two inline appends (see the header note).
type harnessRuleBuilder struct {
	Receiver     string
	RuleToken    string
	ProfileToken string
	// Build is the definition as the fixture records it — from the ZERO config, so the
	// committed bytes are reproducible. BuildWithDefaults is the same definition from a
	// populated, defaulted config, which is what a real run actually publishes. The two
	// must agree; TestAuthoredRulesDoNotVaryWithSeedOrConfig is what says so, and the day
	// they stop agreeing the fixture has quietly become a sample of a family.
	Build             func() (string, error)
	BuildWithDefaults func() (string, error)
}

var harnessRuleBuilders = []harnessRuleBuilder{
	{
		Receiver:          "DetectionConfig",
		RuleToken:         HarnessRuleToken,
		ProfileToken:      HarnessProfileToken,
		Build:             DetectionConfig{}.ruleDefinitionJSON,
		BuildWithDefaults: DetectionConfig{Seed: 42}.withDefaults().ruleDefinitionJSON,
	},
	{
		Receiver:          "CommandConfig",
		RuleToken:         HarnessCommandRuleToken,
		ProfileToken:      HarnessCommandProfileToken,
		Build:             CommandConfig{}.ruleDefinitionJSON,
		BuildWithDefaults: CommandConfig{Seed: 42}.withDefaults().ruleDefinitionJSON,
	},
}

// mirroredAlarmSeverityWire maps a producer to the WIRE-form alarm severity constant it
// mirrors — the uppercase tier a raise lands at on the durable row, which the sim copies
// by hand because it cannot import device-management.
//
// A producer whose rule raises an alarm MUST have an entry, and buildAuthoredRules
// refuses to write a fixture otherwise. That is deliberate pressure: a new scenario
// authoring a raiseAlarm rule has to state the tier it expects on the wire, and stating
// it is what puts it in front of device-management's gate. Without the refusal, such a
// scenario would simply carry an empty mirror and be waved through.
var mirroredAlarmSeverityWire = map[string]string{
	"buildingpulse": sim.BuildingpulseAlarmSeverityWire,
	"widgetlab":     sim.WidgetlabAlarmSeverityWire,
	"sitepulse":     sim.SitepulseAlarmSeverityWire,
	"loadtest":      harnessAlarmSeverityWire,
}

// raisesAlarm reports whether a published definition carries a raiseAlarm action. The
// decode is deliberately lenient: the rule GRAMMAR is event-processing's authority and is
// gated there against the real publish gate, so a strict decode here would be a second,
// weaker copy of that check.
func raisesAlarm(t *testing.T, producer, ruleToken, definition string) bool {
	t.Helper()
	var rule struct {
		Actions []struct {
			Type string `json:"type"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(definition), &rule); err != nil {
		t.Fatalf("%s/%s: the definition about to be published is not even valid JSON: %v",
			producer, ruleToken, err)
	}
	for _, a := range rule.Actions {
		if a.Type == "raiseAlarm" {
			return true
		}
	}
	return false
}

// forEachScenarioRule visits every detection rule declared by every REGISTERED scenario
// at the given seed. Registry is what main.go resolves a handshake's manifestId against,
// so it is exactly the set of scenarios a real bring-up can run — which is why the walk
// is over the registry and not over a list of scenario names.
func forEachScenarioRule(seed int64, visit func(manifestId, profileToken string, r sim.DetectionRuleSpec)) {
	for _, manifestId := range sim.ManifestIds() {
		for _, p := range sim.Registry[manifestId](seed, sim.Load{}).Manifest().Profiles {
			for _, r := range p.DetectionRules {
				visit(manifestId, p.Token, r)
			}
		}
	}
}

// buildAuthoredRules collects every DETECT rule this module publishes, from the same
// values the provisioning paths read.
//
// 🔴 IT COLLECTS THE CLASS, NOT THE INSTANCES IT KNOWS ABOUT (see the header note).
// Scenarios come from sim.Registry, so a rule added to any registered scenario — or a
// fourth scenario — is picked up with no edit here; harness rules come from
// harnessRuleBuilders, which a source scan holds to the package's actual
// ruleDefinitionJSON methods.
func buildAuthoredRules(t *testing.T) []authoredRule {
	t.Helper()
	var out []authoredRule

	add := func(producer, ruleToken, profileToken string, enabled bool, definition string) {
		wire := mirroredAlarmSeverityWire[producer]
		if raisesAlarm(t, producer, ruleToken, definition) {
			if wire == "" {
				t.Fatalf("%s/%s raises an alarm but %q registers no mirrored wire severity — "+
					"the tier its alarm lands at would be checked by nothing. Add an entry to "+
					"mirroredAlarmSeverityWire naming the constant the scenario mirrors.",
					producer, ruleToken, producer)
			}
		} else {
			// A rule that raises no alarm has no tier to mirror. Blanking it here keeps
			// the fixture from asserting a severity for an alarm that is never raised —
			// device-management refuses a non-empty mirror on such a rule for the same
			// reason.
			wire = ""
		}
		out = append(out, authoredRule{
			Producer:          producer,
			RuleToken:         ruleToken,
			ProfileToken:      profileToken,
			Enabled:           enabled,
			AlarmSeverityWire: wire,
			Definition:        definition,
		})
	}

	// Every REGISTERED scenario, not just the one that happens to declare rules today.
	forEachScenarioRule(authoredRuleFixtureSeed,
		func(manifestId, profileToken string, r sim.DetectionRuleSpec) {
			add(manifestId, r.Token, profileToken, r.Enabled, r.Definition)
		})

	// The load-test harnesses, which author their rules directly rather than through a
	// manifest — which is why they cannot come from the walk above.
	for _, b := range harnessRuleBuilders {
		def, err := b.Build()
		if err != nil {
			t.Fatalf("build the %s harness rule definition: %v", b.Receiver, err)
		}
		// enabled:true is the literal both ensureDetectionRule and ensureCommandRule
		// send in their create request.
		add("loadtest", b.RuleToken, b.ProfileToken, true, def)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Producer != out[j].Producer {
			return out[i].Producer < out[j].Producer
		}
		return out[i].RuleToken < out[j].RuleToken
	})

	// Negative control for everything below. An empty (or one-sided) set would let
	// every consumer's loop pass without decoding a single rule, which is the same
	// green-over-nothing failure the dashboard fixtures guard against.
	if len(out) == 0 {
		t.Fatal("collected no authored rules, so the fixture would be empty and every check " +
			"over it in event-processing and device-management vacuous")
	}
	return out
}

// TestEveryHarnessRuleBuilderIsCollected is what makes harnessRuleBuilders a description
// of this package rather than a list of what someone remembered.
//
// The harnesses author rules directly instead of through a manifest, so no walk can find
// them (see the header note for what that cost the first time). The convention they
// follow is a `ruleDefinitionJSON` method on a config type, so that is what is checked:
// every such method declared in this package must have its receiver registered.
//
// go/parser rather than a regex, deliberately. A regex over source cannot tell code from
// prose, so the same text sitting in a comment satisfies it — a gate this repo has
// already watched come back to life as documentation.
func TestEveryHarnessRuleBuilderIsCollected(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse this package: %v", err)
	}

	declared := map[string]bool{}
	for name, pkg := range pkgs {
		if name != "loadtest" {
			continue
		}
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != ruleBuilderMethodName || fn.Recv == nil ||
					len(fn.Recv.List) == 0 {
					continue
				}
				declared[receiverTypeName(fn.Recv.List[0].Type)] = true
			}
		}
	}

	// Negative control: a scan that matched nothing would make the membership loop below
	// pass for any list at all, including an empty one.
	if len(declared) == 0 {
		t.Fatalf("found no %s methods in this package — the scan has rotted (renamed "+
			"convention? moved file?) and this test would accept any harness list",
			ruleBuilderMethodName)
	}

	registered := map[string]bool{}
	for _, b := range harnessRuleBuilders {
		registered[b.Receiver] = true
	}
	for receiver := range declared {
		if !registered[receiver] {
			t.Errorf("(%s).%s builds a detection rule that is published to a real cluster, "+
				"but it is not in harnessRuleBuilders — so it reaches no fixture and no gate "+
				"ever decodes it. Add it.", receiver, ruleBuilderMethodName)
		}
	}
	// And the reverse: a registered receiver whose method is gone means the list is
	// describing a builder that no longer exists, and the fixture carries a fossil rule.
	for receiver := range registered {
		if !declared[receiver] {
			t.Errorf("harnessRuleBuilders registers %q, but this package declares no "+
				"(%s).%s — the fixture carries a rule nothing publishes any more",
				receiver, receiver, ruleBuilderMethodName)
		}
	}
}

// ruleBuilderMethodName is the convention the harness rule builders follow.
const ruleBuilderMethodName = "ruleDefinitionJSON"

// receiverTypeName renders a method receiver's type name, unwrapping a pointer receiver
// so `func (c *X)` and `func (c X)` both report "X".
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func authoredRuleFixturePath() string {
	return filepath.Join(authoredRuleFixtureDir, authoredRuleFixtureFile)
}

func renderAuthoredRuleFixture(t *testing.T, rules []authoredRule) []byte {
	t.Helper()
	fixture := authoredRuleFixture{
		Rules: rules,
		WireVocabulary: wireVocabulary{
			AlarmState: alarmStateVocabulary{
				Active:  alarmStateActive,
				Cleared: alarmStateCleared,
			},
			CommandStatus: commandStatusVocabulary{
				Queued:     cmdStatusQueued,
				Sent:       cmdStatusSent,
				Successful: cmdStatusSuccessful,
			},
			DetectionRuleStatus: detectionRuleStatusVocabulary{
				Active: sim.RuleStatusActiveWire,
			},
		},
	}
	// 🔴 A BLANK VALUE MUST NOT REACH THE FILE. An empty string compares equal to
	// nothing, so a consumer looping over the group would have nothing to hold to its
	// enum and would pass — the vacuous green this whole artifact exists to prevent.
	// Every consumer refuses an empty value too; this refuses to write one at all.
	//
	// Walked by reflection rather than enumerated, for the same reason the rules are
	// collected by class: a hand-written list of the six values we have today is a list
	// someone must remember to extend, and a new vocabulary group added without its line
	// here would be written blank and gated by nothing.
	forEachVocabularyValue(t, fixture.WireVocabulary, func(name, value string) {
		if value == "" {
			t.Fatalf("wire vocabulary %s is empty, so the module that owns it would have "+
				"nothing to hold to its real enum", name)
		}
	})
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("render the fixture: %v", err)
	}
	return append(raw, '\n')
}

// forEachVocabularyValue visits every leaf string in the wire-vocabulary section, named
// "group.field" from the JSON tags. Reflection is worth it here precisely because the
// alternative is a list that has to be kept in step by hand — the shape this file exists
// to argue against. It PANICS on a non-struct/non-string field rather than skipping it,
// so a vocabulary group added in some other shape is a loud failure and not a silent gap
// in the walk.
func forEachVocabularyValue(t *testing.T, v wireVocabulary, visit func(name, value string)) {
	t.Helper()
	groups := reflect.ValueOf(v)
	groupTypes := groups.Type()
	if groups.NumField() == 0 {
		t.Fatal("the wire vocabulary declares no groups, so this walk visits nothing")
	}
	visited := 0
	for i := 0; i < groups.NumField(); i++ {
		group, groupName := groups.Field(i), jsonFieldName(groupTypes.Field(i))
		if group.Kind() != reflect.Struct {
			t.Fatalf("wire vocabulary group %q is a %s, not a struct of strings — the walk "+
				"cannot see inside it", groupName, group.Kind())
		}
		for j := 0; j < group.NumField(); j++ {
			field := group.Field(j)
			if field.Kind() != reflect.String {
				t.Fatalf("wire vocabulary %s.%s is a %s, not a string",
					groupName, jsonFieldName(group.Type().Field(j)), field.Kind())
			}
			visited++
			visit(groupName+"."+jsonFieldName(group.Type().Field(j)), field.String())
		}
	}
	if visited == 0 {
		t.Fatal("the wire vocabulary has groups but no values, so this walk asserted nothing")
	}
}

// jsonFieldName is the field's JSON tag name, falling back to the Go name.
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	if comma := strings.Index(tag, ","); comma >= 0 {
		tag = tag[:comma]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

// TestAuthoredRuleFixtureIsCurrent is the staleness gate: the committed fixture must be
// what the builders produce right now. Without it the consuming modules go on decoding
// and compiling a rule the sim stopped publishing, every assertion passing.
//
// Regenerate with:
//
//	go test ./loadtest/ -run TestAuthoredRuleFixtureIsCurrent -update-rule-fixture
//
// 🔴 Regenerating is step one of two. The consumers are what actually judge the new
// bytes, and they live in other modules — so a regenerate-and-move-on leaves the real
// verdict unread until the full sweep or CI runs event-processing and
// device-management. That is the intended flow, not a gap; do not read a green run
// HERE as "the rule is valid".
func TestAuthoredRuleFixtureIsCurrent(t *testing.T) {
	want := renderAuthoredRuleFixture(t, buildAuthoredRules(t))
	path := authoredRuleFixturePath()

	if *updateRuleFixture {
		if err := os.MkdirAll(authoredRuleFixtureDir, 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRegenerate with: go test ./loadtest/ -run "+
			"TestAuthoredRuleFixtureIsCurrent -update-rule-fixture", path, err)
	}
	// Compared as BYTES, unlike the dashboard fixtures. There the committed file is
	// indented and the published document is not, so a byte compare would fail over
	// formatting that means nothing. Here the published document is a STRING FIELD
	// inside the file, so the bytes of the thing under test survive indentation
	// exactly — and a byte compare additionally pins the escaping, which a value
	// compare would normalise away.
	if string(got) != string(want) {
		t.Errorf("%s is stale — the rule builders now publish something different.\n"+
			"Regenerate with: go test ./loadtest/ -run TestAuthoredRuleFixtureIsCurrent "+
			"-update-rule-fixture\nThen run the event-processing and device-management "+
			"tests, which are what actually judge the new rule.", path)
	}
}

// The fixture is generated at ONE seed and ONE config. That is only sound while the
// published rule depends on neither — otherwise the committed bytes are a sample of a
// family, and the consumers gate the sample while every other member ships unchecked.
//
// Both of these hold today by construction (the definitions are built from constants),
// which is exactly the kind of fact that stops holding quietly.
// scenarioRuleDefinitions returns every registered scenario's authored rule definitions
// at one seed, keyed "manifestId/profileToken/ruleToken". It goes through the SAME walk
// buildAuthoredRules uses, so the two literally cannot disagree about which rules a
// scenario declares — a second loop of the same shape would be one more thing to keep in
// step, in the file that argues against exactly that.
func scenarioRuleDefinitions(seed int64) map[string]string {
	out := map[string]string{}
	forEachScenarioRule(seed, func(manifestId, profileToken string, r sim.DetectionRuleSpec) {
		out[manifestId+"/"+profileToken+"/"+r.Token] = r.Definition
	})
	return out
}

func TestAuthoredRulesDoNotVaryWithSeedOrConfig(t *testing.T) {
	// Every REGISTERED scenario, not just the one that declares rules today — a fourth
	// scenario with a seed-dependent rule would otherwise be pinned at one seed and ship
	// every other seed ungated. Seed is the only axis: the composed-fixture scenarios
	// PANIC on a load override (their boards bind named devices), so there is no load
	// axis to vary here.
	first := scenarioRuleDefinitions(authoredRuleFixtureSeed)
	second := scenarioRuleDefinitions(20260802)
	if len(first) == 0 {
		t.Fatal("no registered scenario declares a detection rule, so the seed check " +
			"asserted nothing")
	}
	if len(first) != len(second) {
		t.Fatalf("two seeds produced %d and %d scenario rules", len(first), len(second))
	}
	for key, definition := range first {
		other, ok := second[key]
		if !ok {
			t.Errorf("rule %s exists at one seed and not another", key)
			continue
		}
		if definition != other {
			t.Errorf("rule %s differs between seeds, so the fixture pins one seed's rule "+
				"and every other seed publishes something ungated:\n %s\n %s",
				key, definition, other)
		}
	}

	// The harness rules are built from a config; ensureDetectionRule/ensureCommandRule
	// pass one that has been through withDefaults(). If a definition ever starts reading
	// a config field, the zero-config fixture stops describing what a real run publishes.
	// Checked for EVERY registered builder, not just the detection one.
	for _, b := range harnessRuleBuilders {
		zero, err := b.Build()
		if err != nil {
			t.Fatalf("%s zero config: %v", b.Receiver, err)
		}
		populated, err := b.BuildWithDefaults()
		if err != nil {
			t.Fatalf("%s populated config: %v", b.Receiver, err)
		}
		if zero != populated {
			t.Errorf("the %s harness rule now varies with its config, so the fixture pins "+
				"the zero-config form while a real run publishes another:\n %s\n %s",
				b.Receiver, zero, populated)
		}
	}
}

// The harness rules compare a metric against a threshold, and the probes emit values
// chosen to sit on either side of it. Nothing in CI held those numbers in that relation.
//
// Getting it wrong is quiet in the worst way: raise the threshold above the "above" probe
// value and no probe ever crosses, so the edge probes never alarm — which the harness
// reports as a liveness FAILURE only because it waits for an alarm that never comes. The
// no-spurious-alarm invariant, meanwhile, becomes trivially satisfiable, since nothing can
// alarm at all. A gate whose passing branch is "we saw no alarm" must not be able to reach
// that state by making alarms impossible.
//
// `gt` is strict, so a value AT the threshold is a non-match — the assertions are strict
// inequalities on both sides, not >= / <=.
func TestHarnessProbeValuesStraddleTheRuleThreshold(t *testing.T) {
	if !(harnessBelowValue < harnessThreshold) {
		t.Errorf("the below-threshold probe emits %v, which is not strictly below the rule's "+
			"threshold %v — it would alarm, and the no-spurious-alarm invariant would fail "+
			"for a reason that is not a platform defect",
			harnessBelowValue, harnessThreshold)
	}
	if !(harnessAboveValue > harnessThreshold) {
		t.Errorf("the above-threshold probe emits %v, which is not strictly above the rule's "+
			"threshold %v — no probe ever crosses, so no alarm is ever raised and the "+
			"safety invariant is satisfied by the detection path being inert",
			harnessAboveValue, harnessThreshold)
	}
	// That the PUBLISHED rule compares against harnessThreshold — rather than the probes
	// straddling a constant the rule does not use — is already pinned next door by
	// TestRuleDefinitionJSON in detection_test.go, in this same package.
}
