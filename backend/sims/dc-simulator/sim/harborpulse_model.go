// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"time"
)

// The harborpulse day. One sim day takes harborpulseDayLength of wall time and starts at
// harborpulseStartHour, so a fresh run opens on the morning commute and reaches the storm about
// halfway through. The day loops.
const (
	harborpulseDayLength = 24 * time.Minute
	harborpulseStartHour = 6.0
)

// harborHour is the sim hour of day, in [0, 24), at elapsed wall time since the first tick, for a
// day of the default length.
func harborHour(elapsed time.Duration) float64 { return harborHourOf(elapsed, harborpulseDayLength) }

// harborDay is how many midnights have passed since the first tick, for a day of the default length.
func harborDay(elapsed time.Duration) int { return harborDayOf(elapsed, harborpulseDayLength) }

// harborHourOf is the sim hour of day at elapsed for a day of dayLength.
func harborHourOf(elapsed, dayLength time.Duration) float64 {
	h := math.Mod(harborpulseStartHour+24*elapsed.Seconds()/dayLength.Seconds(), 24)
	if h < 0 {
		h += 24
	}
	return h
}

// harborDayOf is how many midnights have passed at elapsed for a day of dayLength.
func harborDayOf(elapsed, dayLength time.Duration) int {
	return int(math.Floor((harborpulseStartHour + 24*elapsed.Seconds()/dayLength.Seconds()) / 24))
}

type harborKind int

const (
	harborGauge harborKind = iota
	harborPump
	harborLight
	harborCharger
	harborMeter
	harborTruck
	// harborLoraGauge is a tide gauge behind the LoRaWAN network server: it posts no HTTP
	// reading, only uplinks through the integration.
	harborLoraGauge
)

// harborDevice is one named device of the district.
type harborDevice struct {
	token, externalId, deviceType string
	kind                          harborKind
	quay                          string
	// customer is "" for the public realm (the manifest's first customer).
	customer string
	index    int
	// devEUI is a LoRaWAN gauge's DevEUI, "" for every other device.
	devEUI string
}

// The planted faults, by device.
const (
	harborFlickerLamp    = "hp-light-east-3"
	harborHotCharger     = "hp-charger-basin-2"
	harborAirLockPump    = "hp-pump-east"
	harborBasinPump      = "hp-pump-basin"
	harborSilencedLamp   = "hp-light-north-1"
	harborLoftsMeter     = "hp-meter-quayside-lofts"
	harborFerryMeter     = "hp-meter-ferry-terminal"
	harborResponseTruck  = "hp-truck-1"
	harborLegacyGauge    = "hp-legacy-gauge-basin"
	harborOutageQuay     = HarborpulseEastQuay
	harborEastQuayOffset = 0.3
)

var harborQuays = []struct {
	token, name string
}{{HarborpulseNorthQuay, "north"}, {HarborpulseEastQuay, "east"}, {HarborpulseBasin, "basin"}}

// harborLayout is the whole fleet, in a fixed order: 9 gauges, 3 pumps, 12 lights, 6 chargers,
// 2 meters and a truck.
func harborLayout() []harborDevice {
	var out []harborDevice
	add := func(kind harborKind, deviceType, prefix, quay, quayName string, n int) {
		for i := 1; i <= n; i++ {
			token := fmt.Sprintf("hp-%s-%s-%d", prefix, quayName, i)
			ext := fmt.Sprintf("PAW-%s-%s-%d", upper(prefix), upper(quayName), i)
			out = append(out, harborDevice{token: token, externalId: ext, deviceType: deviceType, kind: kind, quay: quay, index: i})
		}
	}
	for _, q := range harborQuays {
		add(harborGauge, HarborpulseGaugeType, "gauge", q.token, q.name, 3)
	}
	for _, q := range harborQuays {
		out = append(out, harborDevice{
			token: "hp-pump-" + q.name, externalId: "PAW-PUMP-" + upper(q.name),
			deviceType: HarborpulsePumpType, kind: harborPump, quay: q.token, index: 1,
		})
	}
	for _, q := range harborQuays {
		add(harborLight, HarborpulseLightType, "light", q.token, q.name, 4)
	}
	for _, q := range harborQuays {
		add(harborCharger, HarborpulseChargerType, "charger", q.token, q.name, 2)
	}
	out = append(out,
		harborDevice{token: harborLoftsMeter, externalId: "PAW-METER-LOFTS", deviceType: HarborpulseMeterType,
			kind: harborMeter, quay: HarborpulseNorthQuay, customer: HarborpulseLoftsToken, index: 1},
		harborDevice{token: harborFerryMeter, externalId: "PAW-METER-FERRY", deviceType: HarborpulseMeterType,
			kind: harborMeter, quay: HarborpulseBasin, customer: HarborpulseFerryToken, index: 2},
		harborDevice{token: harborResponseTruck, externalId: "PAW-TRUCK-1", deviceType: HarborpulseTruckType,
			kind: harborTruck, quay: HarborpulseBasin, index: 1},
	)
	// The LoRaWAN gauges, last so that every earlier device keeps its place: one per quay on the
	// current decoder, and one legacy gauge on the Basin.
	for i, q := range harborQuays {
		out = append(out, harborDevice{
			token: "hp-lora-gauge-" + q.name, externalId: "PAW-LORA-" + upper(q.name), deviceType: HarborpulseLoraGaugeType,
			kind: harborLoraGauge, quay: q.token, index: i + 1, devEUI: fmt.Sprintf("70B3D57ED0A1%04X", i+1),
		})
	}
	out = append(out, harborDevice{
		token: harborLegacyGauge, externalId: "PAW-LEGACY-BASIN", deviceType: HarborpulseLegacyGaugeType,
		kind: harborLoraGauge, quay: HarborpulseBasin, index: 1, devEUI: "70B3D57ED0A1000F",
	})
	return out
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// ---- The site: two fences and the truck's route (illustrative coordinates) -------------------

// 🔴 PORT ALDER IS FICTIONAL, AND SO IS WHERE IT STANDS. Every harborpulse coordinate lies in open
// Atlantic water south-west of Ireland, where no settlement is, so a map that draws the scenario
// never puts the fictional town on a real one. (The first coordinates sat on a real harbour town.)
// Keep any new coordinate within a few kilometres of these.
const (
	harborDepotLat, harborDepotLon = 49.8000, -12.2100
	harborPlainLat, harborPlainLon = 49.8020, -12.2000
)

func harborBox(lat, lon, dLat, dLon float64) [][2]float64 {
	return [][2]float64{
		{lon - dLon, lat - dLat}, {lon + dLon, lat - dLat}, {lon + dLon, lat + dLat}, {lon - dLon, lat + dLat}, {lon - dLon, lat - dLat},
	}
}

func harborpulseGeoFences() []GeoFenceSpec {
	return []GeoFenceSpec{
		{Token: HarborpulseFloodPlainFence, Name: "East Quay flood plain", Description: "The low streets behind East Quay that flood first.",
			Ring: harborBox(harborPlainLat, harborPlainLon, 0.0015, 0.0025)},
		{Token: HarborpulseDepotFence, Name: "Works depot", Description: "Where the response truck is kept.",
			Ring: harborBox(harborDepotLat, harborDepotLon, 0.0004, 0.0006)},
	}
}

// ---- Pure curves -----------------------------------------------------------------------------

func smoothstep(a, b, x float64) float64 {
	if x <= a {
		return 0
	}
	if x >= b {
		return 1
	}
	t := (x - a) / (b - a)
	return t * t * (3 - 2*t)
}

// window is 1 inside [b, c], ramping smoothly from a and down to d.
func window(a, b, c, d, x float64) float64 { return smoothstep(a, b, x) * (1 - smoothstep(c, d, x)) }

// harborTide is a simplified semi-diurnal tide, 12 h period so the day loops without a step,
// with high water at 07:00 and 19:00 (the evening one is the king tide the storm rides).
func harborTide(h float64) float64 { return 0.5 + 0.5*math.Cos(2*math.Pi*(h-19)/12) }

// harborSurge is the storm surge: a fast rise from 17:00, full from 18:30 to 20:30, gone by 22:30.
func harborSurge(h float64) float64 { return 1.0 * window(17, 18.5, 20.5, 22.5, h) }

// harborRain is the storm's inflow into a wet well, metres per wall second.
func harborRain(h float64) float64 { return 0.025 * window(16, 17, 20, 21, h) }

// The East Quay feeder outage and the Basin charger's long session, sim hours.
const (
	harborOutageFrom = 19.75
	harborOutageTo   = 21.25
	harborOverheatTo = 15.5
)

func harborOutage(quay string, h float64) bool {
	return quay == harborOutageQuay && h >= harborOutageFrom && h < harborOutageTo
}

// The air lock's window, sim hours.
const (
	harborAirLockFrom = 19.0
	harborAirLockTo   = 20.5
)

func harborOverheating(token string, h float64) bool {
	return token == harborHotCharger && h >= 14 && h < harborOverheatTo
}

// harborNoise is a deterministic jitter in [-amp, amp] for (seed, device, sample).
func harborNoise(seed int64, token string, sample int64, amp float64) float64 {
	f := fnv.New64a()
	fmt.Fprintf(f, "%d|%s|%d", seed, token, sample)
	return amp * (2*float64(f.Sum64()%10001)/10000 - 1)
}

// harborGaugeOffset is a gauge's fixed installation offset, in [-0.04, 0.04] m.
func harborGaugeOffset(seed int64, token string) float64 { return harborNoise(seed, token, -1, 0.04) }

func harborGaugeLevel(seed int64, d harborDevice, h float64, sample int64) float64 {
	level := harborTide(h) + harborSurge(h) + harborGaugeOffset(seed, d.token) + harborNoise(seed, d.token, sample, 0.005)
	if d.quay == HarborpulseEastQuay {
		level += harborEastQuayOffset
	}
	return level
}

func harborLampOn(h float64) bool { return h < 6.5 || h >= 18 }

// harborLampFault holds across the maintenance window's end on two lamps. At the default
// five-second cadence (five sim minutes), count-3 in a one-minute WALL trailing window raises
// at about 11:10, twenty wall seconds before maintenance closes at 11:30. The single off reading
// at 11:20 makes the fault intermittent without clearing it. The third-last match at about
// 11:45 leaves the half-open trailing minute at about 12:45 (cadence phase can shift this toward
// 12:50). Thus it remains active past the worst default scheduler pass, about 12:31, and the
// held page can release; notification-management drops a held page whose alarm already cleared.
func harborLampFault(token string, h float64) bool {
	// harborHourOf uses floating-point hours: an exact 11:20 tick can be a few ulps below
	// 11+20/60. Normalize to nanosecond sim minutes so the single off reading is not lost.
	minute := math.Round(h*60*1e9) / 1e9
	return (token == harborFlickerLamp || token == HarborpulseControlLamp) && minute >= 660 && minute < 720 && !(minute >= 680 && minute < 685)
}

// harborAmbient is the air temperature a charger's connector starts from.
func harborAmbient(h float64) float64 {
	if h < 6 || h > 20 {
		return 18
	}
	return 18 + 12*math.Sin(math.Pi*(h-6)/14)
}

// harborChargerDemand is what a charger's session would draw, before its cap.
func harborChargerDemand(d harborDevice, h float64) float64 {
	if harborOverheating(d.token, h) {
		return 22
	}
	occ := 0.15 + 0.6*window(7, 8, 9.5, 10.5, h) + 0.5*window(16.5, 17.5, 19.5, 20.5, h)
	return 22 * occ * (0.8 + 0.2*math.Sin(float64(d.index)*1.7+float64(len(d.quay))))
}

func harborConnectorTemp(token string, h, power float64) float64 {
	t := harborAmbient(h) + 1.2*power
	if harborOverheating(token, h) {
		t += 1.6 * power
	}
	return t
}

func harborMeterKw(token string, h float64) float64 {
	if token == harborFerryMeter {
		return 60 + 110*window(8.0, 8.5, 9.75, 10.25, h)
	}
	return 35 + 45*window(17, 18.5, 21.5, 23, h) + 10*window(6.5, 7.5, 8.5, 9.5, h)
}

// harborTruckFix is the truck's position: at the depot, out to the flood plain from 17:00,
// parked there from 18:00 to 21:30, back by 22:30.
func harborTruckFix(h float64) Fix {
	var frac, speed, heading float64
	switch {
	case h >= 17 && h < 18:
		frac, speed = h-17, 8
		heading = canonicalHeading(math.Atan2(harborPlainLon-harborDepotLon, harborPlainLat-harborDepotLat) * 180 / math.Pi)
	case h >= 18 && h < 21.5:
		frac = 1
	case h >= 21.5 && h < 22.5:
		frac, speed = 1-(h-21.5), 8
		heading = canonicalHeading(math.Atan2(harborDepotLon-harborPlainLon, harborDepotLat-harborPlainLat) * 180 / math.Pi)
	}
	return Fix{
		Latitude:  harborDepotLat + frac*(harborPlainLat-harborDepotLat),
		Longitude: harborDepotLon + frac*(harborPlainLon-harborDepotLon),
		Speed:     &speed,
		Heading:   &heading,
	}
}

// ---- The stateful half: what the platform's commands change -----------------------------------

const (
	harborPumpRate     = 0.05 // m/s drained at 100 % duty
	harborFloatStart   = 3.2  // the pump's own float switch
	harborLowCutoff    = 0.8  // the pump's own low-level stop
	harborWellCeiling  = 4.0  // the well overflows into the street above this
	harborWellStart    = 1.0
	harborChargerMaxKw = 22.0
	harborMaxStep      = time.Second
	// harborMaxGap bounds how much wall time one advance integrates: a sim stopped for an hour
	// resumes where it stopped instead of replaying the hour in one tick.
	harborMaxGap = time.Minute
	// harborBufferCap is the least a charger's store-and-forward buffer holds. The buffer is
	// bounded, as a device's memory would be, but sized from the day so that a clean outage never
	// overflows it: a reading evicted after the ledger counted it would turn the clean day red.
	harborBufferCap = 512
	// harborMaxBufferCap is the most a charger's buffer may be sized to; a day-length and cadence
	// that would need more are refused (ConfigureScenario).
	harborMaxBufferCap = 1 << 16
	// harborTierDip is how long the flood-clear-between-tiers defect holds a gauge below the minor
	// tier when its water climbs into a higher one: five of the verifier's two-second polls, and
	// short enough to leave the major tier visible on East Quay at the default day.
	harborTierDip = 12 * time.Second
	// harborDropNth is how often the drop-backfill defect loses a buffered reading.
	harborDropNth = 5
)

type harborPumpState struct {
	well    float64
	running bool
	duty    float64
}

type harborBuffered struct {
	at     time.Time
	values map[string]float64
}

// harborBackfill is one charger's buffered readings, oldest first.
type harborBackfill struct {
	token    string
	readings []harborBuffered
}

type harborModel struct {
	mu        sync.Mutex
	seed      int64
	dayLength time.Duration
	defects   harborDefects
	devices   map[string]harborDevice
	elapsed   time.Duration
	day       int
	pumps     map[string]*harborPumpState
	cap       map[string]float64
	dim       map[string]float64
	flushes   map[string]int
	kwh       map[string]float64
	// lastMeter is when each meter last read, for its energy integral.
	lastMeter map[string]time.Duration
	buffers   map[string][]harborBuffered
	// outageNext is the next device-local outage log sample, independent of emit ticks.
	outageNext map[string]time.Duration
	// uplinks counts each LoRaWAN gauge's uplinks: its frame counter, which also alternates the
	// frame layout it sends.
	uplinks map[string]int
	// bufferCap is how many readings a charger's buffer holds (harborBufferCapFor).
	bufferCap int
	// tier and dipUntil are the flood-clear-between-tiers defect's state, per gauge: the highest
	// flood tier its water has reached since it last fell below the minor tier, and the elapsed
	// time its dropout ends.
	tier     map[string]int
	dipUntil map[string]time.Duration
	// ledger is what each charger BUFFERED in each outage, keyed by device and sim day: the
	// expectation a verifier holds the platform's stored readings against. It counts what the
	// device took, not what it delivered, so a delivery that loses readings shows as a shortfall.
	ledger map[harborLedgerKey]*ScenarioBackfill
}

type harborLedgerKey struct {
	token string
	day   int
}

func newHarborModel(seed int64) *harborModel {
	return newHarborModelWith(seed, harborpulseDayLength, harborDefects{})
}

func newHarborModelWith(seed int64, dayLength time.Duration, defects harborDefects) *harborModel {
	m := &harborModel{
		seed: seed, dayLength: dayLength, defects: defects, devices: map[string]harborDevice{}, pumps: map[string]*harborPumpState{},
		cap: map[string]float64{}, dim: map[string]float64{}, flushes: map[string]int{}, kwh: map[string]float64{}, lastMeter: map[string]time.Duration{},
		buffers: map[string][]harborBuffered{}, outageNext: map[string]time.Duration{}, ledger: map[harborLedgerKey]*ScenarioBackfill{}, uplinks: map[string]int{},
		bufferCap: harborBufferCapFor(dayLength, DefaultEmitInterval),
		tier:      map[string]int{}, dipUntil: map[string]time.Duration{},
	}
	for _, d := range harborLayout() {
		m.devices[d.token] = d
		switch d.kind {
		case harborPump:
			m.pumps[d.token] = &harborPumpState{well: harborWellStart, duty: 100}
		case harborCharger:
			m.cap[d.token] = harborChargerMaxKw
		case harborLight:
			m.dim[d.token] = 100
		}
	}
	return m
}

// advanceTo integrates the wet wells up to elapsed, in steps of at most a second.
func (m *harborModel) advanceTo(elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if elapsed <= m.elapsed {
		return
	}
	if elapsed-m.elapsed > harborMaxGap {
		m.elapsed = elapsed - harborMaxGap
	}
	for m.elapsed < elapsed {
		step := harborMaxStep
		if elapsed-m.elapsed < step {
			step = elapsed - m.elapsed
		}
		m.elapsed += step
		if day := harborDayOf(m.elapsed, m.dayLength); day != m.day {
			// Midnight: the operator's overnight reset. Derates and dimming expire, so the next
			// day's beats can happen again.
			m.day = day
			for t := range m.cap {
				m.cap[t] = harborChargerMaxKw
			}
			for t := range m.dim {
				m.dim[t] = 100
			}
		}
		h := harborHourOf(m.elapsed, m.dayLength)
		for token, p := range m.pumps {
			locked := m.airLocked(token, h)
			if locked || (!p.running && p.well >= harborFloatStart) {
				if !p.running {
					p.duty = 100
				}
				p.running = true
			}
			out := 0.0
			if p.running && !locked {
				out = harborPumpRate * p.duty / 100
			}
			p.well = math.Max(0, math.Min(harborWellCeiling, p.well+(harborRain(h)-out)*step.Seconds()))
			if p.running && !locked && p.well <= harborLowCutoff {
				p.running = false
			}
		}
	}
}

// reading is device token's measurements at elapsed, or nil when it publishes nothing this tick.
// A charger whose feeder is down buffers the reading it would have sent, stamped at.
func (m *harborModel) reading(token string, elapsed time.Duration, at time.Time) map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[token]
	if !ok {
		return nil
	}
	h := harborHourOf(elapsed, m.dayLength)
	sample := int64(elapsed / time.Second)
	switch d.kind {
	case harborGauge:
		level := harborGaugeLevel(m.seed, d, h, sample)
		if m.defects.floodClearBetweenTiers {
			level = m.dipBetweenTiers(token, level, elapsed)
		}
		return map[string]float64{
			HarborpulseWaterLevelKey: round3(level),
			HarborpulseBatteryKey:    round3(3.6 + harborNoise(m.seed, token, sample, 0.01)),
		}
	case harborPump:
		p := m.pumps[token]
		current, flow := 0.0, 0.0
		if p.running {
			if m.airLocked(token, h) {
				current = 0.8 + harborNoise(m.seed, token, sample, 0.1)
			} else {
				current = 17.5*p.duty/100 + harborNoise(m.seed, token, sample, 0.3)
				flow = 120 * p.duty / 100
			}
		}
		return map[string]float64{
			HarborpulseWetWellKey:     round3(p.well + harborNoise(m.seed, token, sample, 0.005)),
			HarborpulsePumpRunningKey: boolNum(p.running),
			HarborpulsePumpCurrentKey: round3(current),
			HarborpulseFlowKey:        round3(flow),
		}
	case harborLight:
		if harborOutage(d.quay, h) || (m.defects.silenceNorthLamp && token == harborSilencedLamp && harborOutage(harborOutageQuay, h)) {
			return nil
		}
		on := harborLampOn(h)
		power := 0.5
		if on {
			power = 80 * m.dim[token] / 100
		}
		return map[string]float64{
			HarborpulseLampOnKey:    boolNum(on),
			HarborpulseLampPowerKey: round3(power),
			HarborpulseLampFaultKey: boolNum(harborLampFault(token, h)),
			HarborpulseDimKey:       m.dim[token],
		}
	case harborCharger:
		m.logOutage(d, elapsed, at)
		power := math.Min(harborChargerDemand(d, h), m.cap[token])
		values := map[string]float64{
			HarborpulseChargerPowerKey: round3(power),
			HarborpulseSessionKey:      boolNum(power > 1),
			HarborpulseConnectorKey:    round3(harborConnectorTemp(token, h, power) + harborNoise(m.seed, token, sample, 0.3)),
			HarborpulseMaxKwKey:        m.cap[token],
		}
		if harborOutage(d.quay, h) {
			return nil
		}
		return values
	case harborMeter:
		kw := harborMeterKw(token, h) + harborNoise(m.seed, token, sample, 1.5)
		// Energy is power over SIM time since the meter's previous reading.
		if last, seen := m.lastMeter[token]; seen && elapsed > last {
			m.kwh[token] += kw * 24 * (elapsed - last).Seconds() / m.dayLength.Seconds()
		}
		m.lastMeter[token] = elapsed
		return map[string]float64{HarborpulseMeterKwKey: round3(kw), HarborpulseMeterKwhKey: round3(m.kwh[token])}
	case harborTruck:
		fix := m.truckFix(h)
		return map[string]float64{HarborpulseSpeedKey: *fix.Speed}
	}
	return nil
}

// truckFix is the truck's position at sim hour h; the truck-stays-home defect keeps it at the depot.
func (m *harborModel) truckFix(h float64) Fix {
	if m.defects.truckStaysHome {
		return harborTruckFix(0)
	}
	return harborTruckFix(h)
}

// fix is the device's position this tick; only the truck reports one.
func (m *harborModel) fix(token string, elapsed time.Duration) (Fix, bool) {
	if d, ok := m.devices[token]; !ok || d.kind != harborTruck {
		return Fix{}, false
	}
	return m.truckFix(harborHourOf(elapsed, m.dayLength)), true
}

// takeBackfill hands over every buffer whose feeder is back, oldest reading first.
func (m *harborModel) takeBackfill(elapsed time.Duration) []harborBackfill {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := harborHourOf(elapsed, m.dayLength)
	var out []harborBackfill
	for token, buf := range m.buffers {
		if len(buf) == 0 || harborOutage(m.devices[token].quay, h) {
			continue
		}
		if m.defects.dropBackfill {
			buf = harborDropEveryNth(buf, harborDropNth)
		}
		out = append(out, harborBackfill{token: token, readings: buf})
		delete(m.buffers, token)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].token < out[j].token })
	return out
}

// requeue puts readings that were not delivered back at the FRONT of the device's buffer.
func (m *harborModel) requeue(token string, readings []harborBuffered) {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := append(append([]harborBuffered{}, readings...), m.buffers[token]...)
	if len(buf) > m.bufferCap {
		buf = buf[len(buf)-m.bufferCap:]
	}
	m.buffers[token] = buf
}

// command applies one command to the device and reports whether it was accepted. A command the
// device cannot carry out — an unknown key, a missing or out-of-range argument, a key meant for
// another kind of device — is refused, never acknowledged.
func (m *harborModel) command(token, key string, payload json.RawMessage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[token]
	if !ok {
		return false
	}
	switch {
	case d.kind == harborPump && key == HarborpulseStartPumpKey:
		duty, ok := harborParam(payload, HarborpulseDutyParam, 50, 100)
		if !ok {
			return false
		}
		p := m.pumps[token]
		p.running, p.duty = true, duty
		return true
	case d.kind == harborPump && key == HarborpulseStopPumpKey:
		// A latched contactor does not open on command; the device says so.
		if m.airLocked(token, harborHourOf(m.elapsed, m.dayLength)) {
			return false
		}
		m.pumps[token].running = false
		return true
	case d.kind == harborCharger && key == HarborpulseDerateKey:
		if m.defects.refuseDerate {
			return false
		}
		kw, ok := harborParam(payload, HarborpulseMaxKwParam, 3.7, harborChargerMaxKw)
		if !ok {
			return false
		}
		m.cap[token] = kw
		return true
	case d.kind == harborPump && key == HarborpulseFlushKey:
		// A back-flush: the controller runs it and reports done. It moves no water the model
		// tracks, so the beat is WHEN it ran, which the maintenance window decides.
		m.flushes[token]++
		return true
	case d.kind == harborLight && key == HarborpulseSetDimKey:
		level, ok := harborParam(payload, HarborpulseLevelParam, 0, 100)
		if !ok {
			return false
		}
		m.dim[token] = level
		return true
	}
	return false
}

// airLocked reports whether the pump is air-locked at sim hour h: the East Quay pump, or the Basin
// pump when that defect is planted.
func (m *harborModel) airLocked(token string, h float64) bool {
	pump := harborAirLockPump
	if m.defects.airLockBasin {
		pump = harborBasinPump
	}
	return token == pump && h >= harborAirLockFrom && h < harborAirLockTo
}

// recordBuffered files one buffered reading in the outage ledger. Caller holds m.mu.
func (m *harborModel) recordBuffered(token string, day int, at time.Time, values map[string]float64) {
	k := harborLedgerKey{token: token, day: day}
	e := m.ledger[k]
	stamp := at.UTC().Format(time.RFC3339Nano)
	if e == nil {
		e = &ScenarioBackfill{Device: token, Day: day, First: stamp}
		m.ledger[k] = e
	}
	e.Buffered++
	e.Last = stamp
	e.Times = append(e.Times, stamp)
	e.PowerKW = append(e.PowerKW, values[HarborpulseChargerPowerKey])
	e.Values = append(e.Values, values)
}

// Each charger logs 90 entries during an outage, at outage-duration/90, even
// between uplinks. Scaling with the day keeps the restore bounded at every day length.
func harborOutageCadence(dayLength time.Duration) time.Duration {
	return time.Duration((harborOutageTo-harborOutageFrom)/24*float64(dayLength)) / 90
}

func harborBufferNeed(dayLength, interval time.Duration) int {
	outage := time.Duration((harborOutageTo - harborOutageFrom) / 24 * float64(dayLength))
	cadence := harborOutageCadence(dayLength)
	return int((outage + cadence - 1) / cadence)
}

// logOutage materializes the charger's local log through elapsed. The uplink
// ticker merely collects it; a slow or missed tick does not lose outage samples.
// Caller holds m.mu. Values are immutable once recorded.
func (m *harborModel) logOutage(d harborDevice, elapsed time.Duration, at time.Time) {
	if d.quay != HarborpulseEastQuay {
		return
	}
	from := time.Duration((harborOutageFrom - harborpulseStartHour) / 24 * float64(m.dayLength))
	next, ok := m.outageNext[d.token]
	if !ok {
		next = from
	}
	cadence := harborOutageCadence(m.dayLength)
	for next <= elapsed {
		day := int((next - from) / m.dayLength)
		end := from + time.Duration(day)*m.dayLength + 90*cadence
		if next >= end {
			next = from + time.Duration(day+1)*m.dayLength
			continue
		}
		h := harborHourOf(next, m.dayLength)
		power := math.Min(harborChargerDemand(d, h), m.cap[d.token])
		values := map[string]float64{
			HarborpulseChargerPowerKey: round3(power), HarborpulseSessionKey: boolNum(power > 1),
			HarborpulseConnectorKey: round3(harborConnectorTemp(d.token, h, power) + harborNoise(m.seed, d.token, int64(next/time.Second), 0.3)),
			HarborpulseMaxKwKey:     m.cap[d.token],
		}
		instant := at.Add(next - elapsed).UTC()
		stamp := instant.Truncate(time.Microsecond)
		if stamp.Before(instant) {
			// Round into the outage window: the platform stores microseconds,
			// while the scenario's independently known boundary can have nanoseconds.
			stamp = stamp.Add(time.Microsecond)
		}
		m.recordBuffered(d.token, day, stamp, values)
		buf := append(m.buffers[d.token], harborBuffered{at: stamp, values: values})
		if len(buf) > m.bufferCap {
			buf = buf[len(buf)-m.bufferCap:]
		}
		m.buffers[d.token] = buf
		next += cadence
	}
	m.outageNext[d.token] = next
}

// harborBufferCapFor sizes a charger's buffer for the day: twice one outage, since readings the
// ingress refused go back on the buffer and may still be there when the next day's outage begins.
func harborBufferCapFor(dayLength, interval time.Duration) int {
	return max(harborBufferCap, 2*harborBufferNeed(dayLength, interval))
}

// dipBetweenTiers is the flood-clear-between-tiers defect: when the gauge's water climbs into a
// flood tier above the highest it has reached, it reads below the minor tier for harborTierDip,
// so the platform clears its flood alarm and reactivates it at the higher tier. Caller holds m.mu.
func (m *harborModel) dipBetweenTiers(token string, level float64, elapsed time.Duration) float64 {
	t := harborFloodTier(level)
	switch {
	case t == 0:
		m.tier[token] = 0
	case t > m.tier[token]:
		if m.tier[token] > 0 {
			m.dipUntil[token] = elapsed + harborTierDip
		}
		m.tier[token] = t
	}
	if elapsed < m.dipUntil[token] {
		return HarborpulseFloodMinorM - 0.3
	}
	return level
}

// harborFloodTier is the flood tier a water level is in: 0 below minor, 1 minor, 2 major, 3 critical.
func harborFloodTier(level float64) int {
	switch {
	case level > HarborpulseFloodCriticalM:
		return 3
	case level > HarborpulseFloodMajorM:
		return 2
	case level > HarborpulseFloodMinorM:
		return 1
	}
	return 0
}

// backfillLedger is every outage's buffered readings so far, by device then day.
func (m *harborModel) backfillLedger() []ScenarioBackfill {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ScenarioBackfill, 0, len(m.ledger))
	for _, e := range m.ledger {
		c := *e
		c.Times = append([]string(nil), e.Times...)
		c.PowerKW = append([]float64(nil), e.PowerKW...)
		c.Values = make([]map[string]float64, len(e.Values))
		for i, values := range e.Values {
			c.Values[i] = make(map[string]float64, len(values))
			for key, value := range values {
				c.Values[i][key] = value
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Day < out[j].Day
	})
	return out
}

// harborDropEveryNth is the drop-backfill defect: it loses every nth buffered reading on the way
// out, as a device with a broken store-and-forward queue would.
func harborDropEveryNth(buf []harborBuffered, n int) []harborBuffered {
	out := make([]harborBuffered, 0, len(buf))
	for i, r := range buf {
		if (i+1)%n != 0 {
			out = append(out, r)
		}
	}
	return out
}

func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
