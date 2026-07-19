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

// buildingpulse is the slice-2 reference scenario
// (sim-slice2-buildingpulse-spec.md): a building-automation topology — one
// customer, three buildings, one HVAC asset per building, and 12 thermostats
// distributed round-robin across the buildings and all assigned to both their
// building and the customer, so every measurement they emit carries an area
// anchor and a customer anchor (ADR-013/044). Each tick emits all four metrics
// in one Measurement; the temperature curve deterministically crosses 30 C,
// which the DETECT DetectionRule path will alarm on once rule seeding lands
// (alarm authoring moved off AlarmDefinition, ADR-057).
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
	return Provision(ctx, rt, s.Manifest())
}

// Tick emits all four metrics in one Measurement per device (EmitMeasurements,
// not one EmitMeasurement call per metric). Each device gets a distinct phase
// offset spaced evenly around a full sine period (2*pi / device count) so
// that, at any tick, the device closest to the sine peak is at most half that
// spacing away from it. With 12 evenly-spaced thermostats that worst case is
// 15 degrees from the peak: sin(90-15 degrees) = sin(75 degrees) is about
// 0.966, giving temperature = 24 + 8*0.966 is about 31.7 degrees C —
// comfortably over 30 C every cycle (the level a DETECT rule will alarm on
// once rule seeding lands), regardless of n, without ever needing every
// device in phase at once.
func (s *buildingpulse) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	if len(rt.Devices) == 0 {
		return nil
	}
	offset := 2 * math.Pi / float64(len(rt.Devices))

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(i int, _ DeviceInstance) map[string]float64 {
			phase := float64(n)*0.3 + float64(i)*offset
			return map[string]float64{
				BuildingpulseTemperatureKey: 24 + 8*math.Sin(phase),
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
