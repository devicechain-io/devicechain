// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	_ "embed"
	"encoding/json"
	"math"
	"sync"
	"sync/atomic"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/rs/zerolog/log"
)

// Fleetpulse profile/device-type/metric tokens — fixed, not derived from any
// handshake field, since this manifest is a static built-in scenario (mirrors the
// Devicepulse*/Buildingpulse* constants in sim.go).
const (
	FleetpulseProfileToken    = "fleetpulse-profile"
	FleetpulseDeviceTypeToken = "fleetpulse-vehicle"
	FleetpulseSpeedKey        = "speed_mps"
	FleetpulseHeadingKey      = "heading_deg"

	// The one typed command the profile declares. FleetpulseSpeedLimitKey is read from two
	// sides: the definition authored here and whatever issues it (the console's command
	// form, a rule's sendCommand), so a vehicle that listened for any other spelling would
	// answer FAILED to the command it was built for.
	FleetpulseSpeedLimitCommandDef = "fleetpulse-set-speed-limit-cmd"
	FleetpulseSpeedLimitKey        = "set-speed-limit"
	FleetpulseSpeedLimitParameter  = "limit_mps"

	// fleetpulseVehicleCount is the demo sizing: enough vehicles to see a fleet move
	// together, small enough to watch. Resizable — this is a single population.
	fleetpulseVehicleCount = 6
)

// ---- The loop ---------------------------------------------------------------------
//
// The track is a closed circuit around downtown Atlanta, and both properties in that
// sentence are load-bearing rather than scenic.
//
// CLOSED, because a demo that integrates a heading into a position wanders: it looks
// right for ten minutes and then the fleet is in the Atlantic, with nothing in the run
// having gone wrong that anyone can point at. A circuit parameterised by tick has no
// accumulated state to drift, so hour six renders the same track as minute one.
//
// IN RANGE BY CONSTRUCTION, not by clamping afterwards. Every rendered latitude is
// within fleetpulseLoopRadius of the centre and every longitude within
// radius*fleetpulseLongitudeScale of it, so the bound follows from the constants below
// rather than from a check that quietly rewrites a coordinate it dislikes — a clamp
// would turn a units bug into a fleet stacked on the edge of the map, which is data
// the platform would faithfully store.
const (
	FleetpulseCentreLatitude  = 33.749
	FleetpulseCentreLongitude = -84.388

	// fleetpulseLoopRadius is the circuit's radius in degrees of latitude — about
	// 1.1 km, so the whole loop sits inside the downtown grid at any zoom a demo uses.
	fleetpulseLoopRadius = 0.01

	// fleetpulseLongitudeScale converts that radius into degrees of LONGITUDE at this
	// latitude, so the track is a circle on the ground rather than an ellipse squashed
	// by the projection. 1/cos(latitude), written as a literal because the constant
	// expression must be usable in a const block; TestFleetpulseLongitudeScaleMatches
	// TheCentreLatitude holds it to cos(FleetpulseCentreLatitude).
	fleetpulseLongitudeScale = 1.2027

	// fleetpulseLoopTicks is how many ticks one full circuit takes. The tick counter is
	// reduced MODULO this before it becomes an angle, which is what keeps a long run
	// bounded: the angle never grows, so it never loses precision and the track never
	// creeps.
	fleetpulseLoopTicks = 72

	// The speed curve, in metres per second — the platform's fixed unit for speed.
	// 9.4-17.4 m/s is roughly 21-39 mph: a plausible city-loop speed.
	//
	// 🔴 Deliberately NOT derived from the loop's geometry and the tick interval, which
	// would be more physically honest and is a trap: --emit-interval is operator input
	// with no floor, so at a sub-microsecond cadence a derived speed exceeds the
	// column's magnitude ceiling and EVERY location event is refused at decode. A
	// bounded synthetic curve cannot do that at any cadence.
	fleetpulseSpeedCentre    = 13.4
	fleetpulseSpeedAmplitude = 4.0

	// Elevation above the ellipsoid, in metres. Atlanta's downtown sits around 320 m;
	// the small ripple exists so an elevation chart is not a flat line.
	fleetpulseElevationCentre    = 320.5
	fleetpulseElevationAmplitude = 6.0

	// Reported GPS accuracy, in metres — 2.5-5.5 m, an ordinary consumer-GNSS fix.
	// Non-negative by construction (the amplitude is under the centre), which is the
	// bound the decoder enforces.
	fleetpulseAccuracyCentre    = 4.0
	fleetpulseAccuracyAmplitude = 1.5
)

// fleetpulseSpeedLimitSchema is the command's parameter schema: ONE required number, 5 to 30
// m/s. It lives in a file of its own so the platform's own validator can be pointed at the
// exact bytes this scenario publishes (device-management's model package reads it): the
// range is enforced by the platform when the command is queued, not by this simulator, and a
// test in the module that owns the validator is what proves it.
//
//go:embed fleetpulse_speed_limit_schema.json
var fleetpulseSpeedLimitSchema string

// fleetpulse is the L0b location scenario: a small fleet of vehicles driving a closed
// loop, each tick emitting BOTH a Location and a Measurement.
//
// It exists because the platform's location spine had no in-repo producer at all, and a
// contract nothing emits is a contract nobody has had to be right about. Two separate
// events rather than one combined shape: Location and Measurement are distinct event
// types with distinct payloads on this wire, and a scenario that invented a merged one
// would be exercising a shape the platform does not have.
//
// The speed value is the SAME number in both — the fix's speed field and the
// speed_mps metric — so the location report has a visible companion on a chart, and so
// a test can hold the two paths to one generated value rather than to two curves that
// merely look alike.
type fleetpulse struct {
	// seed drives all deterministic generation — the topology, and here also the
	// fleet's starting angle on the loop, so a given (sim, seed) renders the same track
	// every run. Same threading and reset rationale as devicepulse's identical field.
	seed int64
	// load carries the run-time device-count/cadence overrides, same role as
	// devicepulse's.
	load Load
	// ticks counts Tick calls since process start, same role as devicepulse's.
	ticks atomic.Int64
	// limits is what the vehicles' set-speed-limit command has set. Empty until a command
	// arrives, which is what keeps a run that never sends one byte-identical to before.
	limits *fleetpulseLimits
}

// fleetpulseLimits is the per-vehicle speed limit the command sets.
type fleetpulseLimits struct {
	mu    sync.Mutex
	byDev map[string]float64
}

func (l *fleetpulseLimits) set(device string, limit float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byDev == nil {
		l.byDev = map[string]float64{}
	}
	l.byDev[device] = limit
}

func (l *fleetpulseLimits) get(device string) (float64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.byDev[device]
	return v, ok
}

// capSpeed applies a vehicle's limit to a fix: the speed never exceeds it. A fix with no limit
// set, or already under it, comes back untouched.
func (l *fleetpulseLimits) capSpeed(device string, fix Fix) Fix {
	limit, ok := l.get(device)
	if !ok || fix.Speed == nil || *fix.Speed <= limit {
		return fix
	}
	capped := limit
	fix.Speed = &capped
	return fix
}

// fleetpulseHandler is the vehicles' device side of the command contract. Without it the
// command receiver answers SUCCESSFUL on receipt whatever was asked, so the delivery would
// complete without the vehicle having changed anything.
type fleetpulseHandler struct{ limits *fleetpulseLimits }

// Receive applies the limit, then answers. The answer is the vehicle's, sent after the limit
// is in force, so SUCCESSFUL means the next reading is already capped.
//
// The range 5-30 is NOT checked here: the platform refuses an out-of-range value when the
// command is queued, so one that reaches a vehicle is in range. This only refuses what the
// vehicle cannot act on at all (another command, no number, a non-positive or non-finite
// one), answering FAILED rather than inventing a limit.
func (h fleetpulseHandler) Receive(device string, c cmdreceiver.Command, answer func(success bool)) {
	ok := false
	if c.Name == FleetpulseSpeedLimitKey {
		var args map[string]json.Number
		if err := json.Unmarshal(c.Payload, &args); err == nil {
			if limit, err := args[FleetpulseSpeedLimitParameter].Float64(); err == nil &&
				limit > 0 && !math.IsInf(limit, 0) && !math.IsNaN(limit) {
				h.limits.set(device, limit)
				ok = true
			}
		}
	}
	// answer may block on the broker, and this runs on the broker's goroutine.
	go answer(ok)
}

// CommandHandler makes the vehicles answer their own commands (see commandHandlerSource).
func (s *fleetpulse) CommandHandler() cmdreceiver.Handler {
	return fleetpulseHandler{limits: s.limits}
}

// NewFleetpulse returns the fleetpulse reference Sim seeded from the handshake.
// Prefer NewSim, which validates load against the manifest.
func NewFleetpulse(seed int64, load Load) Sim {
	return &fleetpulse{seed: seed, load: load, limits: &fleetpulseLimits{}}
}

func (s *fleetpulse) Manifest() SimManifest {
	return resize(s.load, SimManifest{
		Name: "fleetpulse",
		Seed: s.seed,
		// The vehicles listen for set-speed-limit, so they need a far end: an HTTP-ingress
		// device cannot receive, and the command would sit at SENT until it timed out.
		CommandFarEnd: FarEndInternal,
		Profiles: []ProfileSpec{
			{
				Token:    FleetpulseProfileToken,
				Name:     "Fleet Pulse Vehicle Profile",
				Category: "vehicle",
				// The two metrics are the two fields of the fix a chart can read back:
				// the profile declares them so the Measurement half of each tick lands
				// against a declared vocabulary (ADR-016) rather than as free-floating
				// keys. The position itself is NOT a metric — it travels as a Location
				// event, which is the whole point of this scenario.
				Metrics: []MetricSpec{
					{Key: FleetpulseSpeedKey, Name: "Speed", DataType: "DOUBLE", Unit: "m/s"},
					{Key: FleetpulseHeadingKey, Name: "Heading", DataType: "DOUBLE", Unit: "deg"},
				},
				Commands: []CommandSpec{
					{
						Token:           FleetpulseSpeedLimitCommandDef,
						CommandKey:      FleetpulseSpeedLimitKey,
						Name:            "Set speed limit",
						ParameterSchema: fleetpulseSpeedLimitSchema,
					},
				},
			},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: FleetpulseDeviceTypeToken, Name: "Fleet Pulse Vehicle", ProfileToken: FleetpulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            FleetpulseDeviceTypeToken,
				Count:             fleetpulseVehicleCount,
				TokenPattern:      "fleetpulse-{n:03d}",
				ExternalIdPattern: "VIN-FLEETPULSE-{n:05d}",
			},
		},
	})
}

func (s *fleetpulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// Tick emits one Location and one Measurement per device.
//
// Both passes read the SAME fleetpulseFix, so the speed on the location entry and the
// speed_mps measurement are one generated value seen twice rather than two curves that
// happen to agree today. Two passes rather than one because they are two events: a
// combined emit would have to invent a payload shape the platform does not decode.
//
// The location pass runs FIRST, and only because a reader watching a map wants the
// position that goes with a reading to be there when the reading arrives. Nothing
// depends on the order — the two events are keyed independently.
func (s *fleetpulse) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	count := len(rt.Devices)
	if count == 0 {
		return nil
	}
	workers := rt.Load.Workers(count)

	locErr := EmitLocationAll(ctx, rt, workers, func(i int, d DeviceInstance) (Fix, bool) {
		return s.limits.capSpeed(d.Token, fleetpulseFix(s.seed, n, i, count)), true
	})
	if locErr != nil {
		log.Error().Err(locErr).Msg("emit location failed")
	}

	measErr := EmitAll(ctx, rt, workers, func(i int, d DeviceInstance) map[string]float64 {
		fix := s.limits.capSpeed(d.Token, fleetpulseFix(s.seed, n, i, count))
		return map[string]float64{
			FleetpulseSpeedKey:   *fix.Speed,
			FleetpulseHeadingKey: *fix.Heading,
		}
	})
	if measErr != nil {
		log.Error().Err(measErr).Msg("emit measurement failed")
	}

	// The location error is reported in preference to the measurement one because it is
	// the half this scenario exists to exercise; both are logged either way, so neither
	// disappears.
	if locErr != nil {
		return locErr
	}
	return measErr
}

// fleetpulseTheta is vehicle i's angle on the loop at tick n, in radians.
//
// The tick is reduced modulo the circuit length BEFORE it becomes an angle. That is
// what makes a long run identical to a short one: an angle built from an ever-growing
// tick count keeps every subsequent trig call at a coarser and coarser effective
// precision, so the track a nine-hour demo draws slowly stops being the track its
// first minute drew.
func fleetpulseTheta(seed, tick int64, i, count int) float64 {
	if count < 1 {
		count = 1
	}
	step := ((tick % fleetpulseLoopTicks) + fleetpulseLoopTicks) % fleetpulseLoopTicks
	spacing := 2 * math.Pi / float64(count)
	return fleetpulseSeedPhase(seed) +
		2*math.Pi*float64(step)/fleetpulseLoopTicks +
		float64(i)*spacing
}

// fleetpulseSeedPhase is where on the loop the fleet starts, derived from the seed so
// two sims of this scenario under different seeds do not render the identical track.
//
// Bounded to one revolution by construction: the seed is taken modulo a fixed divisor
// before it scales, so no seed — including a negative one, which uint64 conversion
// folds rather than rejects — can produce an angle large enough to lose precision.
func fleetpulseSeedPhase(seed int64) float64 {
	const divisions = 1000
	return 2 * math.Pi * float64(uint64(seed)%divisions) / divisions
}

// fleetpulseFix is vehicle i's complete position report at tick n — pure, total, and
// the single source both halves of Tick read.
//
// Every optional field is reported. The "device has no such sensor" case is a property
// of the EMITTER (a nil field is absent from the JSON, never "0"), pinned by
// TestAnUnreportedOptionalIsAbsentFromTheJson rather than by a scenario branch that
// would only ever exercise one side of it.
func fleetpulseFix(seed, tick int64, i, count int) Fix {
	theta := fleetpulseTheta(seed, tick, i, count)

	latitude := FleetpulseCentreLatitude + fleetpulseLoopRadius*math.Sin(theta)
	longitude := FleetpulseCentreLongitude +
		fleetpulseLoopRadius*fleetpulseLongitudeScale*math.Cos(theta)

	// The tangent to the circuit, as a compass bearing. North is +latitude and east is
	// +longitude scaled back to ground distance, which cancels fleetpulseLongitudeScale
	// exactly — so the bearing is atan2(-sin, cos) and a vehicle's heading always agrees
	// with the direction its next position lies in.
	heading := canonicalHeading(math.Atan2(-math.Sin(theta), math.Cos(theta)) * 180 / math.Pi)

	speed := fleetpulseSpeedCentre + fleetpulseSpeedAmplitude*math.Sin(theta)
	elevation := fleetpulseElevationCentre + fleetpulseElevationAmplitude*math.Sin(2*theta)
	accuracy := fleetpulseAccuracyCentre + fleetpulseAccuracyAmplitude*math.Sin(theta+1)

	return Fix{
		Latitude:  latitude,
		Longitude: longitude,
		Elevation: &elevation,
		Accuracy:  &accuracy,
		Speed:     &speed,
		Heading:   &heading,
	}
}
