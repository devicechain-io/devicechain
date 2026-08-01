// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// widgetlab is the slice-3 widget-exerciser scenario
// (sim-slice3-widget-exerciser-spec.md): a topology whose purpose is to put
// EVERY dashboard widget type on a board with data that actually exercises it,
// so someone about to author a dashboard can see the palette rather than read
// about it. buildingpulse remains the sellable demo; this is the catalog.
//
// It provisions two device types over ONE shared profile (a profile is a shared
// capability contract, ADR-045): nominal sensors that trace the sweep below, and
// "edge" sensors the pathological lane will drive into the states a widget
// renders badly — no data, a spike that blows an axis, a device that goes silent.
// They are separate DEVICE TYPES rather than a token-prefix convention so a
// generator classifies a device structurally, off DeviceInstance.DeviceTypeToken,
// instead of guessing from its name.
//
// BUILD STATE: lanes L0 (topology, tokens, the shared sweep), L1 (the DETECT rule
// and command definition) and L2 (the two boards, in widgetlab_dashboard.go). The
// per-channel generators (L3) land on top: until then every device traces the same
// nominal sweep, so the stress board is wired but not yet fed anything hostile.
type widgetlab struct {
	// seed drives all deterministic generation, threaded from the handshake —
	// see devicepulse's identical field for the reset/idempotency rationale.
	seed int64
	// load carries the run-time cadence overrides. Its DeviceCount override is
	// REFUSED for this scenario — see FixedTopology on the manifest below.
	load Load
	// ticks counts Tick calls since process start, driving the sweep. Atomic for
	// the same reason as devicepulse's.
	ticks atomic.Int64
}

// Widgetlab sizing. Small and hand-composed: the boards bind specific devices in
// specific zones, so these counts are part of the fixture, not a scale knob.
const (
	// Two zones so the entity-selector's ROOT slot has more than one candidate —
	// with one zone a rebind is unobservable, and the widget proves nothing.
	widgetlabZoneCount = 2
	// Four nominal sensors, round-robin across the zones, so each zone holds two
	// and the SCOPED child slot also has a choice to make.
	widgetlabSensorCount = 4
	// Three edge sensors, one per pathological behaviour the stress board shows.
	widgetlabEdgeCount = 3
)

// Widgetlab profile/device-type/metric tokens — fixed, not derived from any
// handshake field, since this manifest is a static built-in scenario (mirrors the
// Devicepulse*/Buildingpulse* constants).
const (
	WidgetlabProfileToken        = "wl-sensor-profile"
	WidgetlabDeviceTypeToken     = "wl-sensor"
	WidgetlabEdgeDeviceTypeToken = "wl-edge-sensor"

	WidgetlabTemperatureKey = "temperature"
	WidgetlabHumidityKey    = "humidity"
	WidgetlabPressureKey    = "pressure"
	WidgetlabBatteryKey     = "battery"

	// The two boards. Coverage is asserted on the gallery; the stress board
	// carries a curated set bound to the edge sensors.
	WidgetlabGalleryDashboardToken = "wl-gallery"
	WidgetlabStressDashboardToken  = "wl-stress"
)

// ---- The sweep: ONE design object, shared by the rule and the generator ------
//
// The DETECT rule's threshold and the curve that has to cross it are a single
// design decision, so they live in one place and are checked against each other
// by a test. Split across two lanes they drift independently — the rule moves its
// threshold, the generator changes its amplitude, both PRs stay green, and alarms
// simply never raise. That is the ADR-062 seam-bug shape, and it is invisible to
// any test that looks at only one side.
//
// Everything downstream reads these: L1's rule predicate, L2's gauge min/max,
// L3's generator, and L5's render gate.
const (
	// The sweep's bounds, in degrees C. A gauge's min/max are authored from these,
	// so the needle visits both ends of its own scale.
	WidgetlabSweepMin = 15.0
	WidgetlabSweepMax = 35.0
	// One full sweep (min → max → min) in ticks. At the default emit interval this
	// is a period a person can watch a value climb through, not a blur. MUST be
	// even, or the peak falls between ticks and the sweep never reaches its own
	// maximum — pinned by TestWidgetlabSweepReachesBothEnds.
	WidgetlabSweepTicks = 90
	// The temperature the DETECT rule alarms above. Strictly inside the sweep's
	// bounds, which is what makes a raise AND a clear inevitable rather than
	// hoped-for; TestWidgetlabSweepCrossesAndClearsTheThreshold holds it there.
	WidgetlabAlarmThreshold = 30.0
)

// The alarm the rule raises and the command the button issues. Both are read from
// TWO sides — the rule/definition authored here, and the widget options L2 bakes —
// so they are constants for the same reason the threshold is: a command-button
// whose commandName misses the authored definition looks configured, enqueues
// nothing the profile's vocabulary resolves, and fails at the delivery boundary.
const (
	WidgetlabRuleToken  = "wl-over-temp"
	WidgetlabAlarmKey   = "wl-over-temp"
	WidgetlabCommandDef = "wl-set-setpoint-cmd"
	WidgetlabCommandKey = "setSetpoint"

	// 🔴 TWO severities, and they are not interchangeable.
	//
	// WidgetlabSeverity is the RULE AUTHORING vocabulary, which is lowercase
	// (event-processing's rules.Severity). WidgetlabAlarmSeverityWire is the same
	// tier as it lands on the durable alarm row, because the raise-alarm consumer
	// uppercases the authoring severity — and it is the wire form an alarm widget's
	// `severity` filter option is written in.
	//
	// Collapsing them into one uppercase constant is not a cosmetic mistake: the
	// rule compiler rejects "MAJOR" outright, so with the ADR-044 publish gate wired
	// the profile fails to publish and the scenario cannot bootstrap at all, and
	// with the gate unwired the version publishes while event-processing drops the
	// rule at load with only a log line — the alarm the whole board depends on
	// silently never fires. loadtest/detection.go carries the same pair for the same
	// reason; this port collapsed them and had to be corrected.
	WidgetlabSeverity          = "major"
	WidgetlabAlarmSeverityWire = "MAJOR"
)

// widgetlabParameterSchema is the command's parameter descriptors, as the JSON
// array the platform stores and a command-button widget bakes in.
//
// Two parameters of DIFFERENT types on purpose: the console's typed form renders
// a numeric input with bounds for one and a select for the other, so a single
// command exercises both branches. A one-parameter command would leave most of
// the form unexercised, which is the whole reason the widget is on the board.
const widgetlabParameterSchema = `[` +
	`{"name":"target","kind":"SCALAR","dataType":"DOUBLE","unit":"C","required":true,"minValue":15,"maxValue":35},` +
	`{"name":"mode","kind":"SCALAR","dataType":"STRING","required":false,"enum":["heat","cool","auto"],"default":"auto"}` +
	`]`

// widgetlabRuleDefinition is the opaque rules.Rule JSON: a threshold rule whose
// predicate reads the swept metric and whose raiseAlarm action drives the alarm
// widgets. The keys are exactly the rules.Rule / Condition / Action json tags —
// event-processing decodes with DisallowUnknownFields, so a stray one is rejected
// at publish. A raiseAlarm action requires a non-empty severity.
//
// The threshold is WidgetlabAlarmThreshold rather than a literal, which is the
// entire point of the const block above: the rule and the curve that must cross it
// cannot drift apart if neither owns the number.
func widgetlabRuleDefinition() string {
	raw, err := json.Marshal(map[string]any{
		"name":     "Widget Lab over-temperature",
		"type":     "threshold",
		"severity": WidgetlabSeverity,
		"when": map[string]any{
			"metric":    WidgetlabTemperatureKey,
			"op":        "gt",
			"threshold": WidgetlabAlarmThreshold,
		},
		"actions": []any{
			map[string]any{
				"type":       "raiseAlarm",
				"raiseAlarm": map[string]any{"alarmKey": WidgetlabAlarmKey},
			},
		},
	})
	if err != nil {
		// Marshaling a static map of strings and floats cannot fail; a failure here
		// is a programming error, not a runtime condition, and the same house style
		// as buildingpulse's panic on a dashboard it cannot marshal.
		panic(fmt.Sprintf("widgetlab: marshal rule definition: %v", err))
	}
	return string(raw)
}

// widgetlabSweep is the nominal temperature at a given tick: a triangle wave
// between WidgetlabSweepMin and WidgetlabSweepMax with period WidgetlabSweepTicks.
//
// Triangle rather than sine deliberately. A sine loiters near its extremes and
// races through the middle, so the samples cluster at the ends and the crossing
// of a threshold is brief; a triangle visits every value in its range for equal
// time, which is what a widget catalog wants — the gauge sweeps its scale evenly
// and the chart draws a shape whose slope is readable.
//
// Pure in tick: no clock, no random. The same tick always yields the same value,
// so a fixture, a test and a live run agree on what tick N looks like. Note the
// tick counter is process-local, so a restart REPLAYS the curve from the start
// rather than resuming mid-sweep — purity buys reproducibility, not continuity.
func widgetlabSweep(tick int64) float64 {
	// Go's % keeps the sign of the dividend, so a negative tick would fold the
	// wave backwards past zero. Normalise first — cheap, and it makes the
	// function total rather than "correct for the ticks we happen to pass".
	phase := ((tick % WidgetlabSweepTicks) + WidgetlabSweepTicks) % WidgetlabSweepTicks
	// Rise over the first half, fall over the second: 0 → 1 → 0.
	var ramp float64
	if half := int64(WidgetlabSweepTicks / 2); phase < half {
		ramp = float64(phase) / float64(half)
	} else {
		ramp = float64(WidgetlabSweepTicks-phase) / float64(half)
	}
	return WidgetlabSweepMin + ramp*(WidgetlabSweepMax-WidgetlabSweepMin)
}

// NewWidgetlab returns the widget-exerciser Sim seeded from the handshake.
// Prefer NewSim, which validates load against the manifest.
func NewWidgetlab(seed int64, load Load) Sim {
	return &widgetlab{seed: seed, load: load}
}

func (s *widgetlab) Manifest() SimManifest {
	zones := make([]AreaSpec, widgetlabZoneCount)
	assets := make([]AssetSpec, widgetlabZoneCount)
	for i := 0; i < widgetlabZoneCount; i++ {
		n := i + 1
		zones[i] = AreaSpec{
			Token:         fmt.Sprintf("wl-zone-%02d", n),
			Name:          fmt.Sprintf("Zone %d", n),
			AreaTypeToken: "wl-zone",
		}
		assets[i] = AssetSpec{
			Token:          fmt.Sprintf("wl-rig-%02d", n),
			Name:           fmt.Sprintf("Test Rig %d", n),
			AssetTypeToken: "wl-rig",
		}
	}

	// Routed through resize like every other scenario even though a fixed topology
	// can never BE resized. That is the point: resize panics on the refusal, which
	// is the house contract for a driver built by calling New* directly with an
	// illegal load. Returning the manifest without it would make this the one
	// scenario that silently ignores a device count instead of failing loudly.
	manifest := resize(s.load, SimManifest{
		Name: "widgetlab",
		Seed: s.seed,
		// The boards bind named devices in named zones, so resizing this scenario
		// would leave a dashboard pointing at a device that no longer exists.
		FixedTopology: true,
		CustomerTypes: []CustomerTypeSpec{
			{Token: "wl-operator", Name: "Operator"},
		},
		Customers: []CustomerSpec{
			{Token: "wl-demo-co", Name: "Widget Lab", CustomerTypeToken: "wl-operator"},
		},
		AreaTypes: []AreaTypeSpec{
			{Token: "wl-zone", Name: "Zone"},
		},
		Areas: zones,
		AssetTypes: []AssetTypeSpec{
			{Token: "wl-rig", Name: "Test Rig"},
		},
		Assets: assets,
		Profiles: []ProfileSpec{
			{
				Token:    WidgetlabProfileToken,
				Name:     "Widget Lab Sensor Profile",
				Category: "sensor",
				// Four metrics, chosen so the widgets have something to differ
				// about: temperature drives the sweep and the alarm, battery is a
				// natural 0-100 gauge, and having four at all gives the `table`
				// widget more than one row to render.
				Metrics: []MetricSpec{
					{Key: WidgetlabTemperatureKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"},
					{Key: WidgetlabHumidityKey, Name: "Humidity", DataType: "DOUBLE", Unit: "%"},
					{Key: WidgetlabPressureKey, Name: "Pressure", DataType: "DOUBLE", Unit: "kPa"},
					{Key: WidgetlabBatteryKey, Name: "Battery", DataType: "DOUBLE", Unit: "%"},
				},
				// The command a command-button issues, and the rule whose raiseAlarm
				// drives alarm-table and alarm-count. Both are profile-version
				// content, so Provision creates them before it publishes.
				Commands: []CommandSpec{
					{
						Token:           WidgetlabCommandDef,
						CommandKey:      WidgetlabCommandKey,
						Name:            "Set Setpoint",
						ParameterSchema: widgetlabParameterSchema,
					},
				},
				DetectionRules: []DetectionRuleSpec{
					{
						Token:      WidgetlabRuleToken,
						Name:       "Widget Lab over-temperature",
						Definition: widgetlabRuleDefinition(),
						Metric:     WidgetlabTemperatureKey,
						// Enabled, because publish-time validation only gates ENABLED
						// rules — a disabled rule is published unchecked and would make
						// a broken predicate look accepted.
						Enabled: true,
					},
				},
			},
		},
		// Both device types share the one profile: they differ in how they BEHAVE,
		// not in what they can report, and a widget bound to either reads the same
		// metric vocabulary.
		DeviceTypes: []DeviceTypeSpec{
			{Token: WidgetlabDeviceTypeToken, Name: "Widget Lab Sensor", ProfileToken: WidgetlabProfileToken},
			{Token: WidgetlabEdgeDeviceTypeToken, Name: "Widget Lab Edge Sensor", ProfileToken: WidgetlabProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            WidgetlabDeviceTypeToken,
				Count:             widgetlabSensorCount,
				TokenPattern:      "wl-sensor-{n:02d}",
				ExternalIdPattern: "WL-SENSOR-{n:04d}",
				DistributeAcross:  []string{"area"},
			},
			{
				OfType:            WidgetlabEdgeDeviceTypeToken,
				Count:             widgetlabEdgeCount,
				TokenPattern:      "wl-edge-{n:02d}",
				ExternalIdPattern: "WL-EDGE-{n:04d}",
				DistributeAcross:  []string{"area"},
			},
		},
	})

	// The boards bind concrete device tokens, so they are built from this same
	// manifest's own (pure, deterministic) Expand output rather than handed
	// rt.Devices later — keeping Manifest a single source of truth with no
	// dependency on Provision having already run. Resize is applied above first, so
	// the boards are built from the topology this scenario will actually run.
	devices := manifest.Expand(manifest.Seed)
	gallery, err := buildWidgetlabGallery(devices)
	if err != nil {
		// The builders only fail on a malformed topology (no device of a declared
		// type, a device with no area) or on marshaling static structs — programming
		// errors, not runtime conditions, so fail loudly rather than handing
		// Provision a manifest with a missing board.
		panic(fmt.Sprintf("widgetlab: build gallery dashboard: %v", err))
	}
	stress, err := buildWidgetlabStress(devices)
	if err != nil {
		panic(fmt.Sprintf("widgetlab: build stress dashboard: %v", err))
	}
	manifest.Dashboards = []DashboardSpec{
		{
			Token:       WidgetlabGalleryDashboardToken,
			Name:        "Widget Lab",
			Description: "One of every dashboard widget, with data that exercises each of them.",
			Definition:  gallery,
		},
		{
			Token:       WidgetlabStressDashboardToken,
			Name:        "Widget Lab — Stress",
			Description: "The same widgets fed deliberately pathological data.",
			Definition:  stress,
		},
	}
	return manifest
}

func (s *widgetlab) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// Tick emits one measurement per device, all four metrics together.
//
// L0 drives every device from the same sweep, including the edge sensors: the
// pathological lane (L3) is what makes them differ, and giving them a half-built
// behaviour here would be a fixture nobody designed. What this DOES establish is
// that the temperature every device reports is the shared sweep — the same
// function the DETECT rule's threshold is checked against.
func (s *widgetlab) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	temperature := widgetlabSweep(n)

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(int, DeviceInstance) map[string]float64 {
			return map[string]float64{
				WidgetlabTemperatureKey: temperature,
				// Offset by a quarter period so the two curves are visibly
				// different on a multi-series chart rather than one line hiding
				// another.
				WidgetlabHumidityKey: 40 + 20*(widgetlabSweep(n+WidgetlabSweepTicks/4)-WidgetlabSweepMin)/(WidgetlabSweepMax-WidgetlabSweepMin),
				WidgetlabPressureKey: 101.3,
				WidgetlabBatteryKey:  100 - float64(n%100),
			}
		})
	if err != nil {
		log.Error().Err(err).Msg("emit measurement failed")
	}
	return err
}
