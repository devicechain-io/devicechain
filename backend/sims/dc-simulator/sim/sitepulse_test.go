// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// ---- Scaffolding --------------------------------------------------------------
//
// Everything below reads the AUTHORED ARTIFACTS back — the manifest as Provision
// receives it, the rule document as event-processing decodes it — rather than
// asserting against the constants the builder was handed.
//
// 🔴 READING THE ARTIFACT BACK IS ONLY HALF OF IT, and getting the other half wrong is
// how seven mutants walked through the first version of this file. A test that decodes
// the published document and then compares a value to THE SAME CONSTANT THAT PRODUCED
// IT cannot fail: rename the constant's value and both sides move together, green.
// `len(devices) != sitepulseDozerCount` and `p.Name != SitepulseGotoAreaParam` were
// each exactly that, and each let its mutant through the whole suite.
//
// So the values that are CONTRACTS with something outside this package — the tokens an
// authored Unity scene binds, the wire key an enqueue payload spells, the zone tokens
// the scene maps to real places — are pinned against LITERALS here, deliberately
// duplicated. The duplication IS the mechanism: a rename must be typed twice, and the
// second typing is a reviewable diff at the surface that actually constitutes the
// contract. Where a literal would only restate an internal choice, the constant is
// still fine.
//
// The zone tokens and "areaToken" get literals for a further reason: neither appears in
// the rule document, so the authored-rules fixture cannot see them either. This file is
// the only gate they have.

// The sitepulse strings that are contracts with a consumer outside this module,
// spelled as literals. Nothing in the scenario may read these — they exist to be
// compared AGAINST the scenario.
const (
	wantSitepulseDozerCount = 1

	wantSitepulseFirstToken      = "sp-dozer-01"
	wantSitepulseFirstExternalId = "SP-DZ-0001"

	wantSitepulseAreaParam = "areaToken"

	wantSitepulseCustomerToken = "acme-earthworks"
	wantSitepulseRefuelAsset   = "sp-refuel-station"
)

// wantSitepulseZoneTokens is the site's zones IN ORDER. The order is load-bearing
// twice over: it is the round-robin order DistributeAcross places machines in (so the
// sole S0 dozer's zone is decided by which token is first), and it is the order the
// goto-area enum is rendered in. A swap is invisible to any test that compares the
// enum to the areas, since both sides move.
var wantSitepulseZoneTokens = []string{"sp-zone-cut", "sp-zone-fill", "sp-zone-yard"}

// wantSitepulseMetricKeys is the machine's full declared vocabulary. Asserted as a SET
// rather than "the rule's metric exists", because engine_temp_c and engine_hours are
// read by no rule and no widget — so deleting either is invisible to every other check
// in this file, while quietly removing a metric the player emits and the profile is
// supposed to promise.
var wantSitepulseMetricKeys = []string{"fuel_pct", "engine_temp_c", "engine_hours"}

func sitepulseManifest(t *testing.T) SimManifest {
	t.Helper()
	return NewSitepulse(1, Load{}).Manifest()
}

func sitepulseProfile(t *testing.T) ProfileSpec {
	t.Helper()
	return profileByToken(t, sitepulseManifest(t), SitepulseProfileToken)
}

// commandByKey returns the profile's command declaring a given COMMAND KEY — the
// string a sendCommand action names — and reports whether one exists. Keyed on
// CommandKey rather than Token deliberately: that is the lookup command-delivery's
// enqueue gate performs, so it is the one a test standing in for it must perform too.
func commandByKey(p ProfileSpec, key string) (CommandSpec, bool) {
	for _, c := range p.Commands {
		if c.CommandKey == key {
			return c, true
		}
	}
	return CommandSpec{}, false
}

// ---- The manifest -------------------------------------------------------------

// The whole-manifest gate. It is cheap and it covers the cross-checks that live
// nowhere else — a rule reading a metric the profile does not declare, a command key
// no command publishes, an assignment to an area that does not exist — each of which
// otherwise provisions cleanly and produces a scenario that is inert rather than
// broken.
func TestSitepulseManifestValidates(t *testing.T) {
	if err := sitepulseManifest(t).Validate(); err != nil {
		t.Fatalf("the sitepulse manifest does not validate: %v", err)
	}
}

// ---- The anchor topology -------------------------------------------------------
//
// Everything in this section was UNASSERTED in the first version of this file, and all
// of it survives Validate: an empty DistributeAcross is legal (renderAssignments only
// acts when "area" is present), a manifest with no customers is legal, an asset nothing
// references is legal, and a profile may declare as few metrics as it likes. So a
// deletion here provisions clean, validates clean, publishes clean — and leaves the
// dozer anchored to nothing, which is exactly the "looks provisioned, is inert" class
// the manifest's other cross-checks exist to catch.

// The dozer must be ANCHORED, not merely created. Every measurement it emits carries
// the anchors its assignments render (ADR-013/044), so a device with none produces
// telemetry that no area-scoped or customer-scoped query can ever reach: the events
// land, the device is healthy, and a board scoped to the site shows nothing forever.
//
// Asserted on Expand's OUTPUT rather than on the DistributeAcross field, because the
// field is the input and the assignment is the artifact — and because the customer
// anchor is not a DistributeAcross target at all (renderAssignments applies the
// manifest's single customer unconditionally), so reading the field would miss it.
func TestSitepulseAnchorsTheDozerToItsZoneAndItsCustomer(t *testing.T) {
	devices := sitepulseManifest(t).Expand(1)
	if len(devices) == 0 {
		t.Fatal("no devices, so there are no assignments to judge")
	}
	dozer := devices[0]

	anchors := map[string]string{}
	for _, a := range dozer.Assignments {
		if prev, dup := anchors[a.TargetType]; dup {
			t.Fatalf("device %q carries two %s anchors (%q and %q)", dozer.Token, a.TargetType, prev, a.TargetToken)
		}
		anchors[a.TargetType] = a.TargetToken
	}

	// The AREA anchor. At count=1 the round-robin puts the sole dozer in the FIRST
	// zone, so this pins the placement and the zone ordering at once — a swapped zone
	// list moves the dozer to the fill ground, which is a different scene entirely.
	if got := anchors["area"]; got != wantSitepulseZoneTokens[0] {
		t.Errorf("the sole dozer's area anchor is %q, want %q. An unanchored dozer emits "+
			"telemetry no area-scoped query can reach, and the device still looks healthy",
			got, wantSitepulseZoneTokens[0])
	}
	// The CUSTOMER anchor, which no DistributeAcross value controls — it exists only
	// because the manifest declares a customer at all.
	if got := anchors["customer"]; got != wantSitepulseCustomerToken {
		t.Errorf("the sole dozer's customer anchor is %q, want %q — deleting the manifest's "+
			"Customers validates clean and silently drops this anchor from every device",
			got, wantSitepulseCustomerToken)
	}
}

// The refuelling station is the destination the whole low-fuel loop ends at, and it is
// referenced by NOTHING the manifest can check: assets are provisioned but are not
// assignment targets yet, so deleting it validates clean and the scene's refuel point
// has no platform-side entity behind it.
func TestSitepulseProvisionsTheRefuellingStation(t *testing.T) {
	m := sitepulseManifest(t)

	var station AssetSpec
	for _, a := range m.Assets {
		if a.Token == wantSitepulseRefuelAsset {
			station = a
		}
	}
	if station.Token == "" {
		t.Fatalf("no asset %q is declared, so the destination the low-fuel command sends the "+
			"machine to exists only in the scene", wantSitepulseRefuelAsset)
	}
	// Its type must be declared too, or Validate would have caught it — asserted anyway
	// so the asset TYPE is not silently repointed at some other classifier.
	if station.AssetTypeToken != SitepulseAssetTypeToken {
		t.Errorf("the refuelling station is of type %q, want %q", station.AssetTypeToken, SitepulseAssetTypeToken)
	}
}

// The zones, in order, against literals. Two facts at once, and neither is checkable
// from the enum test — which compares the enum to the areas, so both sides move
// together under a rename OR a reorder.
func TestSitepulseDeclaresTheThreeSiteZonesInOrder(t *testing.T) {
	m := sitepulseManifest(t)
	if len(m.Areas) != len(wantSitepulseZoneTokens) {
		t.Fatalf("the site declares %d zones, want %d: %+v", len(m.Areas), len(wantSitepulseZoneTokens), m.Areas)
	}
	for i, want := range wantSitepulseZoneTokens {
		if m.Areas[i].Token != want {
			t.Errorf("zone[%d] is %q, want %q. These tokens are what the scene maps to real "+
				"places on the site and what a goto-area payload spells; they appear in no rule "+
				"document, so this file is the only thing that sees them",
				i, m.Areas[i].Token, want)
		}
		if m.Areas[i].AreaTypeToken != SitepulseAreaTypeToken {
			t.Errorf("zone %q is of area type %q, want %q", m.Areas[i].Token, m.Areas[i].AreaTypeToken, SitepulseAreaTypeToken)
		}
	}
}

// The whole metric vocabulary, as a set. Only fuel_pct is reachable from any other
// check in this file (through the rule), so engine_temp_c and engine_hours are
// deletable without a single test noticing — while the profile is the capability
// contract the player publishes against, and a metric it does not declare is one
// device-management rejects at ingest.
func TestSitepulseProfileDeclaresTheWholeMachineVocabulary(t *testing.T) {
	declared := map[string]MetricSpec{}
	for _, mx := range sitepulseProfile(t).Metrics {
		declared[mx.Key] = mx
	}
	if len(declared) != len(wantSitepulseMetricKeys) {
		t.Errorf("the profile declares %d metrics, want %d: %v", len(declared), len(wantSitepulseMetricKeys), declared)
	}
	for _, key := range wantSitepulseMetricKeys {
		mx, ok := declared[key]
		if !ok {
			t.Errorf("the profile does not declare metric %q; the player publishes it and the "+
				"profile is what promises it exists", key)
			continue
		}
		// DOUBLE across the board — an INT fuel percentage would truncate every reading
		// the low-fuel threshold is compared against.
		if mx.DataType != "DOUBLE" {
			t.Errorf("metric %q is %s, want DOUBLE", key, mx.DataType)
		}
		if mx.Unit == "" {
			t.Errorf("metric %q declares no unit, so a query reads back a bare number", key)
		}
	}
}

// ---- The rule and the command are read from BOTH sides -------------------------

// The seam the manifest's command cross-check exists to close, asserted here by
// construction so a later edit to EITHER side breaks this test rather than silently
// diverging.
//
// 🔴 THE HAZARD IS THAT ALL THREE CANDIDATE STRINGS ARE GRAMMAR-VALID TOKENS. A rule
// naming the CommandDefinition's Token ("sp-cmd-refuel") or its display Name instead of
// its CommandKey ("goto-refuel") publishes perfectly clean: event-processing's compiler
// is deliberately state-free and checks only that the key is a valid token, never that
// any profile declares it, and the rule then reports ACTIVE. It fails for the first time
// when the rule FIRES — command-delivery's enqueue gate rejects the key, REACT classifies
// the error as retryable, and the detection redelivers to the poison cap and dead-letters
// with nothing pointing back at the manifest line that wrote it.
//
// So this resolves the key the DOCUMENT carries through the profile's vocabulary, the
// way the enqueue gate would, and additionally asserts the two near-miss strings are NOT
// what the rule names — because a lookup that happened to succeed for the wrong reason
// would be the same green.
func TestSitepulseRuleSendsACommandTheProfileActuallyDeclares(t *testing.T) {
	profile := sitepulseProfile(t)
	doc := decodeThresholdRule(t, soleDetectionRule(t, profile).Definition)

	if len(doc.Actions) != 2 {
		t.Fatalf("the low-fuel rule renders %d actions, want 2 (raiseAlarm + sendCommand): %+v",
			len(doc.Actions), doc.Actions)
	}
	if doc.Actions[0].Type != actionTypeRaiseAlarm {
		t.Errorf("first action is %q, want %q", doc.Actions[0].Type, actionTypeRaiseAlarm)
	}
	if doc.Actions[0].RaiseAlarm.AlarmKey != SitepulseAlarmKey {
		t.Errorf("the rule raises alarm key %q, want %q", doc.Actions[0].RaiseAlarm.AlarmKey, SitepulseAlarmKey)
	}
	if doc.Actions[1].Type != actionTypeSendCommand {
		t.Fatalf("second action is %q, want %q — without it the machine is told nothing and "+
			"the loop this scenario exists for is only half built", doc.Actions[1].Type, actionTypeSendCommand)
	}

	sent := doc.Actions[1].SendCommand.Command
	command, ok := commandByKey(profile, sent)
	if !ok {
		t.Fatalf("the rule sends command key %q, which profile %q declares no command for — it "+
			"would publish, compile, report %s, and dead-letter on its first firing",
			sent, profile.Token, RuleStatusActiveWire)
	}
	// Which command it resolved to matters as much as that it resolved: goto-area is also
	// declared here, and a rule chaining THAT would pass the lookup above while enqueuing
	// a command whose required areaToken parameter it cannot supply.
	if command.Token != SitepulseRefuelCommandDef {
		t.Errorf("the rule's command key %q resolves to command %q, want the refuelling one %q",
			sent, command.Token, SitepulseRefuelCommandDef)
	}
	// The two near misses, asserted explicitly. Each is a valid token, so nothing on the
	// platform side would object to either.
	if sent == command.Token {
		t.Errorf("the rule names the command's TOKEN %q rather than its commandKey %q", sent, command.CommandKey)
	}
	if sent == command.Name {
		t.Errorf("the rule names the command's display NAME %q rather than its commandKey %q", sent, command.CommandKey)
	}
}

// The refuel command takes no arguments, and that is a design decision rather than an
// omission: REACT's sendCommand payload is STATIC — frozen at authoring time, with no
// templating — so a parameterised refuel command could never be given a station to drive
// to by the rule that sends it. Asserted because the rule builder renders no payload at
// all, so a required parameter appearing here would make every firing fail the enqueue
// gate's payload validation.
func TestSitepulseRefuelCommandTakesNoArguments(t *testing.T) {
	command, ok := commandByKey(sitepulseProfile(t), SitepulseRefuelCommandKey)
	if !ok {
		t.Fatalf("no command declares key %q", SitepulseRefuelCommandKey)
	}
	if command.ParameterSchema != "" {
		t.Errorf("the refuel command declares a parameter schema (%s), but the rule that sends "+
			"it renders no payload — a required parameter would fail the enqueue gate at every "+
			"firing, and the platform cannot compute one anyway (sendCommand payloads are static)",
			command.ParameterSchema)
	}
}

// ---- Direction: the rule must fire on the way DOWN ----------------------------

// LOW fuel means the rule fires BELOW the threshold, so the rendered operator must be
// "lt". This is the one assertion in the file whose wrong answer is completely silent:
// a rule reading `gt 15` publishes, compiles, reports ACTIVE and raises low-fuel on a
// nearly FULL tank while staying quiet on an empty one — every layer between the
// manifest and the alarm table reporting success.
//
// Read off the DOCUMENT, not off the Op field, because the document is what the compiler
// sees; and compared to the literal "lt" as well as to OpLt, so swapping the two
// constants' values would not leave this green.
func TestSitepulseLowFuelRuleFiresBelowTheThresholdNotAboveIt(t *testing.T) {
	doc := decodeThresholdRule(t, soleDetectionRule(t, sitepulseProfile(t)).Definition)

	if doc.When.Op != "lt" {
		t.Errorf("the low-fuel rule renders op %q, want \"lt\" — fuel is a DEPLETING quantity, so "+
			"the rule must fire while fuel_pct is BELOW %v%%. With \"gt\" it would raise low-fuel "+
			"on a full tank and go silent on an empty one, and nothing downstream would say so",
			doc.When.Op, SitepulseLowFuelThreshold)
	}
	if doc.When.Op != string(OpLt) {
		t.Errorf("the rendered op %q is not OpLt (%q) — the direction constants no longer spell "+
			"what the compiler reads", doc.When.Op, OpLt)
	}
	if doc.When.Threshold != SitepulseLowFuelThreshold {
		t.Errorf("the rule fires below %v but the scenario is designed around %v",
			doc.When.Threshold, SitepulseLowFuelThreshold)
	}
	if doc.When.Metric != SitepulseFuelKey {
		t.Errorf("the rule reads metric %q, want %q", doc.When.Metric, SitepulseFuelKey)
	}
	// The AUTHORING severity, which is lowercase. The uppercase wire form compiles
	// nowhere — event-processing's rule compiler rejects it — so a scenario handed the
	// wire constant by mistake never publishes at all.
	if doc.Severity != SitepulseSeverity {
		t.Errorf("the rule carries severity %q, want the authoring form %q", doc.Severity, SitepulseSeverity)
	}
	if doc.Severity == SitepulseAlarmSeverityWire {
		t.Errorf("the rule carries the WIRE severity %q; the compiler rejects it and the profile "+
			"would not publish", doc.Severity)
	}
	// Disabled rules are published UNCHECKED — the publish gate only validates enabled
	// ones — so a rule parked here would make a broken predicate look accepted.
	if !soleDetectionRule(t, sitepulseProfile(t)).Enabled {
		t.Error("the low-fuel rule is disabled, so it is published unchecked and never fires")
	}
}

// ---- The far end lives in another process --------------------------------------

// 🔴 FarEndInternal WOULD BE ACTIVELY WRONG HERE, and that is why this is asserted on the
// exact mode rather than on "declares a far end". The device is a Unity player holding
// its own MQTT session under the device's own credential; FarEndInternal would make
// Bootstrap attach a Go cmdreceiver alongside it, which answers SUCCESSFUL the moment a
// delivery envelope arrives — for work only the player can do. The command round trip
// would then report a perfect QUEUED -> SENT -> SUCCESSFUL while the dozer never moved,
// which is worse than the failure the far-end seam was built to remove: a command that
// expires at SENT at least LOOKS broken. (MQTT 3.1.1 has no shared subscriptions either,
// so the two sessions would fight over one client id and evict each other.)
//
// FarEndNone is the opposite error and is refused for a different reason: it would leave
// the scenario with no declared far end at all, which is the honest state for a
// telemetry-only demo and a lie for the one scenario whose entire point is the command
// arriving.
func TestSitepulseCommandFarEndIsExternal(t *testing.T) {
	m := sitepulseManifest(t)
	if m.CommandFarEnd != FarEndExternal {
		t.Fatalf("sitepulse declares CommandFarEnd %q, want %q. %q would attach a Go cmdreceiver "+
			"that answers SUCCESSFUL for a dozer that never moved; %q would declare no command "+
			"channel for the one scenario that is entirely about one",
			m.CommandFarEnd, FarEndExternal, FarEndInternal, FarEndNone)
	}
	// Read through the normalizing accessor too, since that is what every consumer —
	// attachCommandFarEnd, commandFarEndStatus — actually switches on.
	if m.FarEndMode() != FarEndExternal {
		t.Errorf("FarEndMode() is %q, want %q", m.FarEndMode(), FarEndExternal)
	}
	// The half of the external contract this side owns: the published command vocabulary
	// is the ONLY thing the out-of-process client can subscribe for, so a far end with no
	// commands declares a device plane nothing could reach even if it were running.
	if len(sitepulseProfile(t).Commands) == 0 {
		t.Error("an external far end with no declared commands: there is nothing for the player " +
			"to subscribe for and nothing for a widget to enqueue against")
	}
}

// ---- The population ------------------------------------------------------------

// The token and externalId patterns are a PUBLISHED CONTRACT with an authored Unity
// scene: the player resolves devicesByExternalId(["SP-DZ-0001"]) to the addressing token
// and then to the credential. A pattern edit here silently renames every machine the
// scene knows, and the failure surfaces in another repo as a bind that finds nothing.
//
// Checked at S0's count AND at a resized one, because the padding is what makes the two
// agree: the scene binds four-digit ids that count=1 does not need, and a pattern that
// only happened to render correctly at one device would break on the first resize.
func TestSitepulsePopulationRendersTheTokensTheSceneBindsTo(t *testing.T) {
	// S0: exactly one dozer, and the ids the acceptance run names literally.
	devices := sitepulseManifest(t).Expand(1)
	// Against the LITERAL 1, not against sitepulseDozerCount. Comparing the expansion
	// to the constant that sized it is a check that cannot fail: bump the constant to 2
	// and both sides move, so S0's "the vertical at count=1" silently becomes something
	// else with every test green.
	if len(devices) != wantSitepulseDozerCount {
		t.Fatalf("sitepulse expands to %d devices, want exactly %d — S0 is deliberately the "+
			"vertical at one machine", len(devices), wantSitepulseDozerCount)
	}
	if devices[0].Token != wantSitepulseFirstToken {
		t.Errorf("the sole dozer's token is %q, want %q", devices[0].Token, wantSitepulseFirstToken)
	}
	if devices[0].ExternalId != wantSitepulseFirstExternalId {
		t.Errorf("the sole dozer's externalId is %q, want %q — this is the key the scene binds "+
			"by, so a change here is a rename the Unity side cannot see",
			devices[0].ExternalId, wantSitepulseFirstExternalId)
	}
	if devices[0].DeviceTypeToken != SitepulseDeviceTypeToken {
		t.Errorf("the sole dozer is of type %q, want %q", devices[0].DeviceTypeToken, SitepulseDeviceTypeToken)
	}

	// Resized to S1's fleet, through NewSim — the only supported construction path, and
	// the one that would refuse the count if the manifest had acquired a second
	// population or gone FixedTopology.
	s, err := NewSim("sitepulse", 1, Load{DeviceCount: 18})
	if err != nil {
		t.Fatalf("NewSim at 18 devices: %v", err)
	}
	resized := s.Manifest().Expand(1)
	if len(resized) != 18 {
		t.Fatalf("a resized sitepulse expands to %d devices, want 18", len(resized))
	}
	if resized[0].Token != wantSitepulseFirstToken || resized[0].ExternalId != wantSitepulseFirstExternalId {
		t.Errorf("resizing moved the FIRST device to %q/%q; the scene's binding must survive a "+
			"population change", resized[0].Token, resized[0].ExternalId)
	}
	if got, want := resized[17].Token, "sp-dozer-18"; got != want {
		t.Errorf("the eighteenth dozer's token is %q, want %q", got, want)
	}
	if got, want := resized[17].ExternalId, "SP-DZ-0018"; got != want {
		t.Errorf("the eighteenth dozer's externalId is %q, want %q", got, want)
	}
	// The zones are round-robin targets, so at 18 they must divide evenly — six machines
	// per zone with no remainder is what makes the S1 site look composed rather than
	// lopsided, and it is a property of the ZONE COUNT, not of the population.
	if zones := len(sitepulseManifest(t).Areas); 18%zones != 0 {
		t.Errorf("18 machines do not divide evenly across %d zones", zones)
	}
}

// ---- The goto-area enum is derived, not copied ----------------------------------

// The command's enum and the manifest's Areas are two statements of one fact — where a
// machine may be sent — and the enum is built from the same list the Areas are. This
// asserts the RESULT rather than the mechanism, so a later hand-typed enum that happens
// to agree today still fails the day a zone is renamed.
//
// The quiet failure it stands in for: a goto-area naming a zone that no longer exists is
// a grammar-valid STRING that the enqueue gate accepts against the schema, and the scene
// then cannot resolve it to anywhere on the site.
func TestSitepulseGotoCommandEnumIsExactlyTheSitesZones(t *testing.T) {
	m := sitepulseManifest(t)
	command, ok := commandByKey(profileByToken(t, m, SitepulseProfileToken), SitepulseGotoCommandKey)
	if !ok {
		t.Fatalf("no command declares key %q", SitepulseGotoCommandKey)
	}

	var params []struct {
		Name     string   `json:"name"`
		Kind     string   `json:"kind"`
		DataType string   `json:"dataType"`
		Required bool     `json:"required"`
		Enum     []string `json:"enum"`
	}
	if err := json.Unmarshal([]byte(command.ParameterSchema), &params); err != nil {
		t.Fatalf("the goto-area parameter schema is not a decodable descriptor array: %v", err)
	}
	if len(params) != 1 {
		t.Fatalf("the goto-area command declares %d parameters, want exactly 1", len(params))
	}
	p := params[0]
	// Against the LITERAL, not against SitepulseGotoAreaParam. This is the WIRE KEY an
	// enqueue payload must spell — the console's form, the scene's action UI and
	// command-delivery's payload validation all key on this exact string — so comparing
	// it to the constant that rendered it would let a rename to "destination" pass every
	// test while breaking every caller that spells the old one.
	if p.Name != wantSitepulseAreaParam {
		t.Errorf("the parameter is named %q, want %q — this is the key an enqueue payload "+
			"spells, so a rename is a break every external caller sees and no test here would",
			p.Name, wantSitepulseAreaParam)
	}
	if p.DataType != "STRING" || p.Kind != "SCALAR" {
		t.Errorf("the parameter is %s/%s, want SCALAR/STRING", p.Kind, p.DataType)
	}
	if !p.Required {
		t.Error("the parameter is optional, so a goto-area with no destination would be accepted " +
			"at enqueue and mean nothing to the machine that received it")
	}

	// Compared against the LITERAL zone list, not against m.Areas. Deriving the
	// expectation from the same areas the enum is built from makes both sides move
	// together — a renamed or reordered zone would keep this green, which is how the
	// zone-swap mutant survived. TestSitepulseDeclaresTheThreeSiteZonesInOrder pins the
	// areas to the same literals, so the two ends of the derivation are held
	// independently and the DERIVATION itself is what this test proves.
	if len(p.Enum) != len(wantSitepulseZoneTokens) {
		t.Fatalf("the enum offers %d destinations but the site has %d zones: %v vs %v",
			len(p.Enum), len(wantSitepulseZoneTokens), p.Enum, wantSitepulseZoneTokens)
	}
	for i, want := range wantSitepulseZoneTokens {
		if p.Enum[i] != want {
			t.Errorf("enum[%d] is %q, want %q — the console would offer a destination the site "+
				"does not have, and a goto-area naming it is a grammar-valid STRING the enqueue "+
				"gate accepts and the scene cannot resolve to anywhere", i, p.Enum[i], want)
		}
	}
	// And the derivation itself: the enum must still BE the manifest's areas, or the two
	// literal-pinned ends could agree while the schema was hand-typed in between.
	for i, a := range m.Areas {
		if i < len(p.Enum) && p.Enum[i] != a.Token {
			t.Errorf("enum[%d] is %q but the manifest's zone %d is %q — the schema is no longer "+
				"derived from the areas", i, p.Enum[i], i, a.Token)
		}
	}
}

// ---- Bootstrap-only, by design --------------------------------------------------

// The no-op Tick has to be DECLARED, not merely true, and this is the assertion that
// makes the two agree.
//
// The failure it stands for is the one that shipped in the first draft of this
// scenario: registering a scenario whose Tick emits nothing quietly broke "resizable
// implies load-drivable", because a load harness drives Tick directly. A run started
// against it — from a --manifest offering built off the registry, or from a harness
// defaulting the id off the handshake — provisions, holds for its whole window, applies
// zero load, and fails the min-accepted floor with a message about lost load flags. The
// declaration is what lets Profile.Validate refuse it by name instead.
func TestSitepulseDeclaresThatItsDevicesPublishTheirOwnTelemetry(t *testing.T) {
	if !sitepulseManifest(t).DevicesPublishTheirOwnTelemetry {
		t.Error("sitepulse does not declare DevicesPublishTheirOwnTelemetry, but its Tick emits " +
			"nothing: a load run would hold for its whole window, apply zero load, and fail the " +
			"min-accepted floor as though its load flags had been lost")
	}
	// The two flags are separate facts and must not be inferred from one another: an
	// external far end says who ANSWERS COMMANDS, not who PUBLISHES TELEMETRY. Pinned
	// here so a later "simplification" that derives one from the other has to delete an
	// assertion that says why it must not.
	if m := sitepulseManifest(t); m.FarEndMode() != FarEndExternal || !m.DevicesPublishTheirOwnTelemetry {
		t.Errorf("sitepulse declares far end %q and self-publishing %v; both are true of this "+
			"scenario independently, and neither implies the other",
			m.FarEndMode(), m.DevicesPublishTheirOwnTelemetry)
	}
}

// Tick must do NOTHING, and it is asserted rather than left to the comment because the
// wrong direction here is silent and destructive: a Go emitter would publish a SECOND,
// contradicting reading for a device the Unity player is already reporting, so low-fuel
// would raise against telemetry the machine on screen knows nothing about.
//
// It is checked against a REAL fake ingress that counts what reaches it, not against a
// nil-error return and not against a Runtime with no client. A nil error says nothing —
// an emitter pointed at a working ingress returns nil too — and a client-less Runtime
// would turn an emitting Tick into a nil-pointer panic, which is a red for the wrong
// reason and stops measuring the moment someone hardens the emit path. Zero requests at
// the far end is the property, so that is what is counted.
func TestSitepulseTickEmitsNothingBecauseUnityIsTheDevice(t *testing.T) {
	var requests atomic.Int64
	rt := fakeIngress(t, 1, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	// Sitepulse's OWN topology, so the no-op is measured over the devices it really
	// provisions rather than over an empty slice — which EmitAll returns early on, making
	// any emitter look like a no-op too.
	rt.Devices = NewSitepulse(1, Load{}).Manifest().Expand(1)
	if len(rt.Devices) == 0 {
		t.Fatal("no devices to emit against, so this test could not tell an emitter from a no-op")
	}

	s := NewSitepulse(1, Load{})
	for i := 0; i < 3; i++ {
		if err := s.Tick(t.Context(), rt); err != nil {
			t.Fatalf("tick %d returned %v; sitepulse's Tick is bootstrap-only and does no work", i, err)
		}
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("Tick made %d ingress requests: the Go runner is publishing telemetry for a "+
			"device the Unity player is also publishing, so the two readings contradict each "+
			"other and the low-fuel rule fires against the tank nobody can see", n)
	}
	snap := rt.Stats.Snapshot(time.Now())
	if snap.Emitted != 0 || snap.Failed != 0 || snap.Shed != 0 {
		t.Errorf("Tick moved the emit counters (emitted %d, failed %d, shed %d)",
			snap.Emitted, snap.Failed, snap.Shed)
	}
}
