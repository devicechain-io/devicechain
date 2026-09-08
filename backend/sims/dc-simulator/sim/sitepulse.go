// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
)

// sitepulse is the slice-4 heavy-equipment construction-site scenario
// (sim-slice4-sitepulse-spec.md), at its S0 size: ONE dozer, on a site of three
// zones, whose closed loop is
//
//	fuel_pct drains -> sp-rule-lowfuel fires below 15% -> raiseAlarm("low-fuel")
//	+ sendCommand("goto-refuel") -> the command reaches the machine over MQTT ->
//	it drives to the refuelling station and answers SUCCESSFUL
//
// 🔴 IT IS THE FIRST SCENARIO WHOSE DEVICE IS NOT IN THIS PROCESS. Every scenario
// before it is the "headless generator" archetype — the Go Tick IS the machine, and
// the presentation page merely watches what Tick already decided. Here a Unity
// player holds the device's own MQTT session under the device's own credential: it
// emits the telemetry, it receives the command, and it decides when the task was
// actually done. The Go runner is BOOTSTRAP ONLY. See Tick, which does nothing and
// says why.
//
// That inversion is what CommandFarEnd: FarEndExternal states, and picking it is not
// a formality — the other two settings are each actively wrong here, in opposite
// directions. FarEndNone would refuse a control surface the scenario is entirely
// about. FarEndInternal would attach a Go cmdreceiver per device that answers
// SUCCESSFUL the instant a command lands, for work only the player can do: the
// command channel would report a perfect round trip while the dozer sat still, which
// is the false success this whole seam exists to prevent, running backwards.
type sitepulse struct {
	// seed drives all deterministic generation (tokens, externalIds, credentials),
	// threaded from the handshake — see devicepulse's identical field for the
	// reset/idempotency rationale.
	//
	// It matters MORE here than in any previous scenario: the player binds its scene
	// objects by externalId and then authenticates with the credential Expand derives,
	// so (manifest, seed) reproducing identical VINs and bearers is what lets a scene
	// authored once keep working across a destroy/recreate.
	seed int64
	// load carries the run-time cadence/count overrides. Unlike widgetlab's this one
	// is honoured: the site is a scale knob, not a composed fixture.
	load Load
}

// Sitepulse sizing. S0 is deliberately the vertical at ONE machine — the same move
// devicepulse made — because every seam below it is net-new (emit from Unity, receive
// a command in Unity, bind by externalId) and one machine is the smallest topology
// that can be debugged when any of them misbehaves. S1 takes the population to 18
// across three device types; see the deliberate omissions below.
const sitepulseDozerCount = 1

// Sitepulse entity tokens — fixed, not derived from any handshake field, since this
// manifest is a static built-in scenario (mirrors the Devicepulse*/Buildingpulse*/
// Widgetlab* constants).
//
// They are named constants rather than literals in the manifest for the reason every
// scenario's are: a token is read from more than one place — the profile the device
// types resolve, the areas the goto-area enum is built from, the population a scene
// binds — and a literal repeated at each of those is a rename that succeeds partially
// and provisions a half-connected topology no gate objects to.
const (
	SitepulseProfileToken    = "sp-equipment-profile"
	SitepulseDeviceTypeToken = "sp-dozer"

	SitepulseCustomerTypeToken = "sp-contractor"
	SitepulseCustomerToken     = "acme-earthworks"
	SitepulseAreaTypeToken     = "sp-site-zone"
	SitepulseAssetTypeToken    = "sp-fixed-plant"

	// The refuelling station the low-fuel command sends a machine to. It is an ASSET
	// rather than an area because it is a thing standing in the yard, not a region of
	// the site — and it is provisioned even though nothing anchors an assignment to it
	// yet (asset-tier assignment targets are deferred; see PopulationSpec), because the
	// scene needs a platform-side entity to place it against.
	SitepulseRefuelStationToken = "sp-refuel-station"

	// The three site zones. They are the goto-area command's enum as well as the
	// population's DistributeAcross targets, which is why they are constants and why
	// the enum is DERIVED from the same list rather than typed out beside it.
	SitepulseZoneCutToken  = "sp-zone-cut"
	SitepulseZoneFillToken = "sp-zone-fill"
	SitepulseZoneYardToken = "sp-zone-yard"
)

// The machine's metric vocabulary — its capability contract, shared by every device
// type the scenario will grow (ADR-045).
//
// engine_hours is declared and read by NOTHING: no rule tests it and no widget binds
// it. That is intended rather than an oversight. A profile states what a machine can
// report, not what some rule happens to consume, and an odometer is part of what a
// piece of heavy plant reports; the player emits it from S0 so the metric exists on
// the wire before anything needs it.
const (
	SitepulseFuelKey        = "fuel_pct"
	SitepulseEngineTempKey  = "engine_temp_c"
	SitepulseEngineHoursKey = "engine_hours"
)

// The command vocabulary, and the rule that chains one of them.
//
// 🔴 TOKEN AND KEY ARE DIFFERENT STRINGS, and the distinction is the whole reason
// these are four constants rather than two. The Token is the CommandDefinition's own
// entity token (what the reconciler creates and looks up); the CommandKey is what a
// sendCommand action names and what command-delivery's enqueue gate resolves. Both are
// grammar-valid tokens, so naming the wrong one publishes perfectly clean and fails
// for the first time when the rule FIRES, several services away — which is exactly the
// seam Manifest.Validate's command cross-check closes, and this scenario is the first
// to exercise it.
const (
	SitepulseGotoCommandDef   = "sp-cmd-goto"
	SitepulseGotoCommandKey   = "goto-area"
	SitepulseGotoAreaParam    = "areaToken"
	SitepulseRefuelCommandDef = "sp-cmd-refuel"
	SitepulseRefuelCommandKey = "goto-refuel"

	SitepulseRuleToken = "sp-rule-lowfuel"
	SitepulseAlarmKey  = "low-fuel"
	sitepulseRuleName  = "Site Pulse low fuel"

	// The alarm tier. Both spellings live in wirevocabulary.go, which carries the whole
	// reasoning for why they are two constants and what each way of collapsing them
	// breaks — the canonical copy, not a summary of one.
	SitepulseSeverity          = SeverityMajor
	SitepulseAlarmSeverityWire = AlarmSeverityMajorWire
)

// SitepulseLowFuelThreshold is the fuel percentage the DETECT rule fires BELOW.
//
// 🔴 THE DIRECTION IS THE POINT. Every scenario before this one alarms on a metric
// running HIGH, and the rule builder hardcoded "gt" precisely because an inverted
// operator looks configured and silently alarms on the wrong half of the cycle. Fuel
// is a DEPLETING quantity, so this rule is the first to set Op: OpLt — a rule reading
// `gt 15` here would raise low-fuel on a nearly full tank and go silent on an empty
// one, with the manifest, the publish gate, the compiler and the alarm table all
// reporting success.
//
// It is a lone constant rather than half of a curve/threshold pair like widgetlab's
// and buildingpulse's, because there is no curve on this side to hold it against: the
// fuel level is generated by the Unity player, in another process and another
// language. The threshold is therefore the SHARED half of that contract, and the
// scene's drain rate is judged against it by the live acceptance run, not by a test in
// this module. Nothing here can close that seam; saying so is better than a test that
// pretends to.
const SitepulseLowFuelThreshold = 15.0

// sitepulseAreas is the site's three zones, freshly built per call.
//
// Fresh rather than a package-level slice because a manifest is handed out to callers
// (Provision, the fixture walks, a test) and a shared backing array makes one caller's
// accidental write everyone's. Same reason widgetlab builds its zones per call.
func sitepulseAreas() []AreaSpec {
	return []AreaSpec{
		{Token: SitepulseZoneCutToken, Name: "Excavation Face", AreaTypeToken: SitepulseAreaTypeToken},
		{Token: SitepulseZoneFillToken, Name: "Fill Ground", AreaTypeToken: SitepulseAreaTypeToken},
		{Token: SitepulseZoneYardToken, Name: "Equipment Yard", AreaTypeToken: SitepulseAreaTypeToken},
	}
}

// sitepulseGotoParameterSchema is sp-cmd-goto's parameter descriptors, as the JSON
// array the platform stores and the console's typed form renders.
//
// 🔴 THE ENUM IS DERIVED FROM THE AREAS, not typed out beside them, and that is not
// tidiness. The enum is the console's (and the scene's) whole idea of where a machine
// may be sent; the Areas are what the platform actually provisions. Written as two
// lists they drift on the first zone rename — and the failure is quiet in the worst
// way, because a goto-area naming a zone that no longer exists is a grammar-valid
// STRING that the enqueue gate accepts against the schema and the scene then cannot
// resolve to anywhere. Deriving makes the zone list the only thing to edit, which is
// also what makes S2's "add an enum value to the manifest, the scene's action UI
// changes with no scene code change" true by construction rather than by discipline.
//
// One parameter, unlike widgetlab's deliberately two-typed schema: widgetlab's job is
// to exercise every branch of the console's form renderer, and this scenario's is to
// move a machine.
var sitepulseGotoParameterSchema = buildSitepulseGotoParameterSchema()

func buildSitepulseGotoParameterSchema() string {
	zones := sitepulseAreas()
	tokens := make([]string, len(zones))
	for i, z := range zones {
		tokens[i] = z.Token
	}
	// The keys and the SCALAR/STRING vocabulary are device-management's ParameterSpec
	// json tags and its ADR-016 MetricDataType, mirrored by hand for the reason
	// everything in this module mirrors wire vocabulary: dc-simulator is deliberately an
	// untrusted external client and cannot import the owning type. Unlike a rule
	// definition this one is NOT gated by the authored-rules fixture — the fixture
	// records rule documents, not command schemas — so its only judge is
	// ValidateParameterSchema at create time, on a live cluster.
	raw, err := json.Marshal([]map[string]any{{
		"name":     SitepulseGotoAreaParam,
		"kind":     "SCALAR",
		"dataType": "STRING",
		"required": true,
		"enum":     tokens,
	}})
	if err != nil {
		// Marshaling a static map of strings cannot fail; a failure here is a programming
		// error, not a runtime condition, so fail at init rather than hand Provision a
		// command definition with no schema.
		panic(fmt.Sprintf("sitepulse: marshal %s parameter schema: %v", SitepulseGotoCommandDef, err))
	}
	return string(raw)
}

// NewSitepulse returns the construction-site Sim seeded from the handshake.
// Prefer NewSim, which validates load against the manifest.
func NewSitepulse(seed int64, load Load) Sim {
	return &sitepulse{seed: seed, load: load}
}

func (s *sitepulse) Manifest() SimManifest {
	return resize(s.load, SimManifest{
		Name: "sitepulse",
		Seed: s.seed,
		// A legitimate scale knob, unlike widgetlab: nothing here binds a named device
		// (no dashboard at S0, and the scene resolves whatever externalIds exist), so
		// `--devices` genuinely means "more machines" and S1 is this same manifest at a
		// larger count.
		FixedTopology: false,
		// The Unity player publishes this scenario's telemetry, so Sim.Tick emits
		// nothing (see Tick). Declared, because "resizable" is not "load-drivable": a
		// load harness drives Tick directly and reconciles what the DRIVER accepted, so
		// without this a run started against sitepulse — by a --manifest offering built
		// from the registry, or by a harness defaulting the id from the handshake —
		// would hold for its whole window, apply zero load, and fail the min-accepted
		// floor with a message about lost load flags.
		DevicesPublishTheirOwnTelemetry: true,
		// The Unity player IS the device — see the type comment for why the other two
		// modes are each actively wrong here. Validate enforces the half of the contract
		// this side owns: a far end with no command vocabulary is refused, because the
		// published CommandDefinitions are the entire contract between this manifest and
		// a process that cannot see it.
		CommandFarEnd: FarEndExternal,
		CustomerTypes: []CustomerTypeSpec{
			{Token: SitepulseCustomerTypeToken, Name: "Contractor"},
		},
		Customers: []CustomerSpec{
			{Token: SitepulseCustomerToken, Name: "Acme Earthworks", CustomerTypeToken: SitepulseCustomerTypeToken},
		},
		AreaTypes: []AreaTypeSpec{
			{Token: SitepulseAreaTypeToken, Name: "Site Zone"},
		},
		Areas: sitepulseAreas(),
		AssetTypes: []AssetTypeSpec{
			{Token: SitepulseAssetTypeToken, Name: "Fixed Plant"},
		},
		Assets: []AssetSpec{
			{Token: SitepulseRefuelStationToken, Name: "Refuelling Station", AssetTypeToken: SitepulseAssetTypeToken},
		},
		Profiles: []ProfileSpec{
			{
				Token:    SitepulseProfileToken,
				Name:     "Site Pulse Equipment Profile",
				Category: "vehicle",
				Metrics: []MetricSpec{
					{Key: SitepulseFuelKey, Name: "Fuel Level", DataType: "DOUBLE", Unit: "%"},
					{Key: SitepulseEngineTempKey, Name: "Engine Temperature", DataType: "DOUBLE", Unit: "C"},
					{Key: SitepulseEngineHoursKey, Name: "Engine Hours", DataType: "DOUBLE", Unit: "h"},
				},
				// Two commands, and only one of them is chained by a rule. goto-refuel is
				// argument-free ON PURPOSE: REACT's sendCommand payload is STATIC — frozen at
				// authoring time, with no templating and no expressions — so the platform can
				// say "go and refuel" but cannot compute "go to the nearest station". That is
				// the authority split doing its job rather than a limitation to design around:
				// the platform issues the task, the machine picks the route.
				//
				// goto-area is the parameterised twin, published at S0 even though no rule
				// sends it, because it is what S2's console command-button and the scene's own
				// action UI enqueue against — and because a profile's command vocabulary is
				// what an external far end SUBSCRIBES for, so a command that appears later is a
				// contract change to a client in another process.
				Commands: []CommandSpec{
					{
						Token:           SitepulseGotoCommandDef,
						CommandKey:      SitepulseGotoCommandKey,
						Name:            "Go To Area",
						ParameterSchema: sitepulseGotoParameterSchema,
					},
					{
						Token:      SitepulseRefuelCommandDef,
						CommandKey: SitepulseRefuelCommandKey,
						Name:       "Go Refuel",
					},
				},
				// The one rule, and the first in the tree to both ALARM and ACT.
				//
				// CommandKey is SitepulseRefuelCommandKey — the command's KEY, never its Token
				// and never its display Name. Manifest.Validate cross-checks it against this
				// same profile's declared keys, so a slip is a loud bootstrap error naming both
				// sides rather than a dead-letter on first firing.
				//
				// Enabled, because publish-time validation only gates ENABLED rules — a
				// disabled one is published unchecked and would make a broken predicate look
				// accepted.
				//
				// 🔴 ONE RULE, DELIBERATELY. The full scenario also carries an engine_temp_c
				// overheat rule at severity `critical`. It is not here because `critical` would
				// mean adding a second severity pair to wirevocabulary.go — an authoring
				// spelling and a wire spelling, each needing a consumer test in the module that
				// owns its enum — and S0's acceptance exercises neither. It lands with S1,
				// where a fleet makes a second tier worth showing.
				DetectionRules: []DetectionRuleSpec{
					ThresholdAlarmRule{
						Token:      SitepulseRuleToken,
						Name:       sitepulseRuleName,
						Metric:     SitepulseFuelKey,
						Op:         OpLt,
						Threshold:  SitepulseLowFuelThreshold,
						Severity:   SitepulseSeverity,
						AlarmKey:   SitepulseAlarmKey,
						CommandKey: SitepulseRefuelCommandKey,
						Enabled:    true,
					}.Spec(),
				},
			},
		},
		// ONE device type at S0. The full site has three (dozer/loader/hauler) over this
		// same profile — they differ in what a viewer sees and what the scene binds a
		// prefab to, not in what they can report — but three of anything at count=1 is
		// three ways for the same seam to fail rather than one. S1 adds the other two.
		DeviceTypes: []DeviceTypeSpec{
			{Token: SitepulseDeviceTypeToken, Name: "Site Pulse Dozer", ProfileToken: SitepulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType: SitepulseDeviceTypeToken,
				Count:  sitepulseDozerCount,
				// The externalId is the SCENE'S binding key (ADR-049): the player resolves
				// devicesByExternalId(["SP-DZ-0001"]) to the addressing token, then the
				// credential, and binds its dozer object to the result. So this pattern is a
				// published contract with another repo's authored scene, not an internal
				// naming choice — which is why it is padded to four digits it does not need at
				// count=1: widening it later would rename every machine the scene knows.
				TokenPattern:      "sp-dozer-{n:02d}",
				ExternalIdPattern: "SP-DZ-{n:04d}",
				// Round-robin across the three zones, which at count=1 puts the sole dozer in
				// the cut face and at S1's 18 lands six per zone with no remainder. It is the
				// device's INITIAL placement — the platform's record of where the machine is
				// anchored — not a live position; position is a LocationEvent the player emits.
				DistributeAcross: []string{"area"},
			},
		},
		// 🔴 NO DASHBOARD, DELIBERATELY. S0's acceptance criteria name none — they are
		// four platform-side facts (externalId resolves, telemetry lands, the alarm raises
		// and clears, the command reaches SUCCESSFUL), each read through GraphQL — and a
		// board would drag in the frontend/testdata/sim-dashboards fixture and its
		// TypeScript-side gate for a surface nothing in this slice looks at. S1's
		// acceptance asks for one (fleet fuel + alarms beside the 3D window) and that is
		// where it belongs. Note the shape this leaves BEHIND: Validate's control-widget
		// gate is about a board with no far end, so a scenario with a far end and no board
		// is unremarkable to it.
	})
}

func (s *sitepulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// Tick does nothing, and that is this scenario's design rather than an unfinished
// method.
//
// 🔴 READ THIS BEFORE "FIXING" IT BY ADDING AN EMITTER. sitepulse is the first
// scenario of the game/interactive archetype: the Unity player holds the device's own
// MQTT session under the device's own credential and publishes fuel_pct,
// engine_temp_c, engine_hours and its LocationEvents to
// `{instance}/{tenant}/devices/{deviceToken}/events` itself. The machine's state — how
// fast the tank drains, where it is on the haul road, whether it has finished the
// refuelling task — lives in the game loop, so the platform's view of it can only come
// from there. A Go emitter here would not be a fallback: it would publish a SECOND,
// contradicting reading for the same device, and the alarm would then be raised
// against telemetry the machine on screen knows nothing about — the low-fuel loop
// firing while the dozer's own tank is full.
//
// So this returns nil rather than emitting, and the consequence is that a running
// sitepulse reports an achieved rate of exactly zero on /status. That is expected and
// nothing treats it as a failure: the tick loop counts ticks and attributes sheds but
// asserts nothing about emits, Stats.Snapshot divides an unchanging numerator, and
// bootstrap's post-publish liveness assert is about detection rules being ACTIVE, not
// about telemetry having flowed. The honest reading of `emitted: 0` on this scenario is
// "the Go runner emitted nothing", which is true; whether the DEVICE is emitting is a
// question only event-management can answer, and the acceptance run asks it there.
//
// 🔴 THERE IS EXACTLY ONE PLACE A ZERO RATE IS A FAILURE, and an earlier version of
// this comment was WRONG about how anyone gets there. It claimed reaching it meant
// someone had deliberately pointed a measurement at a scenario that generates nothing.
// Two paths falsify that, and both arrive with nobody having chosen this scenario:
// cmd/loadtest-contention builds its --manifest offering FROM THE REGISTRY, and
// cmd/loadtest, -monitor and -selftest default the id from the HANDSHAKE — so
// `loadtest --handshake <a sitepulse sim>.json` drives it by simply pointing at the sim
// that already exists. The harness calls Sim.Bootstrap directly, bypassing the
// Lifecycle entirely, then holds for its whole window and fails the MinAccepted floor
// (loadtest/oracle.go's InvLoadApplied, default 1000) with a message about a job that
// lost its load flags — a true failure reported as the wrong cause.
//
// So "resizable" quietly stopped meaning "load-drivable" the moment this scenario
// registered, and the fix is a declaration rather than a narrower comment:
// DevicesPublishTheirOwnTelemetry on the manifest above, which
// loadtest.Profile.Validate refuses up front by name and which
// sim.LoadDrivableManifestIds keeps out of any tool's offering. The floor itself stays
// exactly as it is — a gate that applied no load must never report a clean result.
//
// The lifecycle still runs its loop over this no-op, which is deliberate: Start/Stop,
// the shed-transition logging and the tick accounting are the same code paths for every
// scenario, and special-casing a scenario out of the loop would make the one archetype
// that behaves differently ALSO the one whose lifecycle is untested.
func (s *sitepulse) Tick(context.Context, *Runtime) error {
	return nil
}
