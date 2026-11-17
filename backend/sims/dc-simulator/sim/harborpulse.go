// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/rs/zerolog/log"
)

// harborpulse is the waterfront scenario: one utility tenant running a fictional harbour
// district's tide gauges, stormwater pumps, street lights, public EV chargers, two metered
// buildings and a flood-response truck, through one compressed day with a storm, a king tide, a
// feeder outage and a store-and-forward burst. It exists to drive MORE of the platform at once
// than any earlier scenario: seven DETECT rule kinds, severity escalation under one alarm key,
// two command-carrying rules whose commands visibly change the telemetry, customer scoping,
// geofenced positions and backdated readings.
//
// The story, one beat per rule (sim hours; one sim day is harborpulseDayLength of wall time):
//
//	08:30-09:45 peak-demand     the Ferry Terminal's 1-minute average demand passes its cap (MINOR)
//	11:00-12:00 lamp-flicker    one East Quay lamp's driver reports intermittent faults (repeating, MINOR)
//	14:00-15:30 connector-hot   a Basin charger's connector overheats under a long session
//	                            (sliding average, MAJOR) and is sent `derate`; obeying it cools it
//	17:00-18:30 surge-rising    every gauge rises faster than the rate limit (deltaRate, WARNING)
//	17:00-21:00 flood-level     three thresholds under ONE alarm key: each gauge's alarm escalates
//	                            in place MINOR -> MAJOR, and to CRITICAL on East Quay only
//	18:30-20:30 quay-flooding   three East Quay gauges above the correlation level together
//	                            (area correlation: a signal, never an alarm — the platform forbids
//	                            actions on it)
//	17:00-21:00 wet-well-high   a filling wet well raises MAJOR and sends `start-pump`; pumping
//	                            drains it and the alarm clears, several times through the storm
//	19:00-20:30 pump-air-lock   the East Quay pump runs and moves nothing (duration, CRITICAL)
//	19:45-21:15 light-silent /  the East Quay feeder fails: its lamps and chargers go silent
//	            charger-offline (absence, MAJOR); the chargers buffer and, on restore, flush their
//	                            readings with their original timestamps
//
// 🔴 A RULE COMMANDS ONLY THE DEVICE THAT PRODUCED THE EVENT. So the pump controller senses its own
// wet well and the charger its own connector: nothing here pretends a gauge can start a pump.
//
// Every reading is a pure function of (seed, sim time, device) EXCEPT what the platform's
// reactions change — a wet well, a charger's power cap, a lamp's dim level — so a command that
// was not obeyed is visible in the telemetry rather than only in a command status.
type harborpulse struct {
	seed int64
	load Load
	// dayLength and defects are the ScenarioOptions; set before the first Manifest or Tick.
	dayLength time.Duration
	defects   harborDefects

	mu      sync.Mutex
	model   *harborModel
	started time.Time
	now     func() time.Time
}

// NewHarborpulse returns the harborpulse Sim seeded from the handshake. Prefer NewSim.
func NewHarborpulse(seed int64, load Load) Sim {
	return &harborpulse{seed: seed, load: load, dayLength: harborpulseDayLength, now: time.Now}
}

// The planted defects harborpulse can run with (--scenario-defect). Each breaks ONE thing the day
// demonstrates, so that the scenario verifier can be shown to turn the matching invariant red.
const (
	// HarborpulseDefectFloodSplitKey files the critical flood tier under an alarm key of its own,
	// so an East Quay gauge holds two alarms instead of one escalating in place.
	HarborpulseDefectFloodSplitKey = "flood-split-key"
	// HarborpulseDefectRefuseDerate makes every charger refuse derate.
	HarborpulseDefectRefuseDerate = "refuse-derate"
	// HarborpulseDefectAirLockBasin plants the air lock on the Basin pump instead of East Quay's.
	HarborpulseDefectAirLockBasin = "air-lock-basin"
	// HarborpulseDefectSilenceNorthLamp silences one North Quay lamp through the East Quay outage.
	HarborpulseDefectSilenceNorthLamp = "silence-north-lamp"
	// HarborpulseDefectDropBackfill loses every fifth buffered reading on restore.
	HarborpulseDefectDropBackfill = "drop-backfill"
)

// HarborpulseDefects lists every defect harborpulse knows, sorted.
func HarborpulseDefects() []string {
	return []string{
		HarborpulseDefectAirLockBasin, HarborpulseDefectDropBackfill, HarborpulseDefectFloodSplitKey,
		HarborpulseDefectRefuseDerate, HarborpulseDefectSilenceNorthLamp,
	}
}

// HarborpulseFloodSplitAlarmKey is the alarm key the flood-split-key defect files the critical
// tier under.
const HarborpulseFloodSplitAlarmKey = HarborpulseFloodAlarmKey + "-critical"

// HarborpulseMinDayLength is the shortest day --day-length accepts: the default. The rules' windows,
// holds and timeouts are WALL time (a 45 s silence, a 30 s hold, a one-minute average) and the
// beats are sized against a 24-minute day; a shorter one would make the outage shorter than the
// silence that detects it. A longer day stretches every beat and keeps them all detectable.
const HarborpulseMinDayLength = harborpulseDayLength

// HarborpulseMaxDayLength is the longest day --day-length accepts.
const HarborpulseMaxDayLength = 24 * time.Hour

type harborDefects struct {
	floodSplitKey, refuseDerate, airLockBasin, silenceNorthLamp, dropBackfill bool
	names                                                                     []string
}

// ConfigureScenario applies --day-length and --scenario-defect.
func (s *harborpulse) ConfigureScenario(o ScenarioOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model != nil {
		return fmt.Errorf("harborpulse: the day is already running; options are set before it starts")
	}
	if o.DayLength != 0 {
		if o.DayLength < HarborpulseMinDayLength || o.DayLength > HarborpulseMaxDayLength {
			return fmt.Errorf("--day-length %s is outside [%s, %s]: the rules' windows are wall time and the beats are "+
				"sized against a %s day, so a shorter day makes beats shorter than the rules that detect them",
				o.DayLength, HarborpulseMinDayLength, HarborpulseMaxDayLength, harborpulseDayLength)
		}
		s.dayLength = o.DayLength
	}
	names, err := parseDefects(o.Defects, HarborpulseDefects())
	if err != nil {
		return err
	}
	d := harborDefects{names: names}
	for _, n := range names {
		switch n {
		case HarborpulseDefectFloodSplitKey:
			d.floodSplitKey = true
		case HarborpulseDefectRefuseDerate:
			d.refuseDerate = true
		case HarborpulseDefectAirLockBasin:
			d.airLockBasin = true
		case HarborpulseDefectSilenceNorthLamp:
			d.silenceNorthLamp = true
		case HarborpulseDefectDropBackfill:
			d.dropBackfill = true
		}
	}
	s.defects = d
	return nil
}

// harborBeats is the story, as /status names it: each beat and the sim hours it spans.
var harborBeats = []struct {
	name     string
	from, to float64
}{
	{"night", 0, 6.5},
	{"commute", 7.5, 10},
	{"peak-demand", 8.5, 9.75},
	{"lamp-flicker", 11, 12},
	{"connector-hot", 14, harborOverheatTo},
	{"storm", 16, 22.5},
	{"air-lock", harborAirLockFrom, harborAirLockTo},
	{"feeder-outage", harborOutageFrom, harborOutageTo},
}

func harborBeatAt(h float64) string {
	var on []string
	for _, b := range harborBeats {
		if h >= b.from && h < b.to {
			on = append(on, b.name)
		}
	}
	if len(on) == 0 {
		return "quiet"
	}
	return strings.Join(on, ",")
}

// ScenarioClock reports the compressed day on /status.
func (s *harborpulse) ScenarioClock(now time.Time) *ScenarioClock {
	s.mu.Lock()
	started, dayLength, defects, model := s.started, s.dayLength, s.defects.names, s.model
	s.mu.Unlock()
	c := &ScenarioClock{
		Name: "harborpulse", DayLengthSeconds: dayLength.Seconds(), StartHour: harborpulseStartHour,
		SimHour: harborpulseStartHour, Defects: defects,
	}
	if !started.IsZero() {
		elapsed := now.Sub(started)
		c.Started = true
		c.StartedAt = started.UTC().Format(time.RFC3339Nano)
		c.Day = harborDayOf(elapsed, dayLength)
		c.SimHour = harborHourOf(elapsed, dayLength)
	}
	c.SimTime = formatSimTime(c.SimHour)
	c.Beat = harborBeatAt(c.SimHour)
	if model != nil {
		c.Backfill = model.backfillLedger()
	}
	return c
}

// Entity tokens. Fixed literals, read from several places, for the reason every scenario's are.
const (
	HarborpulseCustomerTypeToken = "hp-owner"
	// HarborpulsePublicRealmToken owns the district's own equipment. It is FIRST among the
	// customers on purpose: a named device with no customer falls back to the first one, and the
	// fallback must be the utility's public realm, never a building owner.
	HarborpulsePublicRealmToken = "hp-public-realm"
	HarborpulseLoftsToken       = "hp-quayside-lofts"
	HarborpulseFerryToken       = "hp-ferry-terminal"
	// HarborpulseLoftsOwnerEmail is the customer user limited to Quayside Lofts.
	HarborpulseLoftsOwnerEmail = "owner@quayside-lofts.example"

	HarborpulseQuayTypeToken = "hp-quay"
	HarborpulseNorthQuay     = "hp-north-quay"
	HarborpulseEastQuay      = "hp-east-quay"
	HarborpulseBasin         = "hp-basin"

	HarborpulseFloodPlainFence = "hp-flood-plain"
	HarborpulseDepotFence      = "hp-depot"

	HarborpulseGaugeProfile   = "hp-gauge-profile"
	HarborpulsePumpProfile    = "hp-pump-profile"
	HarborpulseLightProfile   = "hp-light-profile"
	HarborpulseChargerProfile = "hp-charger-profile"
	HarborpulseMeterProfile   = "hp-meter-profile"
	HarborpulseTruckProfile   = "hp-truck-profile"

	HarborpulseGaugeType   = "hp-tide-gauge"
	HarborpulsePumpType    = "hp-pump-controller"
	HarborpulseLightType   = "hp-street-light"
	HarborpulseChargerType = "hp-ev-charger"
	HarborpulseMeterType   = "hp-building-meter"
	HarborpulseTruckType   = "hp-response-truck"
)

// Metric vocabulary.
const (
	HarborpulseWaterLevelKey = "water_level_m"
	HarborpulseBatteryKey    = "battery_v"

	HarborpulseWetWellKey     = "wet_well_m"
	HarborpulsePumpRunningKey = "pump_running"
	HarborpulsePumpCurrentKey = "pump_current_a"
	HarborpulseFlowKey        = "flow_lps"

	HarborpulseLampOnKey    = "lamp_on"
	HarborpulseLampPowerKey = "power_w"
	HarborpulseLampFaultKey = "lamp_fault"
	HarborpulseDimKey       = "dim_pct"

	HarborpulseChargerPowerKey = "power_kw"
	HarborpulseSessionKey      = "session_active"
	HarborpulseConnectorKey    = "connector_temp_c"
	HarborpulseMaxKwKey        = "max_kw"

	HarborpulseMeterKwKey  = "kw"
	HarborpulseMeterKwhKey = "kwh"

	HarborpulseSpeedKey = "speed_mps"
)

// Typed commands. Token (the definition) and key (what a rule or a button sends) differ.
const (
	HarborpulseStartPumpDef = "hp-cmd-start-pump"
	HarborpulseStartPumpKey = "start-pump"
	HarborpulseStopPumpDef  = "hp-cmd-stop-pump"
	HarborpulseStopPumpKey  = "stop-pump"
	HarborpulseDerateDef    = "hp-cmd-derate"
	HarborpulseDerateKey    = "derate"
	HarborpulseSetDimDef    = "hp-cmd-set-dim"
	HarborpulseSetDimKey    = "set-dim"

	HarborpulseDutyParam  = "duty_pct"
	HarborpulseMaxKwParam = "max_kw"
	HarborpulseLevelParam = "level_pct"

	harborpulseStartPumpSchema = `[{"name":"duty_pct","kind":"SCALAR","dataType":"INT","unit":"%","required":true,"minValue":50,"maxValue":100}]`
	harborpulseDerateSchema    = `[{"name":"max_kw","kind":"SCALAR","dataType":"DOUBLE","unit":"kW","required":true,"minValue":3.7,"maxValue":22}]`
	harborpulseSetDimSchema    = `[{"name":"level_pct","kind":"SCALAR","dataType":"INT","unit":"%","required":true,"minValue":0,"maxValue":100}]`
)

// Rule tokens, alarm keys and the levels they fire at. The model's curves are built against
// these numbers and harborpulse_test.go reads them back OUT OF THE RULE DEFINITIONS, so moving a
// threshold without moving the curve that crosses it fails a test rather than a demo.
const (
	HarborpulseFloodMinorRule    = "hp-flood-minor"
	HarborpulseFloodMajorRule    = "hp-flood-major"
	HarborpulseFloodCriticalRule = "hp-flood-critical"
	HarborpulseFloodAlarmKey     = "flood-level"
	HarborpulseFloodMinorM       = 1.4
	HarborpulseFloodMajorM       = 1.8
	HarborpulseFloodCriticalM    = 2.2

	HarborpulseSurgeRule     = "hp-surge-rising"
	HarborpulseSurgeAlarmKey = "surge-rising"
	// HarborpulseSurgeMPerS is the rise rate, in metres per second of WALL time, that the
	// surge exceeds and a tide never does (the day is compressed, so a real rate would be
	// sixty times smaller).
	HarborpulseSurgeMPerS = 0.01

	HarborpulseQuayFloodingRule = "hp-quay-flooding"
	HarborpulseQuayFloodingM    = 2.1
	HarborpulseQuayFloodingN    = 3
	HarborpulseQuayFloodWindow  = 2 * time.Minute

	HarborpulseWetWellRule     = "hp-wet-well-high"
	HarborpulseWetWellAlarmKey = "wet-well-high"
	HarborpulseWetWellM        = 2.5

	HarborpulseAirLockRule     = "hp-pump-air-lock"
	HarborpulseAirLockAlarmKey = "pump-air-lock"
	HarborpulseAirLockAmps     = 2.0
	HarborpulseAirLockHold     = 30 * time.Second
	// harborpulseAirLockCEL: running AND drawing next to no current. Two metrics of one event,
	// which a structured comparison cannot express.
	harborpulseAirLockCEL = `m["pump_running"] > 0.5 && m["pump_current_a"] < 2.0`

	HarborpulseFlickerRule     = "hp-lamp-flicker"
	HarborpulseFlickerAlarmKey = "lamp-flicker"
	HarborpulseFlickerCount    = 3
	HarborpulseFlickerWindow   = time.Minute

	HarborpulseLightSilentRule     = "hp-light-silent"
	HarborpulseLightSilentAlarmKey = "light-silent"
	HarborpulseChargerOfflineRule  = "hp-charger-offline"
	HarborpulseChargerOfflineKey   = "charger-offline"
	HarborpulseSilence             = 45 * time.Second

	HarborpulseConnectorHotRule     = "hp-connector-hot"
	HarborpulseConnectorHotAlarmKey = "connector-hot"
	HarborpulseConnectorHotC        = 70.0
	HarborpulseConnectorHotWindow   = time.Minute
	// HarborpulseDeratedKw is what connector-hot asks the charger to cap itself at.
	HarborpulseDeratedKw = 11.0

	HarborpulsePeakDemandRule     = "hp-peak-demand"
	HarborpulsePeakDemandAlarmKey = "peak-demand"
	HarborpulsePeakDemandKw       = 150.0
	HarborpulsePeakDemandWindow   = time.Minute
)

func (s *harborpulse) Manifest() SimManifest {
	layout := harborLayout()
	pops := map[string][]InstanceSpec{}
	var order []string
	for _, d := range layout {
		if _, seen := pops[d.deviceType]; !seen {
			order = append(order, d.deviceType)
		}
		pops[d.deviceType] = append(pops[d.deviceType], InstanceSpec{
			Token: d.token, ExternalId: d.externalId, Areas: []string{d.quay}, Customer: d.customer,
		})
	}
	var populations []PopulationSpec
	for _, t := range order {
		populations = append(populations, PopulationSpec{OfType: t, Count: len(pops[t]), FixedCount: true, Instances: pops[t]})
	}

	manifest := resize(s.load, SimManifest{
		Name: "harborpulse",
		Seed: s.seed,
		// A composed fixture: its faults are planted on named devices (the East Quay pump, one
		// lamp, one charger, the Ferry Terminal meter), which a device-count override cannot
		// reproduce.
		FixedTopology: true,
		CommandFarEnd: FarEndInternal,
		CustomerTypes: []CustomerTypeSpec{{Token: HarborpulseCustomerTypeToken, Name: "Owner"}},
		Customers: []CustomerSpec{
			{Token: HarborpulsePublicRealmToken, Name: "Port Alder public realm", CustomerTypeToken: HarborpulseCustomerTypeToken},
			{Token: HarborpulseLoftsToken, Name: "Quayside Lofts", CustomerTypeToken: HarborpulseCustomerTypeToken},
			{Token: HarborpulseFerryToken, Name: "Ferry Terminal", CustomerTypeToken: HarborpulseCustomerTypeToken},
		},
		AreaTypes: []AreaTypeSpec{{Token: HarborpulseQuayTypeToken, Name: "Quay"}},
		Areas: []AreaSpec{
			{Token: HarborpulseNorthQuay, Name: "North Quay", AreaTypeToken: HarborpulseQuayTypeToken},
			{Token: HarborpulseEastQuay, Name: "East Quay", AreaTypeToken: HarborpulseQuayTypeToken},
			{Token: HarborpulseBasin, Name: "The Basin", AreaTypeToken: HarborpulseQuayTypeToken},
		},
		GeoFences: harborpulseGeoFences(),
		Profiles: []ProfileSpec{
			harborpulseGaugeProfile(s.defects.floodSplitKey),
			harborpulsePumpProfile(),
			harborpulseLightProfile(),
			harborpulseChargerProfile(),
			harborpulseMeterProfile(),
			harborpulseTruckProfile(),
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: HarborpulseGaugeType, Name: "Tide Gauge", ProfileToken: HarborpulseGaugeProfile},
			{Token: HarborpulsePumpType, Name: "Stormwater Pump Controller", ProfileToken: HarborpulsePumpProfile},
			{Token: HarborpulseLightType, Name: "Street Light Controller", ProfileToken: HarborpulseLightProfile},
			{Token: HarborpulseChargerType, Name: "EV Charger", ProfileToken: HarborpulseChargerProfile},
			{Token: HarborpulseMeterType, Name: "Building Meter", ProfileToken: HarborpulseMeterProfile},
			{Token: HarborpulseTruckType, Name: "Flood Response Truck", ProfileToken: HarborpulseTruckProfile},
		},
		Populations:   populations,
		CustomerUsers: []CustomerUserSpec{{Email: HarborpulseLoftsOwnerEmail, CustomerToken: HarborpulseLoftsToken}},
	})
	// The operator's board and the Lofts owner's, bound to the devices this manifest provisions.
	manifest.Dashboards = harborpulseDashboards(manifest.Expand(manifest.Seed))
	return manifest
}

// harborpulseGaugeProfile is the tide gauge. splitKey is the flood-split-key defect: the critical
// tier is filed under an alarm key of its own.
func harborpulseGaugeProfile(splitKey bool) ProfileSpec {
	tier := func(token, name string, level float64, severity string) DetectionRuleSpec {
		key := HarborpulseFloodAlarmKey
		if splitKey && severity == SeverityCritical {
			key = HarborpulseFloodSplitAlarmKey
		}
		return ThresholdAlarmRule{
			Token: token, Name: name, Metric: HarborpulseWaterLevelKey, Op: OpGt, Threshold: level,
			Severity: severity, AlarmKey: key, Enabled: true,
		}.Spec()
	}
	return ProfileSpec{
		Token: HarborpulseGaugeProfile, Name: "Harborpulse Tide Gauge Profile", Category: "water",
		Metrics: []MetricSpec{
			{Key: HarborpulseWaterLevelKey, Name: "Water level above quay datum", DataType: "DOUBLE", Unit: "m"},
			{Key: HarborpulseBatteryKey, Name: "Battery", DataType: "DOUBLE", Unit: "V"},
		},
		DetectionRules: []DetectionRuleSpec{
			// Three tiers, ONE alarm key: the platform keeps one alarm per gauge and moves its
			// severity with the water rather than opening an alarm per tier.
			tier(HarborpulseFloodMinorRule, "Harborpulse flood level minor", HarborpulseFloodMinorM, SeverityMinor),
			tier(HarborpulseFloodMajorRule, "Harborpulse flood level major", HarborpulseFloodMajorM, SeverityMajor),
			tier(HarborpulseFloodCriticalRule, "Harborpulse flood level critical", HarborpulseFloodCriticalM, SeverityCritical),
			DeltaRateAlarmRule{
				Token: HarborpulseSurgeRule, Name: "Harborpulse surge rising", Metric: HarborpulseWaterLevelKey,
				Op: OpGt, Threshold: HarborpulseSurgeMPerS, Severity: SeverityWarning,
				AlarmKey: HarborpulseSurgeAlarmKey, Enabled: true,
			}.Spec(),
			CorrelationSignalRule{
				Token: HarborpulseQuayFloodingRule, Name: "Harborpulse quay flooding", AnchorType: "area",
				Metric: HarborpulseWaterLevelKey, Op: OpGt, Threshold: HarborpulseQuayFloodingM,
				Count: HarborpulseQuayFloodingN, Window: HarborpulseQuayFloodWindow, Enabled: true,
			}.Spec(),
		},
	}
}

func harborpulsePumpProfile() ProfileSpec {
	return ProfileSpec{
		Token: HarborpulsePumpProfile, Name: "Harborpulse Pump Controller Profile", Category: "water",
		Metrics: []MetricSpec{
			{Key: HarborpulseWetWellKey, Name: "Wet well level", DataType: "DOUBLE", Unit: "m"},
			{Key: HarborpulsePumpRunningKey, Name: "Pump running (0 or 1)", DataType: "INT"},
			{Key: HarborpulsePumpCurrentKey, Name: "Motor current", DataType: "DOUBLE", Unit: "A"},
			{Key: HarborpulseFlowKey, Name: "Flow", DataType: "DOUBLE", Unit: "L/s"},
		},
		Commands: []CommandSpec{
			{Token: HarborpulseStartPumpDef, CommandKey: HarborpulseStartPumpKey, Name: "Start pump", ParameterSchema: harborpulseStartPumpSchema},
			{Token: HarborpulseStopPumpDef, CommandKey: HarborpulseStopPumpKey, Name: "Stop pump"},
		},
		DetectionRules: []DetectionRuleSpec{
			ThresholdAlarmRule{
				Token: HarborpulseWetWellRule, Name: "Harborpulse wet well high", Metric: HarborpulseWetWellKey,
				Op: OpGt, Threshold: HarborpulseWetWellM, Severity: SeverityMajor, AlarmKey: HarborpulseWetWellAlarmKey,
				CommandKey: HarborpulseStartPumpKey, CommandExpiresAfter: "10m",
				CommandPayload: map[string]any{HarborpulseDutyParam: 100}, Enabled: true,
			}.Spec(),
			DurationAlarmRule{
				Token: HarborpulseAirLockRule, Name: "Harborpulse pump air lock", Metric: HarborpulsePumpCurrentKey,
				CEL: harborpulseAirLockCEL, Hold: HarborpulseAirLockHold, Severity: SeverityCritical,
				AlarmKey: HarborpulseAirLockAlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

func harborpulseLightProfile() ProfileSpec {
	return ProfileSpec{
		Token: HarborpulseLightProfile, Name: "Harborpulse Street Light Profile", Category: "lighting",
		Metrics: []MetricSpec{
			{Key: HarborpulseLampOnKey, Name: "Lamp on (0 or 1)", DataType: "INT"},
			{Key: HarborpulseLampPowerKey, Name: "Power", DataType: "DOUBLE", Unit: "W"},
			{Key: HarborpulseLampFaultKey, Name: "Driver fault (0 or 1)", DataType: "INT"},
			{Key: HarborpulseDimKey, Name: "Dim level", DataType: "DOUBLE", Unit: "%"},
		},
		Commands: []CommandSpec{
			{Token: HarborpulseSetDimDef, CommandKey: HarborpulseSetDimKey, Name: "Set dim level", ParameterSchema: harborpulseSetDimSchema},
		},
		DetectionRules: []DetectionRuleSpec{
			RepeatingAlarmRule{
				Token: HarborpulseFlickerRule, Name: "Harborpulse lamp flicker", Metric: HarborpulseLampFaultKey,
				Op: OpGt, Threshold: 0.5, Count: HarborpulseFlickerCount, Window: HarborpulseFlickerWindow,
				Severity: SeverityMinor, AlarmKey: HarborpulseFlickerAlarmKey, Enabled: true,
			}.Spec(),
			AbsenceAlarmRule{
				Token: HarborpulseLightSilentRule, Name: "Harborpulse light silent", Metric: HarborpulseLampOnKey,
				Timeout: HarborpulseSilence, Severity: SeverityMajor, AlarmKey: HarborpulseLightSilentAlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

func harborpulseChargerProfile() ProfileSpec {
	return ProfileSpec{
		Token: HarborpulseChargerProfile, Name: "Harborpulse EV Charger Profile", Category: "energy",
		Metrics: []MetricSpec{
			{Key: HarborpulseChargerPowerKey, Name: "Charging power", DataType: "DOUBLE", Unit: "kW"},
			{Key: HarborpulseSessionKey, Name: "Session active (0 or 1)", DataType: "INT"},
			{Key: HarborpulseConnectorKey, Name: "Connector temperature", DataType: "DOUBLE", Unit: "C"},
			{Key: HarborpulseMaxKwKey, Name: "Power cap", DataType: "DOUBLE", Unit: "kW"},
		},
		Commands: []CommandSpec{
			{Token: HarborpulseDerateDef, CommandKey: HarborpulseDerateKey, Name: "Derate", ParameterSchema: harborpulseDerateSchema},
		},
		DetectionRules: []DetectionRuleSpec{
			AggregateAlarmRule{
				Token: HarborpulseConnectorHotRule, Name: "Harborpulse connector hot", Metric: HarborpulseConnectorKey,
				Op: OpGt, Threshold: HarborpulseConnectorHotC, Window: HarborpulseConnectorHotWindow,
				Severity: SeverityMajor, AlarmKey: HarborpulseConnectorHotAlarmKey,
				CommandKey: HarborpulseDerateKey, CommandPayload: map[string]any{HarborpulseMaxKwParam: HarborpulseDeratedKw},
				CommandExpiresAfter: "10m", Enabled: true,
			}.Spec(),
			AbsenceAlarmRule{
				Token: HarborpulseChargerOfflineRule, Name: "Harborpulse charger offline", Metric: HarborpulseChargerPowerKey,
				Timeout: HarborpulseSilence, Severity: SeverityMajor, AlarmKey: HarborpulseChargerOfflineKey, Enabled: true,
			}.Spec(),
		},
	}
}

func harborpulseMeterProfile() ProfileSpec {
	return ProfileSpec{
		Token: HarborpulseMeterProfile, Name: "Harborpulse Building Meter Profile", Category: "energy",
		Metrics: []MetricSpec{
			{Key: HarborpulseMeterKwKey, Name: "Demand", DataType: "DOUBLE", Unit: "kW"},
			{Key: HarborpulseMeterKwhKey, Name: "Energy", DataType: "DOUBLE", Unit: "kWh"},
		},
		DetectionRules: []DetectionRuleSpec{
			AggregateAlarmRule{
				Token: HarborpulsePeakDemandRule, Name: "Harborpulse peak demand", Metric: HarborpulseMeterKwKey,
				Op: OpGt, Threshold: HarborpulsePeakDemandKw, Window: HarborpulsePeakDemandWindow,
				Severity: SeverityMinor, AlarmKey: HarborpulsePeakDemandAlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

func harborpulseTruckProfile() ProfileSpec {
	return ProfileSpec{
		Token: HarborpulseTruckProfile, Name: "Harborpulse Response Truck Profile", Category: "vehicle",
		Metrics: []MetricSpec{{Key: HarborpulseSpeedKey, Name: "Speed", DataType: "DOUBLE", Unit: "m/s"}},
	}
}

func (s *harborpulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

func (s *harborpulse) ensureModel() *harborModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model == nil {
		s.model = newHarborModelWith(s.seed, s.dayLength, s.defects)
	}
	return s.model
}

// Tick advances the district to now and publishes every device's reading, the truck's position,
// and any readings a charger buffered while its feeder was down.
func (s *harborpulse) Tick(ctx context.Context, rt *Runtime) error {
	model := s.ensureModel()
	s.mu.Lock()
	now := s.now()
	if s.started.IsZero() {
		s.started = now
	}
	elapsed := now.Sub(s.started)
	s.mu.Unlock()

	model.advanceTo(elapsed)
	workers := rt.Load.Workers(len(rt.Devices))
	stamp := now.UTC().Truncate(time.Microsecond)

	measErr := EmitAll(ctx, rt, workers, func(_ int, d DeviceInstance) map[string]float64 {
		return model.reading(d.Token, elapsed, stamp)
	})
	if measErr != nil {
		log.Error().Err(measErr).Msg("emit harborpulse measurements failed")
	}
	locErr := EmitLocationAll(ctx, rt, workers, func(_ int, d DeviceInstance) (Fix, bool) {
		return model.fix(d.Token, elapsed)
	})
	if locErr != nil {
		log.Error().Err(locErr).Msg("emit harborpulse locations failed")
	}
	backErr := s.flushBackfill(ctx, rt, model, elapsed)
	if backErr != nil {
		log.Error().Err(backErr).Msg("flush harborpulse buffered readings failed")
	}
	return errors.Join(measErr, locErr, backErr)
}

// flushBackfill sends the readings chargers buffered while their feeder was down, oldest first,
// each with the time it was TAKEN. A reading the ingress did not accept goes back on the
// device's buffer, as a store-and-forward device keeps what it has not delivered.
func (s *harborpulse) flushBackfill(ctx context.Context, rt *Runtime, model *harborModel, elapsed time.Duration) error {
	pending := model.takeBackfill(elapsed)
	if len(pending) == 0 {
		return nil
	}
	byToken := make(map[string]DeviceInstance, len(rt.Devices))
	for _, d := range rt.Devices {
		byToken[d.Token] = d
	}
	var errs []error
	for _, p := range pending {
		token, readings := p.token, p.readings
		d, ok := byToken[token]
		if !ok {
			continue
		}
		for i, r := range readings {
			err := emitMeasurementsAt(ctx, rt, d, r.at.Format(time.RFC3339Nano), wireMeasurements(r.values))
			if err != nil {
				rt.Stats.Failed.Add(1)
				model.requeue(token, readings[i:])
				errs = append(errs, fmt.Errorf("%s: %w", token, err))
				break
			}
			rt.Stats.Emitted.Add(1)
		}
	}
	return errors.Join(errs...)
}

// CommandHandler is the device side of every command the district is sent: each one is applied
// to the model before it is answered, so SUCCESSFUL means the telemetry will show it.
func (s *harborpulse) CommandHandler() cmdreceiver.Handler {
	return harborHandler{model: s.ensureModel()}
}

type harborHandler struct{ model *harborModel }

func (h harborHandler) Receive(device string, c cmdreceiver.Command, answer func(success bool)) {
	ok := h.model.command(device, c.Name, c.Payload)
	go answer(ok)
}

// harborParam reads one numeric argument, refusing a missing, non-numeric or out-of-range one.
func harborParam(payload json.RawMessage, name string, lo, hi float64) (float64, bool) {
	var args map[string]json.Number
	if err := json.Unmarshal(payload, &args); err != nil {
		return 0, false
	}
	v, err := args[name].Float64()
	if err != nil || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

// ---- What a verifier needs to know about the day ----------------------------------------------

// HarborpulseDevice is one device of the district, as a verifier addresses it.
type HarborpulseDevice struct {
	Token, DeviceType, Quay string
}

// HarborpulseFleet is every device of the district, in the manifest's order.
func HarborpulseFleet() []HarborpulseDevice {
	layout := harborLayout()
	out := make([]HarborpulseDevice, 0, len(layout))
	for _, d := range layout {
		out = append(out, HarborpulseDevice{Token: d.token, DeviceType: d.deviceType, Quay: d.quay})
	}
	return out
}

// The named devices the beats are planted on, and the levels a verifier holds them to.
const (
	HarborpulseAirLockPump = harborAirLockPump
	HarborpulseBasinPump   = harborBasinPump
	HarborpulseHotCharger  = harborHotCharger
	HarborpulseOutageQuay  = harborOutageQuay
	// HarborpulseFloatStartM is the pump's own float switch: a well that reaches it was started
	// by the float, not by the platform.
	HarborpulseFloatStartM = harborFloatStart
	// HarborpulseOutageFrom and HarborpulseOutageTo are the East Quay feeder outage, sim hours.
	HarborpulseOutageFrom = harborOutageFrom
	HarborpulseOutageTo   = harborOutageTo
	// HarborpulseAirLockFrom and HarborpulseAirLockTo are the air lock's window, sim hours.
	HarborpulseAirLockFrom = harborAirLockFrom
	HarborpulseAirLockTo   = harborAirLockTo
	// HarborpulseOverheatTo is when the Basin charger's long session ends, sim hours: a
	// connector-hot alarm that clears before it was cooled by the derate, not by the session ending.
	HarborpulseOverheatTo = harborOverheatTo
)

// HarborpulseQuayFloodingDefinition is the quay-flooding correlation's rule definition with its
// distinct-gauge count set to count. The manifest's rule is count HarborpulseQuayFloodingN; a
// verifier previews a count above the gauges a quay has as the control that must not fire.
func HarborpulseQuayFloodingDefinition(count int) string {
	return CorrelationSignalRule{
		Token: HarborpulseQuayFloodingRule, Name: "Harborpulse quay flooding", AnchorType: "area",
		Metric: HarborpulseWaterLevelKey, Op: OpGt, Threshold: HarborpulseQuayFloodingM,
		Count: count, Window: HarborpulseQuayFloodWindow, Enabled: true,
	}.DefinitionJSON()
}
