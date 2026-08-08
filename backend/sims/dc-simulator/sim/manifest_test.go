// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"reflect"
	"strings"
	"testing"
)

func testManifest() SimManifest {
	return SimManifest{
		Name: "test",
		Seed: 42,
		Profiles: []ProfileSpec{
			{Token: "test-profile", Name: "Test Profile", Category: "test",
				Metrics: []MetricSpec{{Key: "speed_kph", Name: "Speed", DataType: "DOUBLE", Unit: "kph"}}},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: "test-vehicle", Name: "Test Vehicle", ProfileToken: "test-profile"},
		},
		Populations: []PopulationSpec{
			{OfType: "test-vehicle", Count: 3, TokenPattern: "car-{n:05d}", ExternalIdPattern: "VIN-{n:05d}"},
		},
	}
}

// TestExpandDeterministic verifies the ADR-050 hard requirement: the same
// (manifest, seed) always renders identical tokens/externalIds/credentials —
// this is what makes bootstrap idempotent and reset safe to re-run.
func TestExpandDeterministic(t *testing.T) {
	m := testManifest()

	first := m.Expand(m.Seed)
	second := m.Expand(m.Seed)

	if len(first) != 3 {
		t.Fatalf("expected 3 expanded devices, got %d", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("expansion size differs across runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		// DeviceInstance now carries a []Assignment field, so it is no longer
		// comparable with == — reflect.DeepEqual is the direct equivalent.
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Fatalf("device %d differs across runs with the same seed: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// TestExpandTokenRendering checks the token/externalId pattern rendering
// itself (zero-padded index substitution).
func TestExpandTokenRendering(t *testing.T) {
	m := testManifest()
	devices := m.Expand(m.Seed)

	want := []struct {
		token      string
		externalId string
	}{
		{"car-00001", "VIN-00001"},
		{"car-00002", "VIN-00002"},
		{"car-00003", "VIN-00003"},
	}
	for i, w := range want {
		if devices[i].Token != w.token {
			t.Errorf("device %d token = %q, want %q", i, devices[i].Token, w.token)
		}
		if devices[i].ExternalId != w.externalId {
			t.Errorf("device %d externalId = %q, want %q", i, devices[i].ExternalId, w.externalId)
		}
		if devices[i].DeviceTypeToken != "test-vehicle" {
			t.Errorf("device %d deviceTypeToken = %q, want %q", i, devices[i].DeviceTypeToken, "test-vehicle")
		}
	}
}

// TestExpandDifferentSeedsDivergeCredentials checks that credential material
// (which has no pattern of its own) still varies with the seed, even though
// the pattern-derived token/externalId do not.
func TestExpandDifferentSeedsDivergeCredentials(t *testing.T) {
	m := testManifest()
	m.Seed = 1
	a := m.Expand(1)
	b := m.Expand(2)

	if a[0].Token != b[0].Token {
		t.Fatalf("token should be seed-independent (pure pattern formatting): %q vs %q", a[0].Token, b[0].Token)
	}
	if a[0].CredentialId == b[0].CredentialId {
		t.Fatalf("credential id should differ across seeds, got the same value %q", a[0].CredentialId)
	}
}

// TestManifestValidate exercises the manifest-shape checks Provision relies on
// to fail fast before any network call.
func TestManifestValidate(t *testing.T) {
	valid := testManifest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid manifest to pass, got: %v", err)
	}

	badRef := testManifest()
	badRef.Populations[0].OfType = "unknown-type"
	if err := badRef.Validate(); err == nil {
		t.Fatal("expected validation error for population referencing unknown device type")
	}

	badToken := testManifest()
	badToken.Profiles[0].Token = "bad token with spaces"
	if err := badToken.Validate(); err == nil {
		t.Fatal("expected validation error for grammar-unsafe profile token")
	}
}

// TestDevicepulseManifestShape checks the slice-1 built-in scenario matches
// the spec: exactly one population with Count 1, one profile with one numeric
// metric, one device type.
func TestDevicepulseManifestShape(t *testing.T) {
	m := NewDevicepulse(1, Load{}).Manifest()

	if len(m.Populations) != 1 || m.Populations[0].Count != 1 {
		t.Fatalf("expected exactly one population with Count 1, got %+v", m.Populations)
	}
	if len(m.Profiles) != 1 || len(m.Profiles[0].Metrics) != 1 {
		t.Fatalf("expected exactly one profile with one metric, got %+v", m.Profiles)
	}
	if len(m.DeviceTypes) != 1 {
		t.Fatalf("expected exactly one device type, got %+v", m.DeviceTypes)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("devicepulse manifest failed validation: %v", err)
	}

	devices := m.Expand(m.Seed)
	if len(devices) != 1 {
		t.Fatalf("expected exactly one expanded device, got %d", len(devices))
	}
	if len(devices[0].Assignments) != 0 {
		t.Fatalf("devicepulse declares no areas/customers, expected no assignments, got %+v", devices[0].Assignments)
	}
}

// manifestWithAssignments extends testManifest() with an area hierarchy (2
// areas) and a customer, and turns on distributeAcross:"area" on the one
// population — the shape TestExpand*Assignments* and TestValidate* below
// exercise.
func manifestWithAssignments() SimManifest {
	m := testManifest()
	m.AreaTypes = []AreaTypeSpec{{Token: "test-area-type", Name: "Test Area Type"}}
	m.Areas = []AreaSpec{
		{Token: "area-01", Name: "Area 1", AreaTypeToken: "test-area-type"},
		{Token: "area-02", Name: "Area 2", AreaTypeToken: "test-area-type"},
	}
	m.CustomerTypes = []CustomerTypeSpec{{Token: "test-customer-type", Name: "Test Customer Type"}}
	m.Customers = []CustomerSpec{{Token: "cust-01", Name: "Customer 1", CustomerTypeToken: "test-customer-type"}}
	m.Populations[0].DistributeAcross = []string{"area"}
	return m
}

// TestExpandAssignmentsRoundRobinAreaAndFixedCustomer checks Expand's
// assignment rendering: distributeAcross:"area" round-robins devices across
// SimManifest.Areas by (n-1) mod len(areas), and every device additionally
// gets a fixed assignment to the manifest's one customer — both with the
// "assign-<deviceToken>-<targetToken>" relationship token the spec mandates.
func TestExpandAssignmentsRoundRobinAreaAndFixedCustomer(t *testing.T) {
	m := manifestWithAssignments()
	devices := m.Expand(m.Seed)

	wantArea := []string{"area-01", "area-02", "area-01"} // (n-1) mod 2 for n=1,2,3
	if len(devices) != len(wantArea) {
		t.Fatalf("expected %d devices, got %d", len(wantArea), len(devices))
	}

	for i, d := range devices {
		if len(d.Assignments) != 2 {
			t.Fatalf("device %d: expected 2 assignments (area+customer), got %d: %+v", i, len(d.Assignments), d.Assignments)
		}
		var gotArea, gotCustomer *Assignment
		for j := range d.Assignments {
			switch d.Assignments[j].TargetType {
			case "area":
				gotArea = &d.Assignments[j]
			case "customer":
				gotCustomer = &d.Assignments[j]
			}
		}
		if gotArea == nil {
			t.Fatalf("device %d: no area assignment rendered", i)
		}
		if gotArea.TargetToken != wantArea[i] {
			t.Errorf("device %d area assignment target = %q, want %q", i, gotArea.TargetToken, wantArea[i])
		}
		wantAreaRelToken := "assign-" + d.Token + "-" + wantArea[i]
		if gotArea.RelationshipToken != wantAreaRelToken {
			t.Errorf("device %d area relationship token = %q, want %q", i, gotArea.RelationshipToken, wantAreaRelToken)
		}

		if gotCustomer == nil {
			t.Fatalf("device %d: no customer assignment rendered", i)
		}
		if gotCustomer.TargetToken != "cust-01" {
			t.Errorf("device %d customer assignment target = %q, want %q", i, gotCustomer.TargetToken, "cust-01")
		}
		wantCustRelToken := "assign-" + d.Token + "-cust-01"
		if gotCustomer.RelationshipToken != wantCustRelToken {
			t.Errorf("device %d customer relationship token = %q, want %q", i, gotCustomer.RelationshipToken, wantCustRelToken)
		}
	}
}

// TestExpandAssignmentsDeterministic re-checks the ADR-050 determinism
// requirement specifically for the newly-rendered Assignment set (not just
// token/externalId/credential, which TestExpandDeterministic already covers).
func TestExpandAssignmentsDeterministic(t *testing.T) {
	m := manifestWithAssignments()
	first := m.Expand(m.Seed)
	second := m.Expand(m.Seed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("assignments differ across runs with the same seed:\n%+v\nvs\n%+v", first, second)
	}
}

// TestValidateRejectsDistributeAcrossAreaWithNoAreas exercises the specific
// failure the spec calls out: distributeAcross:["area"] declared on a
// population whose manifest has no areas at all must fail fast, not silently
// render zero assignments.
func TestValidateRejectsDistributeAcrossAreaWithNoAreas(t *testing.T) {
	m := testManifest()
	m.Populations[0].DistributeAcross = []string{"area"}
	// Deliberately no AreaTypes/Areas declared.
	if err := m.Validate(); err == nil {
		t.Fatal(`expected validation error for distributeAcross:["area"] with no areas declared`)
	}
}

// TestValidateRejectsUnsupportedDistributeAcross checks the fail-closed
// handling of any distributeAcross value other than "area" (customer/asset
// spreading is a documented, deliberately unbuilt extension — Validate should
// reject it outright rather than silently ignoring it).
func TestValidateRejectsUnsupportedDistributeAcross(t *testing.T) {
	m := manifestWithAssignments()
	m.Populations[0].DistributeAcross = []string{"customer"}
	if err := m.Validate(); err == nil {
		t.Fatal(`expected validation error for unsupported distributeAcross value "customer"`)
	}
}

// TestValidateRejectsAssignmentToMissingTarget exercises validateAssignments
// directly (see its doc comment for why: Expand never produces a dangling
// reference on its own, so this is the only way to prove the check itself —
// not just its unreachable-in-practice call site — actually rejects one).
func TestValidateRejectsAssignmentToMissingTarget(t *testing.T) {
	d := DeviceInstance{
		Token: "device-01",
		Assignments: []Assignment{
			{TargetType: "area", TargetToken: "no-such-area", RelationshipToken: "assign-device-01-no-such-area"},
		},
	}
	err := validateAssignments(d, map[string]bool{}, map[string]bool{}, map[string]bool{})
	if err == nil {
		t.Fatal("expected validation error for an assignment referencing a missing area")
	}

	// Sanity check the positive case with the same helper, so the negative
	// case above is proven against real "it does accept a valid one" behavior
	// rather than a helper that just always errors.
	ok := validateAssignments(d, map[string]bool{"no-such-area": true}, map[string]bool{}, map[string]bool{})
	if ok != nil {
		t.Fatalf("expected no error once the target exists, got: %v", ok)
	}
}

// TestValidateRejectsUnsupportedAssignmentTargetType checks the entity-type
// registry gate itself (a "device" target — valid in the real registry, but
// out of scope for this slice's assignment targets).
func TestValidateRejectsUnsupportedAssignmentTargetType(t *testing.T) {
	d := DeviceInstance{
		Token: "device-01",
		Assignments: []Assignment{
			{TargetType: "device", TargetToken: "some-other-device", RelationshipToken: "assign-device-01-some-other-device"},
		},
	}
	if err := validateAssignments(d, map[string]bool{}, map[string]bool{}, map[string]bool{}); err == nil {
		t.Fatal(`expected validation error for unsupported assignment target type "device"`)
	}
}

// TestBuildingpulseManifestShape checks the slice-2 built-in scenario matches
// the spec table: 12 thermostats (3 buildings x 4/building), 3 areas, 3
// assets, one customer, one profile with 4 metrics, one dashboard — and that it
// passes Validate end to end.
func TestBuildingpulseManifestShape(t *testing.T) {
	m := NewBuildingpulse(1, Load{}).Manifest()

	if len(m.Populations) != 1 || m.Populations[0].Count != 12 {
		t.Fatalf("expected exactly one population with Count 12, got %+v", m.Populations)
	}
	if len(m.Areas) != 3 {
		t.Fatalf("expected 3 areas, got %d: %+v", len(m.Areas), m.Areas)
	}
	if len(m.Assets) != 3 {
		t.Fatalf("expected 3 assets, got %d: %+v", len(m.Assets), m.Assets)
	}
	if len(m.Customers) != 1 {
		t.Fatalf("expected 1 customer, got %d: %+v", len(m.Customers), m.Customers)
	}
	if len(m.Profiles) != 1 || len(m.Profiles[0].Metrics) != 4 {
		t.Fatalf("expected exactly one profile with 4 metrics, got %+v", m.Profiles)
	}
	if len(m.Dashboards) != 1 {
		t.Fatalf("expected exactly one dashboard, got %+v", m.Dashboards)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("buildingpulse manifest failed validation: %v", err)
	}

	devices := m.Expand(m.Seed)
	if len(devices) != 12 {
		t.Fatalf("expected 12 expanded devices, got %d", len(devices))
	}
	for i, d := range devices {
		if len(d.Assignments) != 2 {
			t.Errorf("device %d (%s): expected 2 assignments (area+customer), got %d: %+v",
				i, d.Token, len(d.Assignments), d.Assignments)
		}
	}
}

// TestRegistry checks the manifest-id -> Sim constructor lookup main.go relies
// on to pick a driver from the handshake's ManifestId.
// It enumerates the registry rather than a written-out list: the property being
// pinned — a scenario's registry KEY equals its manifest NAME — is one every
// scenario must have, and the hardcoded list had already gone stale by one when
// a third scenario landed, leaving it unchecked.
func TestRegistry(t *testing.T) {
	if len(ManifestIds()) < 2 {
		t.Fatalf("only %d scenarios registered; this test asserts a property across them "+
			"and would say very little about one", len(ManifestIds()))
	}
	for _, id := range ManifestIds() {
		ctor, ok := Registry[id]
		if !ok {
			t.Fatalf("expected manifest id %q to be registered", id)
		}
		s := ctor(1, Load{})
		if s == nil {
			t.Fatalf("constructor for %q returned a nil Sim", id)
		}
		if got := s.Manifest().Name; got != id {
			t.Errorf("manifest id %q constructor built a Sim named %q", id, got)
		}
	}
	if _, ok := Registry["no-such-manifest"]; ok {
		t.Fatal("expected an unknown manifest id to be absent from the registry")
	}
}

// ---- The command far-end MODE --------------------------------------------------

// The zero value has to resolve, and it has to resolve HERE rather than at each
// reader. A manifest that never mentions commands leaves the field "", which equals
// none of the mode constants — so every consumer comparing against them takes
// neither the none branch nor a far-end branch, and lands in whatever the code does
// when it matched nothing. That is the majority of manifests in this package, so the
// unhandled value would be the common case rather than the exotic one.
func TestFarEndModeNormalizesTheZeroValueToNone(t *testing.T) {
	m := testManifest()
	if m.CommandFarEnd != "" {
		t.Fatalf("the fixture sets CommandFarEnd %q, so this test is not about the zero value",
			m.CommandFarEnd)
	}
	if got := m.FarEndMode(); got != FarEndNone {
		t.Errorf("an omitted CommandFarEnd reads as %q, want %q", got, FarEndNone)
	}
	// And the accessor is not simply "return none": a set mode must survive it, or
	// normalization would erase the very distinction it exists to preserve.
	for _, mode := range []CommandFarEndMode{FarEndNone, FarEndInternal, FarEndExternal} {
		m.CommandFarEnd = mode
		if got := m.FarEndMode(); got != mode {
			t.Errorf("CommandFarEnd %q reads back as %q", mode, got)
		}
	}
}

// Fail closed on a mode this build does not know. A typo — "externl" — normalizes to
// itself, matches no branch anywhere, and leaves a scenario behaving as none while
// its manifest claims an external far end: no attach, and its command widget refused
// with an error about a mode it does not have. Validate is the only place that can
// catch it, because everything downstream reads the value through comparisons that
// all simply miss.
func TestValidateRejectsAnUnknownCommandFarEndMode(t *testing.T) {
	m := testManifest()
	m.CommandFarEnd = "externl"
	// 🔴 The command definition is here to keep this test HONEST, and it was missing
	// on the first draft: without it the far-end-vocabulary check fires first, and
	// its message quotes the mode too — so the test passed with the mode allow-list
	// deleted entirely. A mutation proved it. The typo must be caught AS a typo.
	m.Profiles[0].Commands = []CommandSpec{{Token: "test-cmd", CommandKey: "doThing"}}

	err := m.Validate()
	if err == nil {
		t.Fatal("a manifest with CommandFarEnd \"externl\" was accepted; it would run as though " +
			"it had no far end while claiming one")
	}
	if !strings.Contains(err.Error(), "externl") {
		t.Errorf("error %q does not quote the offending value, so a reader cannot see the typo", err)
	}
	if strings.Contains(err.Error(), "nothing for the far end to answer") {
		t.Errorf("error %q is the no-command-vocabulary rejection, not the unknown-mode one: "+
			"this test would pass with the mode check gone", err)
	}
	// The counterweight: rejecting garbage is only meaningful while every real mode
	// still passes. A check written as "reject anything unfamiliar" that also
	// rejected "external" would look identical from the test above.
	for _, mode := range []CommandFarEndMode{"", FarEndNone, FarEndInternal, FarEndExternal} {
		ok := testManifest()
		ok.CommandFarEnd = mode
		if mode != "" && mode != FarEndNone {
			ok.Profiles[0].Commands = []CommandSpec{{Token: "test-cmd", CommandKey: "doThing"}}
		}
		if err := ok.Validate(); err != nil {
			t.Errorf("CommandFarEnd %q was rejected: %v", mode, err)
		}
	}
}

// A far end with no command vocabulary is refused in BOTH modes, and external is the
// one that needed saying: there the CommandDefinitions are the entire contract with a
// client in another process. With none published there is nothing for that client to
// subscribe for and nothing for a board's widget to enqueue against, so the manifest
// declares a far end that could not be reached even with the Unity player running.
func TestValidateRequiresACommandDefinitionForEveryFarEndMode(t *testing.T) {
	for _, mode := range []CommandFarEndMode{FarEndInternal, FarEndExternal} {
		m := testManifest()
		m.CommandFarEnd = mode
		if len(m.Profiles[0].Commands) != 0 {
			t.Fatalf("the fixture already declares commands, so mode %q would pass for the "+
				"wrong reason", mode)
		}
		err := m.Validate()
		if err == nil {
			t.Fatalf("mode %q was accepted with no command definition anywhere", mode)
		}
		if !strings.Contains(err.Error(), "nothing for the far end to answer") {
			t.Errorf("mode %q: error %q is not the far-end-with-no-commands rejection", mode, err)
		}
	}
}

// ---- The sendCommand command-key cross-check ------------------------------------

// The fixture the command-key tests share. It carries TWO profiles, and both of the things
// that makes possible are load-bearing — a one-profile fixture cannot fail either way:
//
//   - The rule lives on the SECOND profile while the FIRST declares a command of its own.
//     With one profile, a manifest-wide lookup and a per-profile one are the same function,
//     so the scoping decision Validate argues for at length would be pinned by nothing.
//     Hoisting `commandKeys` above the profile loop must break this fixture.
//   - Each command's Token, CommandKey and Name are three DIFFERENT strings, so a probe can
//     name the wrong one of the three. That is the conflation the whole check exists for.
//     A fixture where they coincide would pass against a lookup that accepted all three.
//
// Command tokens share one namespace across the manifest (Validate rejects a repeat), so
// the two profiles' commands are deliberately distinct in every field.
//
// The rule is built through ThresholdAlarmRule rather than a hand-written JSON literal on
// purpose. A literal would let the fixture and the producer drift — the check would keep
// passing against a document shape nothing actually emits — which is the same
// measure-the-fixture failure the decode mirror in detectionrule_test.go warns about.
const (
	// Declared on the rule's OWN profile: the one key that must be accepted.
	ownCmdToken = "own-cmd"
	ownCmdKey   = "raiseBucket"
	ownCmdName  = "Raise Bucket"
	// Declared on the OTHER profile: a real, published, grammar-valid command key that
	// this rule still cannot reach.
	farCmdKey = "gotoRefuel"
)

func commandRuleManifest(commandKey string) SimManifest {
	m := testManifest()
	m.Profiles[0].Commands = []CommandSpec{
		{Token: "far-cmd", CommandKey: farCmdKey, Name: "Go Refuel"},
	}
	m.Profiles = append(m.Profiles, ProfileSpec{
		Token:    "other-profile",
		Name:     "Other Profile",
		Category: "test",
		Metrics:  []MetricSpec{{Key: "speed_kph", Name: "Speed", DataType: "DOUBLE", Unit: "kph"}},
		Commands: []CommandSpec{
			{Token: ownCmdToken, CommandKey: ownCmdKey, Name: ownCmdName},
		},
		DetectionRules: []DetectionRuleSpec{
			ThresholdAlarmRule{
				Token:      "test-rule",
				Name:       "Test rule",
				Metric:     "speed_kph",
				Op:         OpLt,
				Threshold:  15,
				Severity:   SeverityMajor,
				AlarmKey:   "test-alarm",
				CommandKey: commandKey,
				Enabled:    true,
			}.Spec(),
		},
	})
	return m
}

// 🔴 THE SEAM: nothing on either side of the wire compares a rule's sendCommand key against
// a command vocabulary. event-processing's compiler is state-free by design and checks only
// that the key is a grammar-valid token; this manifest otherwise treats Definition as
// opaque JSON. So "gotoRefue1" publishes clean, lands in the authored-rules fixture,
// compiles, and reports ACTIVE to the liveness assert — which proves the rule COMPILED and
// says nothing about whether it can act. It fails for the first time when the rule FIRES,
// as a dead-letter in another service with nothing naming the manifest line that wrote it.
//
// BOTH HALVES ARE HERE and neither is optional: a cross-check that rejects every key looks
// exactly like a working one from the typo half alone, and one that accepts every key looks
// exactly like a working one from the good half alone.
func TestValidateRejectsARuleSendingACommandTheProfileDoesNotDeclare(t *testing.T) {
	// Every string here is a way of being wrong that PUBLISHES CLEAN, and each one is a
	// different mistake — which is why one probe would not do. A lookup widened to accept
	// the Token, or to span the whole manifest, satisfies a single-probe test exactly as
	// well as the correct one does.
	for _, tc := range []struct {
		name string
		key  string
	}{
		// A transposed character. The classic typo, and the only one a careless
		// eyeballing catches.
		{"a typo'd key", "gotoRefue1"},
		// 🔴 A REAL, DECLARED, PUBLISHED command key — on the OTHER profile. This is the
		// case a manifest-wide lookup blesses, and it dead-letters identically to the
		// typo: the rule fires for devices carrying ITS profile, and the command goes to
		// the device the detection fired for, which has no such key in its vocabulary.
		{"a key declared only on another profile", farCmdKey},
		// 🔴 The CommandSpec's own Token, from the rule's own profile. Three different
		// strings sit on that struct and all three are grammar-valid tokens, so a lookup
		// that "helpfully" accepted the token too would take this and produce a rule that
		// compiles, reports ACTIVE and cannot act. This is the motivating conflation.
		{"the command's Token instead of its CommandKey", ownCmdToken},
		// The display Name, same profile — the other half of that conflation, and the one
		// an author copying from the console UI is most likely to reach for.
		{"the command's display Name instead of its CommandKey", ownCmdName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := commandRuleManifest(tc.key).Validate()
			if err == nil {
				t.Fatalf("a rule sending %q was accepted; it would publish, compile, report "+
					"%s, and dead-letter on its first firing", tc.key, RuleStatusActiveWire)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not quote the offending command key %q, so an author "+
					"cannot see which of the similar strings is wrong", err, tc.key)
			}
			if !strings.Contains(err.Error(), "test-rule") {
				t.Errorf("error %q does not name the rule, so an author cannot find the line", err)
			}
		})
	}

	// The good half. The key declared on the rule's OWN profile must pass, or every
	// manifest in the module is rejected and every rejection above reads identically.
	if err := commandRuleManifest(ownCmdKey).Validate(); err != nil {
		t.Errorf("a rule sending its own profile's declared command key %q was rejected: %v",
			ownCmdKey, err)
	}

	// And the check must not fire on a rule with NO sendCommand at all — the shape every
	// existing scenario publishes. A check that treated "no command action" as "an
	// undeclared command" would reject widgetlab and buildingpulse outright.
	none := commandRuleManifest("")
	for i := range none.Profiles {
		none.Profiles[i].Commands = nil
	}
	none.CommandFarEnd = FarEndNone
	if err := none.Validate(); err != nil {
		t.Errorf("a rule with no sendCommand action was rejected: %v", err)
	}
}

// The unit under the manifest check, exercised directly for the shapes Validate cannot
// easily hand it: a definition that does not parse, and action lists ThresholdAlarmRule
// cannot build — in particular the two REACT action types it does not render at all
// (httpCall, publish), which are what pin the discriminator match to == sendCommand rather
// than to != raiseAlarm.
//
// The non-parsing case must return NO OPINION rather than an error. json.Valid in Validate
// already owns malformed JSON, and a second complaint here would give one mistake two
// messages — the same reasoning ruleTopLevelMetric states for returning "".
func TestRuleSendCommandKeysReadsOnlySendCommandActions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition string
		want       []string
	}{
		{"not JSON at all", "{not json", nil},
		{"no actions key", `{"name":"r","type":"threshold"}`, nil},
		{"only a raiseAlarm", `{"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"a"}}]}`, nil},
		// 🔴 THE MATCH RUNS ON THE DISCRIMINATOR, AND IT MUST BE == sendCommand RATHER THAN
		// != raiseAlarm. rules.ActionType has FOUR values, not two; a chain containing an
		// httpCall or a publish is a well-formed rule that names no command at all. Written
		// the negative way, each of these yields "" as a command key and Validate then
		// rejects a perfectly good manifest for sending a command it never sent. That is
		// the loud direction, which is exactly why it needs a test — a wrong-but-loud check
		// still survives every mutation aimed at the silent one.
		{"an httpCall contributes nothing",
			`{"actions":[{"type":"httpCall","httpCall":{"url":"https://example.invalid/hook"}}]}`, nil},
		{"a publish contributes nothing",
			`{"actions":[{"type":"publish","publish":{"connectorRef":"conn-a"}}]}`, nil},
		{"all four action types, only the sendCommand counted",
			`{"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"a"}},` +
				`{"type":"httpCall","httpCall":{"url":"https://example.invalid/hook"}},` +
				`{"type":"sendCommand","sendCommand":{"command":"doThing"}},` +
				`{"type":"publish","publish":{"connectorRef":"conn-a"}}]}`,
			[]string{"doThing"}},
		{"one sendCommand", `{"actions":[{"type":"sendCommand","sendCommand":{"command":"doThing"}}]}`,
			[]string{"doThing"}},
		{"mixed chain, in either order",
			`{"actions":[{"type":"sendCommand","sendCommand":{"command":"a"}},` +
				`{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"k"}},` +
				`{"type":"sendCommand","sendCommand":{"command":"b"}}]}`,
			[]string{"a", "b"}},
		// An empty command is REPORTED, not skipped. Skipping it would make "a sendCommand
		// naming nothing" the one action shape this check has no opinion about, which is
		// the quietest place in the document for it to hide.
		{"a sendCommand naming nothing", `{"actions":[{"type":"sendCommand","sendCommand":{}}]}`,
			[]string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleSendCommandKeys(tc.definition); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ruleSendCommandKeys(%s) = %#v, want %#v", tc.definition, got, tc.want)
			}
		})
	}
}
