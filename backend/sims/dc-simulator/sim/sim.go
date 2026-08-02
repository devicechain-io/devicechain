// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// Sim is the headless Go reference driver for one scenario: it describes its
// topology (Manifest), provisions it once (Bootstrap), and advances one time
// step (Tick).
//
// IMPORTANT: this Go interface is the reference driver, NOT the wire contract
// (sim-subsystem-contract.md §3c). A future Unity (or any other language) sim
// implements the four wire seams directly — device-plane ingress, tenant
// GraphQL provisioning, the control API, and graphql-ws subscribe — and never
// needs to implement this interface. Two sims that never share a line of Go can
// still interoperate with the same platform because the contract lives on the
// wire, not in this type.
type Sim interface {
	// Manifest returns the scenario's declarative topology.
	Manifest() SimManifest
	// Bootstrap provisions the manifest's topology (idempotent — see Provision).
	Bootstrap(ctx context.Context, rt *Runtime) error
	// Tick advances the simulation by one step, typically emitting telemetry.
	Tick(ctx context.Context, rt *Runtime) error
}

// Devicepulse profile/device-type/population tokens — fixed, not derived from
// any handshake field, since this manifest is a static built-in scenario.
const (
	DevicepulseProfileToken    = "devicepulse-profile"
	DevicepulseDeviceTypeToken = "devicepulse-vehicle"
	DevicepulseMetricKey       = "speed_kph"
)

// devicepulse is the slice-1 MVP scenario (sim-slice1-devicepulse-spec.md): one
// device, one numeric metric, emitted every EmitInterval. It proves the whole
// loop — provisioning, ingress, and live read-back — with the smallest possible
// topology.
type devicepulse struct {
	// seed drives all deterministic generation (token/externalId/credential
	// derivation), threaded from the handshake so a given (sim, seed) always
	// renders the same topology and reset is reproducible.
	seed int64
	// load carries the run-time device-count/cadence overrides. A zero Load
	// leaves the scenario at its own demo sizing.
	load Load
	// ticks counts Tick calls since process start, driving a smooth synthetic
	// speed curve. Atomic since the control API and the tick loop are on
	// different goroutines even though only one ever calls Tick.
	ticks atomic.Int64
}

// NewDevicepulse returns the devicepulse reference Sim seeded from the
// handshake. Prefer NewSim, which validates load against the manifest.
func NewDevicepulse(seed int64, load Load) Sim {
	return &devicepulse{seed: seed, load: load}
}

func (s *devicepulse) Manifest() SimManifest {
	return resize(s.load, SimManifest{
		Name: "devicepulse",
		Seed: s.seed,
		Profiles: []ProfileSpec{
			{
				Token:    DevicepulseProfileToken,
				Name:     "Devicepulse Vehicle Profile",
				Category: "vehicle",
				Metrics: []MetricSpec{
					{Key: DevicepulseMetricKey, Name: "Speed", DataType: "DOUBLE", Unit: "kph"},
				},
			},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: DevicepulseDeviceTypeToken, Name: "Devicepulse Vehicle", ProfileToken: DevicepulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            DevicepulseDeviceTypeToken,
				Count:             1,
				TokenPattern:      "devicepulse-{n:05d}",
				ExternalIdPattern: "VIN-DEVICEPULSE-{n:05d}",
			},
		},
	})
}

func (s *devicepulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// Tick emits one speed_kph measurement per provisioned device. The value
// traces a smooth, bounded sine wave (30-70 kph) purely as a function of the
// tick count — a demo needs *something* moving on the presentation page, not
// physically accurate telemetry.
func (s *devicepulse) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	speed := 50 + 20*math.Sin(float64(n)*0.3)

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(int, DeviceInstance) map[string]float64 {
			return map[string]float64{DevicepulseMetricKey: speed}
		})
	if err != nil {
		log.Error().Err(err).Msg("emit measurement failed")
	}
	return err
}

// Buildingpulse sizing + tokens (sim-slice2-buildingpulse-spec.md): a modest
// demo topology, not a scale test — 3 buildings x 4 thermostats/building = 12
// thermostats total.
const (
	buildingpulseBuildingCount      = 3
	buildingpulseDevicesPerBuilding = 4
	buildingpulseThermostatCount    = buildingpulseBuildingCount * buildingpulseDevicesPerBuilding
)

// Buildingpulse profile/device-type/metric tokens — fixed, not derived from any
// handshake field, since this manifest is a static built-in scenario (mirrors
// the Devicepulse* constants above).
const (
	BuildingpulseProfileToken    = "bp-thermostat-profile"
	BuildingpulseDeviceTypeToken = "bp-thermostat"
	BuildingpulseTemperatureKey  = "temperature"
	BuildingpulseHumidityKey     = "humidity"
	BuildingpulseSetpointKey     = "setpoint"
	BuildingpulseCO2Key          = "co2"
)

// ---- The temperature curve and the threshold it has to cross ----------------
//
// One design object, the same arrangement widgetlab's sweep uses and for the same
// reason: the DETECT rule's threshold and the curve that must cross it are a single
// decision, so neither side owns the number alone. Written as separate literals they
// drift independently — Tick changes its amplitude, the rule keeps its threshold, both
// changes look local and correct, and the demo simply stops raising alarms with every
// test still green.
//
// Tick reads these; the rule below reads the threshold; and the tests check the
// threshold against the curve's own bounds AND against what actually reaches the wire.
const (
	// The sine's centre and amplitude, in degrees C, so the curve spans
	// [centre-amplitude, centre+amplitude] = [16, 32].
	BuildingpulseTempCentre    = 24.0
	BuildingpulseTempAmplitude = 8.0
	// How far each device's phase advances per tick. A full period is therefore
	// 2*pi/step ≈ 21 ticks — at the demo cadence, a raise and a clear every couple of
	// minutes, which is what makes the alarm widgets worth watching rather than a
	// single event someone has to catch.
	BuildingpulsePhaseStep = 0.3
	// The temperature the DETECT rule alarms above. STRICTLY inside the curve's bounds,
	// which is what makes a raise AND a clear inevitable rather than hoped-for;
	// TestBuildingpulseThresholdLiesStrictlyInsideTheCurve holds it there.
	BuildingpulseAlarmThreshold = 30.0
)

// The alarm the buildingpulse rule raises. Both severity spellings live in
// wirevocabulary.go, which carries the reasoning for why they are two constants and
// what each way of collapsing them breaks.
const (
	BuildingpulseRuleToken         = "bp-over-temp"
	BuildingpulseAlarmKey          = "bp-over-temp"
	BuildingpulseSeverity          = SeverityMajor
	BuildingpulseAlarmSeverityWire = AlarmSeverityMajorWire
	buildingpulseRuleName          = "Building Pulse over-temperature"
)

// buildingpulseCoverageFloor is the smallest population that keeps SOME thermostat above
// the alarm threshold at every tick.
//
// The thermostats are spread evenly around one temperature cycle, so the worst tick is
// the one where the sine peak falls midway between two neighbours — putting the hottest
// device pi/n away from the peak, and the population's maximum at
// centre + amplitude*cos(pi/n). Coverage holds exactly while that exceeds the threshold.
//
// It lives in non-test code because it is an OPERATOR-facing number: Bootstrap warns
// below it, and a README that told a reader to go and look up a test name for it would
// be documenting the wrong surface. The test then confirms this same formula against the
// wire, so the independent check now guards the production number rather than a copy.
func buildingpulseCoverageFloor() int {
	for n := 1; n <= buildingpulseThermostatCount; n++ {
		if BuildingpulseTempCentre+BuildingpulseTempAmplitude*math.Cos(math.Pi/float64(n)) >
			BuildingpulseAlarmThreshold {
			return n
		}
	}
	// Unreachable while the threshold lies strictly inside the curve, which
	// TestBuildingpulseThresholdLiesStrictlyInsideTheCurve holds it to. Reported as the
	// full population rather than 0 so a caller comparing against it cannot read "no
	// floor" as "any size is fine".
	return buildingpulseThermostatCount
}

// buildingpulse is the slice-2 reference scenario
// (sim-slice2-buildingpulse-spec.md): a building-automation topology — one
// customer, three buildings, one HVAC asset per building, and 12 thermostats
// distributed round-robin across the buildings and all assigned to both their
// building and the customer, so every measurement they emit carries an area
// anchor and a customer anchor (ADR-013/044). Each tick emits all four metrics
// in one Measurement; the temperature curve deterministically crosses
// BuildingpulseAlarmThreshold and falls back below it, which the profile's DETECT
// rule raises and clears an alarm on (alarm authoring lives on DetectionRule, not
// AlarmDefinition — ADR-057).
//
// That rule is why this scenario now needs event-processing: Provision refuses to finish
// unless every enabled rule it publishes is confirmed live in the engine
// (verifyDetectionRulesAreLive), so buildingpulse cannot bootstrap on a deployment
// profile that omits event-processing. Deliberate; the README says why.
type buildingpulse struct {
	// seed drives all deterministic generation, threaded from the handshake —
	// see devicepulse's identical field for the reset/idempotency rationale.
	seed int64
	// load carries the run-time device-count/cadence overrides, same role as
	// devicepulse's.
	load Load
	// ticks counts Tick calls since process start, same role as devicepulse's.
	ticks atomic.Int64
}

// NewBuildingpulse returns the buildingpulse reference Sim seeded from the
// handshake. Prefer NewSim, which validates load against the manifest.
func NewBuildingpulse(seed int64, load Load) Sim {
	return &buildingpulse{seed: seed, load: load}
}

func (s *buildingpulse) Manifest() SimManifest {
	areas := make([]AreaSpec, buildingpulseBuildingCount)
	assets := make([]AssetSpec, buildingpulseBuildingCount)
	for i := 0; i < buildingpulseBuildingCount; i++ {
		n := i + 1
		areas[i] = AreaSpec{
			Token:         fmt.Sprintf("bp-bldg-%02d", n),
			Name:          fmt.Sprintf("Building %d", n),
			AreaTypeToken: "bp-building",
		}
		assets[i] = AssetSpec{
			Token:          fmt.Sprintf("bp-hvac-%02d", n),
			Name:           fmt.Sprintf("HVAC Unit %d", n),
			AssetTypeToken: "bp-hvac",
		}
	}

	manifest := SimManifest{
		Name: "buildingpulse",
		Seed: s.seed,
		CustomerTypes: []CustomerTypeSpec{
			{Token: "bp-facility-owner", Name: "Facility Owner"},
		},
		Customers: []CustomerSpec{
			{Token: "bp-acme", Name: "Acme Properties", CustomerTypeToken: "bp-facility-owner"},
		},
		AreaTypes: []AreaTypeSpec{
			{Token: "bp-building", Name: "Building"},
		},
		Areas: areas,
		AssetTypes: []AssetTypeSpec{
			{Token: "bp-hvac", Name: "HVAC Unit"},
		},
		Assets: assets,
		Profiles: []ProfileSpec{
			{
				Token:    BuildingpulseProfileToken,
				Name:     "Building Pulse Thermostat Profile",
				Category: "sensor",
				Metrics: []MetricSpec{
					{Key: BuildingpulseTemperatureKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"},
					{Key: BuildingpulseHumidityKey, Name: "Humidity", DataType: "DOUBLE", Unit: "%"},
					{Key: BuildingpulseSetpointKey, Name: "Setpoint", DataType: "DOUBLE", Unit: "C"},
					{Key: BuildingpulseCO2Key, Name: "CO2", DataType: "DOUBLE", Unit: "ppm"},
				},
				// The rule whose raiseAlarm fills the dashboard's alarm-table. It is
				// profile-version content (ADR-045), so Provision creates it before it
				// publishes the version. Enabled, because publish-time validation only
				// gates ENABLED rules — a disabled one is published unchecked and would
				// make a broken predicate look accepted.
				DetectionRules: []DetectionRuleSpec{
					ThresholdAlarmRule{
						Token:     BuildingpulseRuleToken,
						Name:      buildingpulseRuleName,
						Metric:    BuildingpulseTemperatureKey,
						Threshold: BuildingpulseAlarmThreshold,
						Severity:  BuildingpulseSeverity,
						AlarmKey:  BuildingpulseAlarmKey,
						Enabled:   true,
					}.Spec(),
				},
			},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: BuildingpulseDeviceTypeToken, Name: "Building Pulse Thermostat", ProfileToken: BuildingpulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            BuildingpulseDeviceTypeToken,
				Count:             buildingpulseThermostatCount,
				TokenPattern:      "bp-therm-{n:03d}",
				ExternalIdPattern: "AST-{n:05d}",
				DistributeAcross:  []string{"area"},
			},
		},
	}

	// Resize BEFORE expanding, so the dashboard below is built from the topology
	// this scenario will actually run.
	//
	// Today the two orders happen to agree: the dashboard binds devices[0], and
	// device 1 exists at every count >= 1. That equivalence is a property of the
	// current dashboard, not of the code — a hero device picked from the END of
	// the population would silently bind to a device a smaller run does not have.
	// Ordering it correctly costs nothing and does not depend on that holding.
	// (Manifest.Validate does NOT check dashboard bindings, so nothing downstream
	// would catch it.)
	manifest = resize(s.load, manifest)

	// The dashboard binds to a concrete device token, so it is built here from
	// this same manifest's own (pure, deterministic) Expand() output — not
	// handed rt.Devices later — keeping Manifest() a single source of truth
	// with no dependency on Provision having already run.
	devices := manifest.Expand(manifest.Seed)
	definition, err := buildBuildingpulseDashboard(devices)
	if err != nil {
		// buildBuildingpulseDashboard only fails marshaling static, well-typed Go
		// structs — a failure here means a programming bug in dashboard.go, not a
		// runtime condition, so fail loudly rather than silently handing
		// Provision a manifest with a missing dashboard.
		panic(fmt.Sprintf("buildingpulse: build dashboard definition: %v", err))
	}
	manifest.Dashboards = []DashboardSpec{
		{
			Token:       "bp-dashboard",
			Name:        "Building Pulse",
			Description: "Temperature, latest values, and live alarms for the Building Pulse demo scenario.",
			Definition:  definition,
		},
	}
	return manifest
}

func (s *buildingpulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	manifest := s.Manifest()
	if err := Provision(ctx, rt, manifest); err != nil {
		return err
	}
	warnIfBuildingpulseIsTooSmallToWatch(manifest, len(rt.Devices))
	return nil
}

// warnIfBuildingpulseIsTooSmallToWatch reports the two ways a `--devices` override
// leaves the demo's alarm story degraded. Both are WARNINGS, not refusals: every device
// still raises and still clears at any size, so the scenario is correct — just less
// worth watching. (Contrast FixedTopology, which refuses a resize outright, because
// there resizing is MEANINGLESS rather than merely disappointing.)
//
// Worded as the consequence rather than as the flag that was passed, matching the
// far-end warning in main.go: the reader needs to know what they will see, not what
// they typed.
func warnIfBuildingpulseIsTooSmallToWatch(manifest SimManifest, devices int) {
	// The harsher of the two, and it is not a matter of degree: a building with no
	// thermostats can never show an alarm, so a board scoped to it is empty forever and
	// no rule can change that.
	if buildings := len(manifest.Areas); devices < buildings {
		log.Warn().Int("devices", devices).Int("buildings", buildings).
			Msg("fewer thermostats than buildings: a dashboard scoped to a building with none " +
				"will show an empty alarm table permanently, whatever the detection rule does")
	}
	if floor := buildingpulseCoverageFloor(); devices < floor {
		log.Warn().Int("devices", devices).Int("floor", floor).
			Msg("too few thermostats to keep one above the alarm threshold at all times: the " +
				"tenant-wide view will have moments with no active alarm. Every device still " +
				"raises and clears, so this is a quieter demo rather than a broken one")
	}
}

// Tick emits all four metrics in one Measurement per device (EmitMeasurements,
// not one EmitMeasurement call per metric). Each device gets a distinct phase
// offset spaced evenly around a full sine period (2*pi / device count), so the
// thermostats reach their peaks at staggered times rather than breathing in
// unison — alarms come and go across the population instead of all at once.
//
// 🔴 TWO PROPERTIES, AND ONLY ONE OF THEM HOLDS AT EVERY POPULATION SIZE.
//
//   - EVERY DEVICE RAISES AND CLEARS, at any size. Each device's own phase advances
//     BuildingpulsePhaseStep per tick, so it traverses a full period by itself; the
//     offset shifts WHEN it crosses, never WHETHER. This is the property the alarm
//     channel actually rests on, and TestBuildingpulseEmittedTemperatureCrossesAndClears
//     PerDevice drives it out of this function onto the wire.
//   - "SOME DEVICE IS ABOVE THE THRESHOLD AT EVERY TICK" IS SIZE-DEPENDENT, and an
//     earlier version of this comment claimed it held "regardless of n". It does not.
//     In the worst tick the peak falls midway between two neighbours, so the hottest
//     device sits at centre + amplitude*cos(pi/n): at the demo's 12 that is ~31.7 C,
//     but at n=4 it is ~29.7 C — under the threshold, and nobody is hot. The floor is
//     n >= 5 for the shipped constants (TestBuildingpulseWholePopulationCoverageHasA
//     StatedFloor derives it rather than restating the 5).
//
// Below that floor a `--devices 2` run still bootstraps, still raises and still clears
// — there are simply moments with no ACTIVE alarm anywhere. That is a degraded DEMO,
// not a broken scenario, which is why it is documented rather than refused.
func (s *buildingpulse) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	if len(rt.Devices) == 0 {
		return nil
	}
	offset := 2 * math.Pi / float64(len(rt.Devices))

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(i int, _ DeviceInstance) map[string]float64 {
			phase := float64(n)*BuildingpulsePhaseStep + float64(i)*offset
			return map[string]float64{
				// Read from the shared const block, not written as literals: this is the
				// curve the rule's threshold is chosen against.
				BuildingpulseTemperatureKey: BuildingpulseTempCentre + BuildingpulseTempAmplitude*math.Sin(phase),
				BuildingpulseHumidityKey:    45 + 10*math.Sin(phase+1),
				BuildingpulseSetpointKey:    22,
				BuildingpulseCO2Key:         600 + 150*math.Sin(phase+2),
			}
		})
	if err != nil {
		log.Error().Err(err).Msg("emit measurements failed")
	}
	return err
}

// Registry maps a manifest id (Handshake.ManifestId) to its Sim constructor.
// main.go looks up the handshake's ManifestId here to pick a driver, defaulting
// to "devicepulse" when the field is empty (a pre-slice-2 handshake never set
// it) so existing sim records keep working unchanged.
var Registry = map[string]func(int64, Load) Sim{
	"devicepulse":   NewDevicepulse,
	"buildingpulse": NewBuildingpulse,
	"widgetlab":     NewWidgetlab,
}

// ManifestIds returns the Registry's known manifest ids, sorted — a single
// source for user-facing "known ids" messaging (e.g. main.go's unknown-id
// error) so a third scenario doesn't leave a stale hardcoded list behind.
func ManifestIds() []string {
	ids := make([]string, 0, len(Registry))
	for id := range Registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ResizableManifestIds returns the manifest ids a device-count override applies
// to — every registered scenario except the composed fixtures, which refuse one
// (see SimManifest.FixedTopology).
//
// It exists because "the scenarios that exist" and "the scenarios a load tool can
// drive" stopped being the same set. A tool that sizes its own runs must offer
// THIS list: naming a fixed-topology scenario in help text advertises a run that
// fails at startup, which is a worse form of the stale list that deriving from
// the registry was meant to fix.
func ResizableManifestIds() []string {
	ids := make([]string, 0, len(Registry))
	for id, newDriver := range Registry {
		if !newDriver(1, Load{}).Manifest().FixedTopology {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
