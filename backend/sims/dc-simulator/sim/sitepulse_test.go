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
	// S1: six of each machine kind and one plant. Literals, never derived from the
	// per-kind counts the manifest is built from: a comparison to the constant that
	// sized a population cannot fail.
	wantSitepulseMachinesPerKind = 6
	wantSitepulsePlantCount      = 1
	wantSitepulseDeviceCount     = 3*wantSitepulseMachinesPerKind + wantSitepulsePlantCount

	wantSitepulseFirstToken      = "sp-dozer-01"
	wantSitepulseFirstExternalId = "SP-DZ-0001"
	// The first dozer's bearer at seed 1, captured from the S0 manifest. Growing the site
	// must not move an existing machine's credential, or a scene authored at S0 stops
	// authenticating.
	wantSitepulseFirstCredentialId = "a85913521abfb00e24b1fa855ab1803c"

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
var wantSitepulseMetricKeys = []string{"fuel_pct", "engine_temp_c", "engine_hours", "payload_t", "tyre_pressure_kpa"}

// wantSitepulseNewMetricUnits are the units of the two measurements S1 added to the
// equipment profile.
var wantSitepulseNewMetricUnits = map[string]string{"payload_t": "t", "tyre_pressure_kpa": "kPa"}

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
	doc := decodeThresholdRule(t, ruleByToken(t, profile, "sp-rule-lowfuel").Definition)

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
	doc := decodeThresholdRule(t, ruleByToken(t, sitepulseProfile(t), "sp-rule-lowfuel").Definition)

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
	if !ruleByToken(t, sitepulseProfile(t), "sp-rule-lowfuel").Enabled {
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
// Every kind's first and last device is pinned against LITERALS, for the same reason the
// counts are: a comparison to the constant that rendered the pattern cannot fail.
func TestSitepulsePopulationRendersTheTokensTheSceneBindsTo(t *testing.T) {
	devices := sitepulseManifest(t).Expand(1)
	if len(devices) != wantSitepulseDeviceCount {
		t.Fatalf("sitepulse expands to %d devices, want exactly %d (6 dozers, 6 loaders, "+
			"6 haul trucks, 1 plant)", len(devices), wantSitepulseDeviceCount)
	}

	kinds := []struct {
		name                            string
		from, to                        int
		firstToken, lastToken           string
		firstExternalId, lastExternalId string
		deviceType                      string
	}{
		{"dozer", 0, 6, "sp-dozer-01", "sp-dozer-06", "SP-DZ-0001", "SP-DZ-0006", "sp-dozer"},
		{"loader", 6, 12, "sp-loader-01", "sp-loader-06", "SP-LD-0001", "SP-LD-0006", "sp-loader"},
		{"hauler", 12, 18, "sp-hauler-01", "sp-hauler-06", "SP-HL-0001", "SP-HL-0006", "sp-hauler"},
		{"plant", 18, 19, "sp-plant-01", "sp-plant-01", "SP-PL-0001", "SP-PL-0001", "sp-crusher-plant"},
	}
	for _, k := range kinds {
		first, last := devices[k.from], devices[k.to-1]
		if first.Token != k.firstToken || last.Token != k.lastToken {
			t.Errorf("%s tokens run %q..%q, want %q..%q", k.name, first.Token, last.Token, k.firstToken, k.lastToken)
		}
		if first.ExternalId != k.firstExternalId || last.ExternalId != k.lastExternalId {
			t.Errorf("%s externalIds run %q..%q, want %q..%q — these are the keys the scene binds by",
				k.name, first.ExternalId, last.ExternalId, k.firstExternalId, k.lastExternalId)
		}
		for _, d := range devices[k.from:k.to] {
			if d.DeviceTypeToken != k.deviceType {
				t.Errorf("%s %q is of device type %q, want %q", k.name, d.Token, d.DeviceTypeToken, k.deviceType)
			}
		}
	}
	// The dozer population stays FIRST: the anchor test and every consumer that reads
	// "the first device" were written against it.
	if devices[0].Token != wantSitepulseFirstToken || devices[0].ExternalId != wantSitepulseFirstExternalId {
		t.Errorf("the first device is %q/%q, want %q/%q", devices[0].Token, devices[0].ExternalId,
			wantSitepulseFirstToken, wantSitepulseFirstExternalId)
	}
}

// Machines are placed round-robin PER POPULATION, so six of a kind lands two in each
// zone in the zone order, and the plant — a population of one — lands in the first.
func TestSitepulseSpreadsEveryMachineKindAcrossTheZones(t *testing.T) {
	devices := sitepulseManifest(t).Expand(1)
	if len(devices) != wantSitepulseDeviceCount {
		t.Fatalf("sitepulse expands to %d devices, want %d", len(devices), wantSitepulseDeviceCount)
	}
	zoneOf := func(d DeviceInstance) string {
		for _, a := range d.Assignments {
			if a.TargetType == "area" {
				return a.TargetToken
			}
		}
		return ""
	}
	for kind := 0; kind < 3; kind++ {
		perZone := map[string]int{}
		for i := 0; i < wantSitepulseMachinesPerKind; i++ {
			d := devices[kind*wantSitepulseMachinesPerKind+i]
			if got, want := zoneOf(d), wantSitepulseZoneTokens[i%3]; got != want {
				t.Errorf("%s is anchored to %q, want %q", d.Token, got, want)
			}
			perZone[zoneOf(d)]++
		}
		for _, z := range wantSitepulseZoneTokens {
			if perZone[z] != 2 {
				t.Errorf("kind %d has %d machines in %q, want 2", kind, perZone[z], z)
			}
		}
	}
	plant := devices[18]
	if got := zoneOf(plant); got != "sp-zone-cut" {
		t.Errorf("the plant is anchored to %q, want the cut face: with no area anchor its "+
			"telemetry is unreachable by any area-scoped query", got)
	}
	for _, d := range devices {
		customer := ""
		for _, a := range d.Assignments {
			if a.TargetType == "customer" {
				customer = a.TargetToken
			}
		}
		if customer != wantSitepulseCustomerToken {
			t.Errorf("%s has customer anchor %q, want %q", d.Token, customer, wantSitepulseCustomerToken)
		}
	}
}

// Provisioning mints a credential per device from Expand, and the Unity player resolves
// {deviceToken}-cred. Every one of the 19 devices needs one, they must not collide, and
// growing the site must not move the S0 dozer's bearer.
func TestSitepulseEveryDeviceGetsItsOwnCredentialAndExternalId(t *testing.T) {
	devices := sitepulseManifest(t).Expand(1)
	credIds := map[string]string{}
	externalIds := map[string]string{}
	for _, d := range devices {
		if d.CredentialToken != d.Token+"-cred" {
			t.Errorf("%s has credential token %q, want %q", d.Token, d.CredentialToken, d.Token+"-cred")
		}
		if d.CredentialId == "" || d.ExternalId == "" {
			t.Errorf("%s has an empty credential id (%q) or externalId (%q)", d.Token, d.CredentialId, d.ExternalId)
		}
		if prev, dup := credIds[d.CredentialId]; dup {
			t.Errorf("%s and %s share a bearer", prev, d.Token)
		}
		credIds[d.CredentialId] = d.Token
		if prev, dup := externalIds[d.ExternalId]; dup {
			t.Errorf("%s and %s share externalId %q", prev, d.Token, d.ExternalId)
		}
		externalIds[d.ExternalId] = d.Token
	}
	if devices[0].CredentialId != wantSitepulseFirstCredentialId {
		t.Errorf("sp-dozer-01's bearer at seed 1 is %q, want %q: the S1 site moved an S0 machine's credential",
			devices[0].CredentialId, wantSitepulseFirstCredentialId)
	}
}

// --devices sizes every MACHINE population and leaves the plant alone: there is one
// crusher on the site however many machines work it.
func TestSitepulseResizeScalesTheMachinesAndKeepsTheSinglePlant(t *testing.T) {
	s, err := NewSim("sitepulse", 1, Load{DeviceCount: 3})
	if err != nil {
		t.Fatalf("NewSim at a device count of 3: %v", err)
	}
	devices := s.Manifest().Expand(1)
	if want := 3*3 + wantSitepulsePlantCount; len(devices) != want {
		t.Fatalf("sitepulse at --devices 3 expands to %d devices, want %d (3 per machine kind + the plant)",
			len(devices), want)
	}
	if devices[0].Token != wantSitepulseFirstToken || devices[0].ExternalId != wantSitepulseFirstExternalId {
		t.Errorf("resizing moved the first device to %q/%q", devices[0].Token, devices[0].ExternalId)
	}
	if last := devices[len(devices)-1]; last.Token != "sp-plant-01" {
		t.Errorf("the last device is %q, want the single plant sp-plant-01", last.Token)
	}
	if err := s.Manifest().Validate(); err != nil {
		t.Errorf("the resized manifest does not validate: %v", err)
	}
}

// The three machine kinds are device types over ONE shared equipment profile; the plant is
// a fixed type over its own profile, because it reports metrics no machine does.
func TestSitepulseDeviceTypesShareTheEquipmentProfileAndThePlantHasItsOwn(t *testing.T) {
	m := sitepulseManifest(t)
	got := map[string]string{}
	for _, dt := range m.DeviceTypes {
		got[dt.Token] = dt.ProfileToken
	}
	want := map[string]string{
		"sp-dozer":         "sp-equipment-profile",
		"sp-loader":        "sp-equipment-profile",
		"sp-hauler":        "sp-equipment-profile",
		"sp-crusher-plant": "sp-plant-profile",
	}
	if len(got) != len(want) {
		t.Errorf("the site declares device types %v, want %v", got, want)
	}
	for tok, profile := range want {
		if got[tok] != profile {
			t.Errorf("device type %q references profile %q, want %q", tok, got[tok], profile)
		}
	}
}

// The two measurements S1 added to the equipment profile.
func TestSitepulseEquipmentProfileCarriesPayloadAndTyrePressure(t *testing.T) {
	declared := map[string]MetricSpec{}
	for _, mx := range sitepulseProfile(t).Metrics {
		declared[mx.Key] = mx
	}
	for key, unit := range wantSitepulseNewMetricUnits {
		mx, ok := declared[key]
		if !ok {
			t.Errorf("the equipment profile does not declare %q", key)
			continue
		}
		if mx.DataType != "DOUBLE" || mx.Unit != unit {
			t.Errorf("metric %q is %s in %q, want DOUBLE in %q", key, mx.DataType, mx.Unit, unit)
		}
	}
}

// The plant: throughput as a DOUBLE and its running state as a BOOLEAN. A BOOLEAN metric is
// how the platform models a discrete on/off signal (stored 0/1); a StateChange event is the
// presence channel and device ingest refuses it, and a STRING metric is not storable.
// It declares no commands — it is not commanded — and is not a machine.
func TestSitepulsePlantProfileModelsThroughputAndRunningState(t *testing.T) {
	p := profileByToken(t, sitepulseManifest(t), "sp-plant-profile")
	declared := map[string]MetricSpec{}
	for _, mx := range p.Metrics {
		declared[mx.Key] = mx
	}
	if len(declared) != 2 {
		t.Fatalf("the plant profile declares %d metrics, want 2: %v", len(declared), declared)
	}
	if mx := declared["throughput_tph"]; mx.DataType != "DOUBLE" || mx.Unit != "t/h" {
		t.Errorf("throughput_tph is %+v, want DOUBLE in t/h", mx)
	}
	if mx := declared["plant_running"]; mx.DataType != "BOOLEAN" {
		t.Errorf("plant_running is %+v, want a BOOLEAN metric — never a STRING (not storable) "+
			"and never a StateChange (the presence channel, refused at device ingest)", mx)
	}
	if len(p.Commands) != 0 {
		t.Errorf("the plant profile declares commands %v; nothing commands the plant", p.Commands)
	}
	if len(p.DetectionRules) != 0 {
		t.Errorf("the plant profile declares %d rules, want none", len(p.DetectionRules))
	}
}

// ---- The S1 rules ---------------------------------------------------------------

// The profile carries exactly three rules, low-fuel first and unchanged.
func TestSitepulseProfileCarriesTheThreeRules(t *testing.T) {
	var tokens []string
	for _, r := range sitepulseProfile(t).DetectionRules {
		tokens = append(tokens, r.Token)
		if !r.Enabled {
			t.Errorf("rule %q is disabled, so it is published unchecked and never fires", r.Token)
		}
	}
	want := []string{"sp-rule-lowfuel", "sp-rule-overheat", "sp-rule-tyre-low"}
	if len(tokens) != len(want) {
		t.Fatalf("the equipment profile declares rules %v, want %v", tokens, want)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Errorf("rule[%d] is %q, want %q", i, tokens[i], want[i])
		}
	}
}

// Overheat: engine_temp_c ABOVE 105, critical, raise an alarm and nothing else. All values
// are contracts with the Unity player's thermal model, pinned as literals.
func TestSitepulseOverheatRuleFiresAbove105AsCriticalWithNoCommand(t *testing.T) {
	rule := ruleByToken(t, sitepulseProfile(t), "sp-rule-overheat")
	doc := decodeThresholdRule(t, rule.Definition)
	if rule.Metric != "engine_temp_c" || doc.When.Metric != "engine_temp_c" {
		t.Errorf("the overheat rule declares metric %q and reads %q, want engine_temp_c", rule.Metric, doc.When.Metric)
	}
	if doc.When.Op != "gt" || doc.When.Threshold != 105 {
		t.Errorf("the overheat rule fires on %s %v, want gt 105 — a hot engine is a metric running HIGH",
			doc.When.Op, doc.When.Threshold)
	}
	if doc.Severity != "critical" {
		t.Errorf("the overheat rule's authoring severity is %q, want critical", doc.Severity)
	}
	if len(doc.Actions) != 1 || doc.Actions[0].Type != actionTypeRaiseAlarm {
		t.Fatalf("the overheat rule renders actions %+v, want exactly one raiseAlarm and no sendCommand", doc.Actions)
	}
	if doc.Actions[0].RaiseAlarm.AlarmKey != "engine-overheat" {
		t.Errorf("the overheat rule raises %q, want engine-overheat", doc.Actions[0].RaiseAlarm.AlarmKey)
	}
}

// Tyre pressure is a DEPLETING quantity, so this is the second rule to fire BELOW its
// threshold: a gt here would alarm on healthy tyres and go quiet on a flat one.
func TestSitepulseTyrePressureRuleFiresBelow600AsMajorWithNoCommand(t *testing.T) {
	rule := ruleByToken(t, sitepulseProfile(t), "sp-rule-tyre-low")
	doc := decodeThresholdRule(t, rule.Definition)
	if rule.Metric != "tyre_pressure_kpa" || doc.When.Metric != "tyre_pressure_kpa" {
		t.Errorf("the tyre rule declares metric %q and reads %q, want tyre_pressure_kpa", rule.Metric, doc.When.Metric)
	}
	if doc.When.Op != "lt" || doc.When.Threshold != 600 {
		t.Errorf("the tyre rule fires on %s %v, want lt 600", doc.When.Op, doc.When.Threshold)
	}
	if doc.Severity != "major" {
		t.Errorf("the tyre rule's authoring severity is %q, want major", doc.Severity)
	}
	if len(doc.Actions) != 1 || doc.Actions[0].Type != actionTypeRaiseAlarm {
		t.Fatalf("the tyre rule renders actions %+v, want exactly one raiseAlarm and no sendCommand", doc.Actions)
	}
	if doc.Actions[0].RaiseAlarm.AlarmKey != "tyre-pressure-low" {
		t.Errorf("the tyre rule raises %q, want tyre-pressure-low", doc.Actions[0].RaiseAlarm.AlarmKey)
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
	if snap.Emitted != 0 || snap.Failed != 0 || snap.Shed != 0 || snap.Backpressured != 0 {
		t.Errorf("Tick moved the emit counters (emitted %d, failed %d, shed %d, backpressured %d)",
			snap.Emitted, snap.Failed, snap.Shed, snap.Backpressured)
	}
}
