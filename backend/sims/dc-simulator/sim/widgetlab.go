// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
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
// All four channels complete. Telemetry goes out over the one-way HTTP ingress like
// every other scenario's; the CONTROL channel is the exception, and it is why this
// manifest declares CommandFarEnd: FarEndInternal — an HTTP-ingress-only device cannot receive
// anything, so a command-button on the board would enqueue against a real published
// definition, dispatch correctly, and sit at SENT until it expired a week later —
// the scenario permanently demonstrating the very failure the board exists to
// distinguish from success. Bootstrap therefore attaches a cmdreceiver per device
// over the MQTT gateway, and REFUSES to come up if it cannot.
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

// The alarm the rule raises and the command the button issues.
//
// WidgetlabCommandKey is genuinely read from TWO sides — the definition authored here
// and the widget options the board bakes — so it is a constant for the same reason
// the threshold is: a button whose commandName misses the published definition looks
// configured, enqueues nothing the vocabulary resolves, and fails at the delivery
// boundary. A test reads both back and compares them.
//
// WidgetlabAlarmKey is NOT: no widget option filters on an alarm key, so it has one
// side only. An earlier version of this comment claimed otherwise and said "the alarm
// widgets filter on the constant" — they filter on state and severity. It is a
// constant because the rule and any future consumer should agree, not because
// anything today disagrees.
const (
	WidgetlabRuleToken  = "wl-over-temp"
	WidgetlabAlarmKey   = "wl-over-temp"
	WidgetlabCommandDef = "wl-set-setpoint-cmd"
	WidgetlabCommandKey = "setSetpoint"

	// The alarm tier. Both spellings live in wirevocabulary.go, which carries the whole
	// reasoning for why they are two constants and what each collapse breaks — the
	// canonical copy, not a summary of one.
	WidgetlabSeverity          = SeverityMajor
	WidgetlabAlarmSeverityWire = AlarmSeverityMajorWire
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
		// The gallery carries a command-button, so these devices must LISTEN, not
		// only report. Manifest.Validate refuses the board without this. INTERNAL
		// because widgetlab's devices are this process's own simulated ones: there is
		// no client anywhere else that could answer for them.
		CommandFarEnd: FarEndInternal,
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
				// The threshold is WidgetlabAlarmThreshold rather than a literal, which
				// is the entire point of the sweep const block: the rule and the curve
				// that must cross it cannot drift apart if neither owns the number.
				// Enabled, because publish-time validation only gates ENABLED rules — a
				// disabled one is published unchecked and would make a broken predicate
				// look accepted.
				DetectionRules: []DetectionRuleSpec{
					ThresholdAlarmRule{
						Token:     WidgetlabRuleToken,
						Name:      "Widget Lab over-temperature",
						Metric:    WidgetlabTemperatureKey,
						Op:        OpGt,
						Threshold: WidgetlabAlarmThreshold,
						Severity:  WidgetlabSeverity,
						AlarmKey:  WidgetlabAlarmKey,
						Enabled:   true,
					}.Spec(),
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
				// Deliberately NOT distributed across the zones. The gallery's alarm
				// widgets are scoped to a zone anchor, and the shared profile means the
				// DETECT rule fires for edge devices too — so an edge sensor inside the
				// gallery's zone would put 350 C alarms on the catalog board, and one
				// click on the alarm table's originator drill would rebind the whole
				// gallery to the extremes device. The stress board binds these by
				// device token directly, so they need no area to be reachable.
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

// ---- The pathological lane ----------------------------------------------------
//
// The edge sensors exist to drive widgets into the states they render badly. Which
// case a device shows is a function of the TICK COUNT, never the wall clock, so a
// fixture, a test and a live run agree on what tick N looks like.
//
// 🔴 THESE STATES ARE NOT EQUALLY REPRODUCIBLE, and pretending otherwise would make
// the stress board lie about what it is showing:
//
//   - reproducible-under-reset: the spike, zero, negative and at-threshold values,
//     and the partial reporter's dropped metrics. Pure tick math over a device that
//     keeps emitting, so `reset` (a re-Bootstrap, which cannot purge telemetry)
//     shows them again on the next cycle.
//   - phase-dependent: the silent device. Whether it is mid-silence depends on where
//     the process is in its cycle, and the tick counter restarts at zero on a
//     restart — so a viewer sees the silence eventually, not on demand.
//   - NOT PRODUCED AT ALL, deliberately: "no data yet" and "exactly one sample". Both
//     exist only before a device's first telemetry, and `reset` never deletes events,
//     so after run 1 they are gone from this instance forever. Manufacturing them
//     would need a device provisioned per run, which is a different scenario. A
//     widget's empty state is worth seeing; it is not worth lying about.
type edgeBehaviour int

const (
	// edgeExtremes cycles through the values that break a chart's axis and a gauge's
	// scale: a large spike, zero, a negative reading, and one exactly AT the alarm
	// threshold — which must NOT raise, because the rule compares with a strict `gt`.
	edgeExtremes edgeBehaviour = iota
	// edgePartial reports only some of the metrics its profile declares, so a table
	// loses rows and a card bound to a missing one shows its empty state while the
	// device is plainly still alive.
	edgePartial
	// edgeSilent stops emitting for part of the cycle: the chart stops extending, the
	// card's timestamp goes stale, and an absence rule would fire.
	edgeSilent
)

// widgetlabEdgeNominalPhases is how many steps of the cycle an edge device spends
// behaving normally before its extremes begin, so a viewer sees the contrast.
const widgetlabEdgeNominalPhases = 2

// widgetlabEdgeCyclePhases is the cycle length, DERIVED from the number of extremes
// rather than fixed at six.
//
// It has to be derived. With a hard-coded length, adding a fifth extreme leaves it
// unreachable (the cycle never gets to its step) and removing one panics on an
// out-of-range index — both from a one-line edit to a list whose comment invites
// exactly that edit. Deriving it makes the list the only thing to change.
var widgetlabEdgeCyclePhases = int64(widgetlabEdgeNominalPhases + len(widgetlabEdgeExtremes))

// widgetlabEdgePhaseTicks is how long each step lasts: a whole cycle spans one sweep
// period, so an edge device walks its entire repertoire while a nominal one draws
// one triangle.
var widgetlabEdgePhaseTicks = max(int64(1), WidgetlabSweepTicks/widgetlabEdgeCyclePhases)

// The extremes an edgeExtremes device visits, in cycle order. A slice rather than a
// switch so a test can assert the cycle visits every one of them, and so adding a
// case is one line rather than an edit to control flow.
//
// The at-threshold value is the interesting one: `gt` is strict, so a device sitting
// exactly ON the threshold must not raise. That boundary is easy to get wrong in a
// rule and impossible to notice without a device that tests it.
var widgetlabEdgeExtremes = []float64{
	WidgetlabSweepMax * 10, // a spike that blows a chart axis scaled for the sweep
	0,
	-WidgetlabSweepMax, // negative, which a gauge scaled from 15 cannot show
	WidgetlabAlarmThreshold,
}

// widgetlabEdgePhase is which step of the cycle a tick falls in.
//
// The normalisation is load-bearing for negative ticks: Go's % keeps the dividend's
// sign, so tick -15 lands on step -1 and indexes the extremes slice out of range. It
// is not reachable through Tick (the counter starts at 1) but the function is total,
// and a total function that panics for some of its domain is a trap for whatever
// calls it next.
func widgetlabEdgePhase(tick int64) int {
	phase := (tick / widgetlabEdgePhaseTicks) % widgetlabEdgeCyclePhases
	if phase < 0 {
		phase += widgetlabEdgeCyclePhases
	}
	return int(phase)
}

// widgetlabEdgeMetrics returns what an edge device emits at a tick, or nil for a
// device that is silent. Pure in (behaviour, tick).
func widgetlabEdgeMetrics(behaviour edgeBehaviour, tick int64) map[string]float64 {
	phase := widgetlabEdgePhase(tick)

	switch behaviour {
	case edgeExtremes:
		// Nominal for the first two steps so a viewer sees the contrast, then one
		// extreme per remaining step.
		if phase < widgetlabEdgeNominalPhases {
			return widgetlabNominalMetrics(tick)
		}
		values := widgetlabNominalMetrics(tick)
		values[WidgetlabTemperatureKey] = widgetlabEdgeExtremes[phase-widgetlabEdgeNominalPhases]
		return values

	case edgePartial:
		values := widgetlabNominalMetrics(tick)
		// Drop metrics progressively, so a table visibly loses rows rather than
		// switching between two fixed shapes.
		switch {
		case int64(phase) >= widgetlabEdgeCyclePhases-2:
			delete(values, WidgetlabPressureKey)
			delete(values, WidgetlabBatteryKey)
			delete(values, WidgetlabHumidityKey)
		case phase >= widgetlabEdgeNominalPhases:
			delete(values, WidgetlabPressureKey)
			delete(values, WidgetlabBatteryKey)
		}
		return values

	case edgeSilent:
		// Silent for the last third of the cycle. Nil, not an empty measurement:
		// EmitAll skips the device entirely, which is what an offline device does.
		if int64(phase) >= widgetlabEdgeCyclePhases-2 {
			return nil
		}
		return widgetlabNominalMetrics(tick)
	}
	return widgetlabNominalMetrics(tick)
}

// widgetlabEdgeBehaviours classifies rt.Devices, positionally, so a generator reads
// a device's behaviour off its INDEX rather than parsing its token. Edge devices get
// one behaviour each in Expand order; a nominal device gets none.
func widgetlabEdgeBehaviours(devices []DeviceInstance) map[int]edgeBehaviour {
	out := make(map[int]edgeBehaviour, len(devices))
	edge := 0
	for i, d := range devices {
		if d.DeviceTypeToken != WidgetlabEdgeDeviceTypeToken {
			continue
		}
		out[i] = edgeBehaviour(edge % 3)
		edge++
	}
	return out
}

// widgetlabEdgeDevice returns the edge device carrying a given behaviour.
//
// The stress board binds through this rather than taking the first edge device it
// finds: each of its widgets is titled for a specific pathology, so a board that
// bound them all to one device would show one pathology under three titles — which
// is what it did, and what made two of its titles false.
func widgetlabEdgeDevice(devices []DeviceInstance, want edgeBehaviour) (DeviceInstance, error) {
	for i, behaviour := range widgetlabEdgeBehaviours(devices) {
		if behaviour == want {
			return devices[i], nil
		}
	}
	return DeviceInstance{}, fmt.Errorf("no edge device carries behaviour %d, so a board bound "+
		"to it would show nothing the widget claims", want)
}

// widgetlabNominalMetrics is what a well-behaved sensor reports at a tick: the sweep
// plus three supporting curves. Returned fresh each call because the pathological
// generators mutate it.
func widgetlabNominalMetrics(tick int64) map[string]float64 {
	return map[string]float64{
		WidgetlabTemperatureKey: widgetlabSweep(tick),
		// Offset by a quarter period so the two curves are visibly different on a
		// multi-series chart rather than one line hiding another.
		WidgetlabHumidityKey: 40 + 20*(widgetlabSweep(tick+WidgetlabSweepTicks/4)-WidgetlabSweepMin)/(WidgetlabSweepMax-WidgetlabSweepMin),
		WidgetlabPressureKey: 101.3,
		WidgetlabBatteryKey:  100 - float64(tick%100),
	}
}

// Tick emits one measurement per device: the nominal sweep for a sensor, and the
// current step of the pathological cycle for an edge sensor.
func (s *widgetlab) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	behaviours := widgetlabEdgeBehaviours(rt.Devices)

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(i int, _ DeviceInstance) map[string]float64 {
			if behaviour, isEdge := behaviours[i]; isEdge {
				return widgetlabEdgeMetrics(behaviour, n)
			}
			return widgetlabNominalMetrics(n)
		})
	if err != nil {
		log.Error().Err(err).Msg("emit measurement failed")
	}
	return err
}
