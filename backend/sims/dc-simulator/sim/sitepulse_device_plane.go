// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"
)

// The sitepulse DEVICE PLANE, played by this process: a headless stand-in for the Unity
// player, for when there is no player (a website capture, a recording, a demo on a host
// with no GPU). It is OFF unless the process is started with --play-devices, and the
// Unity player must not run against the same sim while it is on: both would publish a
// reading for the same machine, and the platform would hold two contradicting histories
// (see sitepulse.Tick).
//
// What it reproduces, and from where:
//
//   - the telemetry follows the scene's own machine model, MachineModel.cs
//     (demos/sitepulse-unity/Assets/Sitepulse/Scripts/Simulation), ported constant for
//     constant: the same seeded starting values (StableRandom over the externalId, so
//     machine SP-HL-0003 starts with the same tank here as in the scene), the same lags,
//     the same burn rates, the same rounding;
//   - the positions replay the scene's generated fleet tracks (quarry_fleet_live.json,
//     embedded below as sitepulse_fleet_tracks.json; a test holds the copy to the source)
//     through the scene's own sampling (TrackPlayer.cs) and geographic conversion
//     (SiteDefinition.cs, whose origin is sitepulse_geofence.go's);
//   - the platform's three rules are crossed by PRESENTER inputs the model already has
//     (PrepareLowFuel, PrepareTyreLeak, Refill), on a fixed script: a machine whose
//     engine runs hot from the start, a slow tyre leak four minutes in, and a low-fuel
//     episode on one machine at a time, round-robin. Normal operation never crosses a
//     rule, exactly as in the scene.
//
// DETERMINISM. The model advances in fixed sub-steps of simulation time, never by the
// wall-clock gap between two ticks, so the readings at a given elapsed time are a pure
// function of (externalId, elapsed): two runs read the same at the same moment of the
// run whatever their tick jitter. The only thing the wall clock decides is WHEN a tick
// samples the model.

// sitepulseFleetTracks is the scene's generated fleet: one looped track per route, and
// which machine drives which track at what phase.
//
//go:embed sitepulse_fleet_tracks.json
var sitepulseFleetTracks []byte

// sitepulseSubstepSeconds is the fixed step the model advances by.
const sitepulseSubstepSeconds = 0.25

// The presenter script, in seconds of simulation time from the start of the run.
const (
	// The low-fuel cycle: the first episode starts a minute in, the next one every
	// sitepulseFuelCycleSeconds after, each on the next machine of sitepulseFuelRotation.
	// A machine is brought just above the line and crosses it within a minute; it is
	// refuelled sitepulseFuelLowSeconds after its episode began.
	sitepulseFuelFirstSeconds   = 60.0
	sitepulseFuelCycleSeconds   = 900.0
	sitepulseFuelLowSeconds     = 480.0
	sitepulseFuelJustAbovePct   = 16.5
	sitepulseFuelCrossSeconds   = 60.0
	sitepulseTyreLeakAtSeconds  = 240.0
	sitepulseTyreJustAboveKpa   = 615.0
	sitepulseTyreCrossSeconds   = 180.0
	sitepulseOverheatBiasC      = 24.0
	sitepulseRefuelLowestPct    = 92.0
	sitepulseRefuelSpreadPct    = 6.0
	sitepulseLeakingMachine     = "SP-HL-0003"
	sitepulseOverheatingMachine = "SP-DZ-0002"
)

// sitepulseFuelRotation is the order machines run low on fuel in.
var sitepulseFuelRotation = []string{"SP-LD-0004", "SP-HL-0005", "SP-DZ-0005", "SP-HL-0002", "SP-LD-0001", "SP-DZ-0006"}

// ---- StableRandom (MachineModel.cs) ------------------------------------------------

// stableSeed is StableRandom.Seed: FNV-1a over the id's UTF-16 code units. Every id the
// scene seeds from is ASCII, where a code unit is the byte.
func stableSeed(text string) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range text {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// stableUnit is StableRandom.Unit: a value in [0, 1) that depends only on the seed and
// the stream number (one SplitMix64 round).
func stableUnit(seed, stream uint64) float64 {
	z := seed + 0x9E3779B97F4A7C15*(stream+1)
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return float64(z>>11) * (1.0 / 9007199254740992.0)
}

// ---- the machine model (MachineModel.cs) -------------------------------------------

type sitepulseKind string

const (
	sitepulseHauler sitepulseKind = "Hauler"
	sitepulseLoader sitepulseKind = "Loader"
	sitepulseDozer  sitepulseKind = "Dozer"
	sitepulsePlant  sitepulseKind = "Plant"
)

// sitepulseMaxStillSpeed is the speed (m/s) above which a machine counts as moving.
const sitepulseMaxStillSpeed = 0.3

type sitepulseMachine struct {
	kind sitepulseKind
	id   string
	seed uint64

	tyreBase, tempOffset, fuelRateScale, phase float64

	fuel, engineTemp, engineHours, tyre, payload, throughput, simTime float64
	boostPctPerHour, boostEndsBelowPct                                float64
	leakKpaPerSecond, leakStopsBelowKpa, leakLossKpa                  float64
	wasLoaded                                                         bool
	loads                                                             uint64
	running                                                           bool

	// tempBiasC is a presenter input this port adds to the scene's model: a constant
	// rise in the engine's setpoint (a blocked radiator), so the overheat rule can be
	// crossed through the model's own lag rather than by writing a temperature.
	tempBiasC float64
}

func newSitepulseMachine(kind sitepulseKind, id string) *sitepulseMachine {
	seed := stableSeed(id)
	m := &sitepulseMachine{
		kind:          kind,
		id:            id,
		seed:          seed,
		fuel:          35.0 + 60.0*stableUnit(seed, 1),
		engineTemp:    80.0 + 8.0*stableUnit(seed, 2),
		engineHours:   4000.0 + 12000.0*stableUnit(seed, 3),
		tyreBase:      697.0 + 13.0*stableUnit(seed, 4),
		tempOffset:    -1.5 + 3.0*stableUnit(seed, 5),
		fuelRateScale: 0.9 + 0.2*stableUnit(seed, 6),
		phase:         2.0 * math.Pi * stableUnit(seed, 7),
		running:       kind == sitepulsePlant,
	}
	m.tyre = m.tyreBase
	if kind == sitepulsePlant {
		m.throughput = m.plantBase()
	}
	return m
}

func (m *sitepulseMachine) hasTyres() bool {
	return m.kind == sitepulseHauler || m.kind == sitepulseLoader
}

func (m *sitepulseMachine) plantBase() float64 { return 780.0 + 80.0*stableUnit(m.seed, 8) }

func (m *sitepulseMachine) fuelPeakPerHour() float64 {
	if m.kind == sitepulseHauler {
		return 1.8
	}
	return 1.7
}

func (m *sitepulseMachine) loadFactor(moving, loaded bool) float64 {
	switch m.kind {
	case sitepulseHauler:
		if loaded {
			return 0.9
		}
		if moving {
			return 0.5
		}
		return 0.15
	case sitepulseLoader:
		if loaded {
			return 0.8
		}
		if moving {
			return 0.5
		}
		return 0.3
	default:
		if moving {
			return 0.7
		}
		return 0.35
	}
}

// step is MachineModel.Step: advance by dt seconds of simulation time.
func (m *sitepulseMachine) step(dt, speedMps float64, loaded, running bool) {
	if !(dt > 0) {
		return
	}
	m.simTime += dt
	if m.kind == sitepulsePlant {
		m.running = running
		target := 0.0
		if running {
			target = m.plantBase() + 25.0*math.Sin(2.0*math.Pi*m.simTime/150.0+m.phase)
		}
		m.throughput += (target - m.throughput) * (1.0 - math.Exp(-dt/8.0))
		if !running && m.throughput < 0.05 {
			m.throughput = 0
		}
		return
	}

	moving := speedMps > sitepulseMaxStillSpeed
	load := m.loadFactor(moving, loaded)

	setpoint := 85.0 + 10.0*load + m.tempOffset + 0.4*math.Sin(2.0*math.Pi*m.simTime/90.0+m.phase) + m.tempBiasC
	m.engineTemp += (setpoint - m.engineTemp) * (1.0 - math.Exp(-dt/120.0))

	perHour := m.fuelPeakPerHour() * (0.3 + 0.7*load) * m.fuelRateScale
	if m.boostPctPerHour > 0 {
		if m.fuel > m.boostEndsBelowPct {
			perHour += m.boostPctPerHour
		} else {
			m.boostPctPerHour = 0
		}
	}
	next := m.fuel - perHour*dt/3600.0
	switch {
	case next < 0:
		m.fuel = 0
	case next < m.fuel:
		m.fuel = next
	}

	m.engineHours += dt / 3600.0

	if m.hasTyres() {
		m.tyre = m.tyreBase + 0.3*(m.engineTemp-85.0) + 0.8*math.Sin(2.0*math.Pi*m.simTime/240.0+m.phase)
		if m.leakKpaPerSecond > 0 {
			m.leakLossKpa += m.leakKpaPerSecond * dt
			if m.tyre-m.leakLossKpa < m.leakStopsBelowKpa {
				m.leakKpaPerSecond = 0
			}
		}
		m.tyre -= m.leakLossKpa

		if loaded && !m.wasLoaded {
			m.loads++
			if m.kind == sitepulseHauler {
				m.payload = 86.0 + 8.0*stableUnit(m.seed, 1000+m.loads)
			} else {
				m.payload = 11.0 + 4.0*stableUnit(m.seed, 1000+m.loads)
			}
		} else if !loaded {
			m.payload = 0
		}
		m.wasLoaded = loaded
	}
}

// prepareLowFuel is MachineModel.PrepareLowFuel: it can only LOWER the tank, and adds an
// extra burn that carries it through linePct within about crossWithinSeconds.
func (m *sitepulseMachine) prepareLowFuel(justAbovePct, crossWithinSeconds, linePct float64) bool {
	if m.kind == sitepulsePlant || m.fuel <= linePct {
		return false
	}
	if m.fuel > justAbovePct {
		m.fuel = justAbovePct
	}
	m.boostPctPerHour = (m.fuel - linePct) / crossWithinSeconds * 3600.0
	m.boostEndsBelowPct = linePct - 1.0
	return true
}

// prepareTyreLeak is MachineModel.PrepareTyreLeak.
func (m *sitepulseMachine) prepareTyreLeak(justAboveKpa, crossWithinSeconds, lineKpa float64) bool {
	if !m.hasTyres() || m.tyre <= lineKpa {
		return false
	}
	if m.tyre > justAboveKpa {
		m.leakLossKpa += m.tyre - justAboveKpa
		m.tyre = justAboveKpa
	}
	m.leakKpaPerSecond = (justAboveKpa - lineKpa + 3.0) / crossWithinSeconds
	m.leakStopsBelowKpa = lineKpa - 12.0
	return true
}

// refill is MachineModel.Refill without the permit (the permit guards the scene's task
// machinery, which this process does not have): it raises the tank, never lowers it.
func (m *sitepulseMachine) refill(targetPct float64) {
	if targetPct > 100 {
		targetPct = 100
	}
	if targetPct > m.fuel {
		m.fuel = targetPct
		m.boostPctPerHour = 0
	}
}

// roundEven rounds to `digits` decimals the way C#'s Math.Round(double, int) does, to the
// nearest even digit at a midpoint.
func roundEven(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.RoundToEven(v*p) / p
}

// measurements is MachineModel.Measurements: exactly the keys of KeysFor(kind), rounded
// for the wire. A dozer reports no tyres or payload; the plant only throughput and its
// running state.
func (m *sitepulseMachine) measurements() map[string]float64 {
	if m.kind == sitepulsePlant {
		running := 0.0
		if m.running {
			running = 1
		}
		return map[string]float64{
			SitepulseThroughputKey:   roundEven(m.throughput, 1),
			SitepulsePlantRunningKey: running,
		}
	}
	out := map[string]float64{
		SitepulseFuelKey:        roundEven(m.fuel, 2),
		SitepulseEngineTempKey:  roundEven(m.engineTemp, 1),
		SitepulseEngineHoursKey: roundEven(m.engineHours, 3),
	}
	if m.hasTyres() {
		out[SitepulsePayloadKey] = roundEven(m.payload, 1)
		out[SitepulseTyreKey] = roundEven(m.tyre, 1)
	}
	return out
}

// ---- the fleet tracks (TrackPlayer.cs) ---------------------------------------------

type sitepulseFleet struct {
	Dt       float64 `json:"dt"`
	Channels []string
	Tracks   []struct {
		Kind   string    `json:"kind"`
		Period float64   `json:"period"`
		Data   []float64 `json:"data"`
	} `json:"tracks"`
	Machines []struct {
		ID     string  `json:"id"`
		Kind   string  `json:"kind"`
		Track  int     `json:"track"`
		Offset float64 `json:"offset"`
	} `json:"machines"`
}

// sitepulseTrackStride is the values per frame: x, z, heading, travel, p1, p2, steer, flag.
const sitepulseTrackStride = 8

type sitepulseTrack struct {
	data         []float64
	frames       int
	frameSeconds float64
	period       float64
}

type sitepulseFrame struct {
	east, north, heading float64
	loaded               bool
}

func repeatSeconds(t, length float64) float64 {
	return math.Max(0, math.Min(length, t-math.Floor(t/length)*length))
}

func deltaAngle(current, target float64) float64 {
	d := repeatSeconds(target-current, 360)
	if d > 180 {
		d -= 360
	}
	return d
}

// at is TrackPlayer.SampleAt for a machine whose loop starts at `offset`: the frame it is
// on at scene time t, interpolated between two.
func (tr *sitepulseTrack) at(t, offset float64) sitepulseFrame {
	ft := repeatSeconds(t-offset, tr.period) / tr.frameSeconds
	i := int(ft)
	if i > tr.frames-1 {
		i = tr.frames - 1
	}
	j := (i + 1) % tr.frames
	w := math.Max(0, math.Min(1, ft-math.Floor(ft)))
	v := func(k int) float64 {
		a, b := tr.data[i*sitepulseTrackStride+k], tr.data[j*sitepulseTrackStride+k]
		return a + (b-a)*w
	}
	h0 := tr.data[i*sitepulseTrackStride+2]
	return sitepulseFrame{
		east:    v(0),
		north:   v(1),
		heading: h0 + deltaAngle(h0, tr.data[j*sitepulseTrackStride+2])*w,
		loaded:  tr.data[i*sitepulseTrackStride+7] > 0.5,
	}
}

// sitepulseSpeedWindow is the span the ground speed is measured over, behind the sample.
const sitepulseSpeedWindow = 0.5

// speedAt is the ground speed (m/s) over the half second before t.
func (tr *sitepulseTrack) speedAt(t, offset float64) float64 {
	a, b := tr.at(t-sitepulseSpeedWindow, offset), tr.at(t, offset)
	d := math.Hypot(b.east-a.east, b.north-a.north)
	// The loop's seam: a machine does not teleport, so a jump longer than any machine
	// could drive in the window is the wrap, measured as standing.
	if d > 40*sitepulseSpeedWindow {
		return 0
	}
	return d / sitepulseSpeedWindow
}

// sitepulseToGeographic is SiteDefinition.ToGeographic: site-local metres (east, north)
// to WGS84 degrees, by the small-area conversion sitepulse_geofence.go documents.
func sitepulseToGeographic(east, north float64) (lat, lon float64) {
	const earthRadius = 6371000.0
	cosOrigin := math.Cos(sitepulseOriginLatitude * math.Pi / 180)
	lat = sitepulseOriginLatitude + north/earthRadius*180/math.Pi
	lon = sitepulseOriginLongitude + east/(earthRadius*cosOrigin)*180/math.Pi
	return lat, lon
}

// ---- the plane ---------------------------------------------------------------------

type sitepulseUnit struct {
	device  DeviceInstance
	machine *sitepulseMachine
	track   *sitepulseTrack // nil for the plant
	offset  float64
}

// SitepulseReading is what one device reports at one sample: its measurements, and for
// a machine its position.
type SitepulseReading struct {
	Measurements map[string]float64
	Fix          *Fix
}

type sitepulseDevicePlane struct {
	units []*sitepulseUnit
	byID  map[string]*sitepulseUnit
	// simT is how far the model has been advanced, in seconds.
	simT float64
	// fuelEpisode is the index of the low-fuel episode under way, -1 before the first;
	// fuelMachine is its machine, cleared once it is refuelled.
	fuelEpisode int
	fuelMachine string
	fuelStarted float64
	leakStarted bool
}

func sitepulseKindOf(externalID string) (sitepulseKind, error) {
	switch {
	case strings.HasPrefix(externalID, "SP-HL-"):
		return sitepulseHauler, nil
	case strings.HasPrefix(externalID, "SP-LD-"):
		return sitepulseLoader, nil
	case strings.HasPrefix(externalID, "SP-DZ-"):
		return sitepulseDozer, nil
	case strings.HasPrefix(externalID, "SP-PL-"):
		return sitepulsePlant, nil
	}
	return "", fmt.Errorf("sitepulse device plane: %q is not a sitepulse machine or plant", externalID)
}

// newSitepulseDevicePlane binds every device to its model and, for a machine, its track.
//
// A machine the fleet has no track for is REFUSED, never parked: the scene's fleet has
// six of each kind, and a resized run (--devices) beyond that would otherwise publish a
// machine that stands still at the origin with a full tank, which reads as data.
func newSitepulseDevicePlane(devices []DeviceInstance) (*sitepulseDevicePlane, error) {
	var fleet sitepulseFleet
	if err := json.Unmarshal(sitepulseFleetTracks, &fleet); err != nil {
		return nil, fmt.Errorf("sitepulse device plane: parse fleet tracks: %w", err)
	}
	if fleet.Dt <= 0 {
		return nil, errors.New("sitepulse device plane: the fleet tracks have no frame time")
	}
	tracks := make([]*sitepulseTrack, len(fleet.Tracks))
	for i, t := range fleet.Tracks {
		if len(t.Data) < sitepulseTrackStride || len(t.Data)%sitepulseTrackStride != 0 || t.Period <= 0 {
			return nil, fmt.Errorf("sitepulse device plane: fleet track %d is malformed", i)
		}
		tracks[i] = &sitepulseTrack{data: t.Data, frames: len(t.Data) / sitepulseTrackStride, frameSeconds: fleet.Dt, period: t.Period}
	}
	route := make(map[string]int, len(fleet.Machines))
	for i, m := range fleet.Machines {
		if m.Track < 0 || m.Track >= len(tracks) {
			return nil, fmt.Errorf("sitepulse device plane: %s drives track %d, which the fleet does not have", m.ID, m.Track)
		}
		route[m.ID] = i
	}

	p := &sitepulseDevicePlane{byID: make(map[string]*sitepulseUnit, len(devices)), fuelEpisode: -1}
	for _, d := range devices {
		kind, err := sitepulseKindOf(d.ExternalId)
		if err != nil {
			return nil, err
		}
		u := &sitepulseUnit{device: d, machine: newSitepulseMachine(kind, d.ExternalId)}
		if kind != sitepulsePlant {
			i, ok := route[d.ExternalId]
			if !ok {
				return nil, fmt.Errorf("sitepulse device plane: the fleet has no track for %s; it drives six machines "+
					"of each kind, so run sitepulse at its own size (no --devices) to play its devices", d.ExternalId)
			}
			m := fleet.Machines[i]
			if !strings.EqualFold(m.Kind, string(kind)) {
				return nil, fmt.Errorf("sitepulse device plane: the fleet drives %s as a %s, not a %s", d.ExternalId, m.Kind, kind)
			}
			u.track, u.offset = tracks[m.Track], m.Offset
		}
		p.units = append(p.units, u)
		p.byID[d.ExternalId] = u
	}
	if u, ok := p.byID[sitepulseOverheatingMachine]; ok {
		u.machine.tempBiasC = sitepulseOverheatBiasC
	}
	return p, nil
}

// advanceTo steps every model forward to `elapsed` seconds of simulation time, in fixed
// sub-steps, applying the presenter script at the sub-step its time falls in.
func (p *sitepulseDevicePlane) advanceTo(elapsed float64) {
	for p.simT+sitepulseSubstepSeconds <= elapsed {
		p.script(p.simT)
		t := p.simT + sitepulseSubstepSeconds
		for _, u := range p.units {
			if u.track == nil {
				u.machine.step(sitepulseSubstepSeconds, 0, false, true)
				continue
			}
			f := u.track.at(t, u.offset)
			u.machine.step(sitepulseSubstepSeconds, u.track.speedAt(t, u.offset), f.loaded, true)
		}
		p.simT = t
	}
}

// script applies the presenter inputs due at simulation time t.
func (p *sitepulseDevicePlane) script(t float64) {
	if !p.leakStarted && t >= sitepulseTyreLeakAtSeconds {
		p.leakStarted = true
		if u, ok := p.byID[sitepulseLeakingMachine]; ok {
			u.machine.prepareTyreLeak(sitepulseTyreJustAboveKpa, sitepulseTyreCrossSeconds, SitepulseTyrePressureThreshold)
		}
	}
	if t >= sitepulseFuelFirstSeconds {
		episode := int((t - sitepulseFuelFirstSeconds) / sitepulseFuelCycleSeconds)
		if episode != p.fuelEpisode {
			p.fuelEpisode = episode
			id := sitepulseFuelRotation[episode%len(sitepulseFuelRotation)]
			if u, ok := p.byID[id]; ok {
				u.machine.prepareLowFuel(sitepulseFuelJustAbovePct, sitepulseFuelCrossSeconds, SitepulseLowFuelThreshold)
				p.fuelMachine, p.fuelStarted = id, t
			}
		}
	}
	if p.fuelMachine != "" && t-p.fuelStarted >= sitepulseFuelLowSeconds {
		u := p.byID[p.fuelMachine]
		u.machine.refill(sitepulseRefuelLowestPct + sitepulseRefuelSpreadPct*stableUnit(u.machine.seed, 2000+uint64(p.fuelEpisode)))
		p.fuelMachine = ""
	}
}

// readings is what every device reports at the plane's current time, in device order.
func (p *sitepulseDevicePlane) readings() []SitepulseReading {
	out := make([]SitepulseReading, len(p.units))
	for i, u := range p.units {
		out[i].Measurements = u.machine.measurements()
		if u.track == nil {
			continue
		}
		f := u.track.at(p.simT, u.offset)
		lat, lon := sitepulseToGeographic(f.east, f.north)
		heading := roundEven(canonicalHeading(f.heading), 1)
		if heading >= 360 {
			heading = 0
		}
		speed := roundEven(u.track.speedAt(p.simT, u.offset), 2)
		out[i].Fix = &Fix{Latitude: roundEven(lat, 7), Longitude: roundEven(lon, 7), Heading: &heading, Speed: &speed}
	}
	return out
}

// ---- running it --------------------------------------------------------------------

// PublishLogEntry is one line of the device plane's publish log: an event the ingress
// ACCEPTED, exactly as it was sent. A recording made from the platform afterwards is
// checked against it, row by row (cmd/sitepulse-record).
type PublishLogEntry struct {
	Device string            `json:"dev"`
	Type   string            `json:"type"`
	Time   string            `json:"t"`
	Values map[string]string `json:"v"`
}

// sitepulsePlayer drives the plane from the lifecycle's tick.
type sitepulsePlayer struct {
	plane   *sitepulseDevicePlane
	started time.Time
	now     func() time.Time

	logMu sync.Mutex
	log   io.Writer
}

// PlayDevices makes a sitepulse sim play its own device plane (see the top of
// sitepulse_device_plane.go), logging every accepted event to publishLog when it is not
// nil. Any other scenario is refused: its Tick already is its device plane, or it has
// none to play.
func PlayDevices(s Sim, publishLog io.Writer) error {
	sp, ok := s.(*sitepulse)
	if !ok {
		return fmt.Errorf("--play-devices is for the sitepulse scenario only (this sim is %q)", s.Manifest().Name)
	}
	sp.player = &sitepulsePlayer{now: time.Now, log: publishLog}
	return nil
}

func (pl *sitepulsePlayer) tick(ctx context.Context, rt *Runtime) error {
	if pl.plane == nil {
		plane, err := newSitepulseDevicePlane(rt.Devices)
		if err != nil {
			return err
		}
		pl.plane, pl.started = plane, pl.now()
	}
	pl.plane.advanceTo(pl.now().Sub(pl.started).Seconds())
	readings := pl.plane.readings()
	byToken := make(map[string]SitepulseReading, len(readings))
	for i, u := range pl.plane.units {
		byToken[u.device.Token] = readings[i]
	}

	// One stamp per kind of event per tick: every machine's reading of this instant
	// carries the same time, as one scene frame would.
	stamp := eventTimestamp()
	workers := rt.Load.Workers(len(rt.Devices))
	mErr := emitEach(ctx, rt, workers, func(_ int, d DeviceInstance) (bool, error) {
		r, ok := byToken[d.Token]
		if !ok {
			return false, nil
		}
		values := wireMeasurements(r.Measurements)
		err := emitMeasurementsAt(ctx, rt, d, stamp, values)
		if err == nil {
			err = pl.record(PublishLogEntry{Device: d.Token, Type: "Measurement", Time: stamp, Values: values})
		}
		return true, err
	})
	lErr := emitEach(ctx, rt, workers, func(_ int, d DeviceInstance) (bool, error) {
		r, ok := byToken[d.Token]
		if !ok || r.Fix == nil {
			return false, nil
		}
		fields := wireFix(*r.Fix)
		err := emitLocationAt(ctx, rt, d, stamp, fields)
		if err == nil {
			err = pl.record(PublishLogEntry{Device: d.Token, Type: "Location", Time: stamp, Values: fields})
		}
		return true, err
	})
	return errors.Join(mErr, lErr)
}

// record appends one accepted event to the publish log (json.Marshal writes a map's keys
// sorted, so the log is byte-stable for a given run). A log that cannot be written fails
// the tick loudly: a recording checked against a log with holes would refuse rows the
// platform really holds, far from the cause.
func (pl *sitepulsePlayer) record(e PublishLogEntry) error {
	if pl.log == nil {
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("publish log: %w", err)
	}
	pl.logMu.Lock()
	defer pl.logMu.Unlock()
	if _, err := pl.log.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("publish log: %w", err)
	}
	return nil
}
