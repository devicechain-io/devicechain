// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/rs/zerolog/log"
)

// towerpulse is the building-management scenario: a five-floor office tower and a two-floor
// lab annex run by a facility-services tenant, 31 MQTT-native controllers in all. It is
// buildingpulse grown into the model the second showcase needs, and it is the headless
// backbone of that showcase: everything the platform side of the story needs, with none of
// the scene.
//
// 🔴 EVERY DEVICE HERE IS AN MQTT-NATIVE CONTROLLER, A CONNECTED RETROFIT. The platform has
// no BACnet, Modbus or OPC-UA support in-tree, so this scenario must never be described, in
// code, docs or a caption, as integrating with a building management system. It is a fleet
// of controllers that speak the platform's own protocol.
//
// The story it carries, one beat per rule:
//
//	R1 zone-hot    a zone whose five-minute average temperature is above 26 C WHILE ITS ROOM IS
//	               OCCUPIED raises a MAJOR alarm and sends `boost-cooling` to the same
//	               controller. The command is SUCCESSFUL only once the zone reaches its setpoint.
//	R2 co2-high    CO2 above 1000 ppm raises a MINOR alarm.
//	R3 filter-due  an air handler's filter pressure drop above 250 Pa raises a MINOR "service
//	               due" alarm and sends nothing: the human beat.
//	R4 chiller-silent  a chiller that sends nothing for two minutes raises a CRITICAL alarm.
//
// 🔴 A RULE CAN ONLY COMMAND THE DEVICE THAT PRODUCED THE EVENT. REACT's sendCommand targets
// the detection's own series, so "a hot zone sensor commands its air handler" is not
// expressible. Sensing and actuation therefore live in ONE device, the VAV zone controller,
// which is also how a real VAV box is built. Acting on a related device through the
// relationship graph is a separate platform feature and is deliberately not faked here.
//
// The device side is this process: the model in towerpulse_model.go streams telemetry over
// the HTTP ingress like any other scenario, and the in-process command far end answers
// commands only when their job is done (towerpulseCommands). The real thermal model, with
// its day timeline, is the Unity showcase's own; this one is the smallest thing that is
// plausible and seeded.
type towerpulse struct {
	seed int64
	load Load

	mu       sync.Mutex
	model    *towerpulseModel
	commands *towerpulseCommands
	started  time.Time
	now      func() time.Time
}

// NewTowerpulse returns the towerpulse Sim seeded from the handshake. Prefer NewSim.
func NewTowerpulse(seed int64, load Load) Sim {
	return &towerpulse{seed: seed, load: load, now: time.Now}
}

// Towerpulse entity tokens, fixed rather than derived from the handshake, for the reason every
// scenario's are: a token is read from several places and a literal repeated at each is a
// rename that succeeds partially.
const (
	TowerpulseCustomerTypeToken = "tp-property-owner"
	TowerpulseTowerOwnerToken   = "tp-meridian"
	TowerpulseAnnexOwnerToken   = "tp-halden"
	// TowerpulseAnnexOwnerEmail is the customer user limited to the annex.
	TowerpulseAnnexOwnerEmail = "owner@halden-labs.example"

	TowerpulseBuildingTypeToken = "tp-building"
	TowerpulseFloorTypeToken    = "tp-floor"
	TowerpulseTowerToken        = "tp-tower"
	TowerpulseAnnexToken        = "tp-annex"

	TowerpulseVAVProfileToken     = "tp-vav-profile"
	TowerpulseAHUProfileToken     = "tp-ahu-profile"
	TowerpulseChillerProfileToken = "tp-chiller-profile"
	TowerpulseMeterProfileToken   = "tp-meter-profile"

	TowerpulseVAVTypeToken     = "tp-vav-zone"
	TowerpulseAHUTypeToken     = "tp-ahu"
	TowerpulseChillerTypeToken = "tp-chiller"
	TowerpulseMeterTypeToken   = "tp-energy-meter"

	TowerpulseDashboardToken      = "tp-dashboard"
	TowerpulseAnnexDashboardToken = "tp-annex-dashboard"
)

// The metric vocabulary, exactly the showcase's: a VAV zone controller, an air handler, a
// chiller and a building meter.
const (
	TowerpulseTempKey      = "temp_c"
	TowerpulseSetpointKey  = "setpoint_c"
	TowerpulseCO2Key       = "co2_ppm"
	TowerpulseOccupancyKey = "occupancy"
	TowerpulseDamperKey    = "damper_pct"

	TowerpulseSupplyAirKey = "supply_air_c"
	TowerpulseFanKey       = "fan_pct"
	TowerpulseFilterDPKey  = "filter_dp_pa"
	// TowerpulseAHUModeKey is numeric, because a metric must be: 0 normal, 1 economy, 2 boost.
	TowerpulseAHUModeKey = "mode"

	TowerpulseChillerSupplyKey = "chw_supply_c"
	TowerpulsePowerKey         = "power_kw"
	TowerpulseCoolingKey       = "cooling_kw"
	TowerpulseRunningKey       = "running"
	// TowerpulseCOPKey is CALCULATED: the platform computes it from the two kilowatt metrics
	// on every reading, so a chiller never reports it.
	TowerpulseCOPKey = "cop"

	TowerpulseMeterKwKey  = "kw"
	TowerpulseMeterKwhKey = "kwh"
	// TowerpulseKwhPerM2Key is CALCULATED from the meter's reading and the floor area it
	// carries as a device constant.
	TowerpulseKwhPerM2Key = "kwh_per_m2"
	// TowerpulseFloorAreaConstant is the device constant a meter carries in its metadata.
	TowerpulseFloorAreaConstant = "floor_area_m2"
)

// The commands, typed on the profile. Token and key are different strings (see sitepulse).
const (
	TowerpulseBoostCommandDef    = "tp-cmd-boost-cooling"
	TowerpulseBoostCommandKey    = "boost-cooling"
	TowerpulseSetpointCommandDef = "tp-cmd-set-setpoint"
	TowerpulseSetpointCommandKey = "set-setpoint"
	TowerpulseModeCommandDef     = "tp-cmd-set-mode"
	TowerpulseModeCommandKey     = "set-mode"
	TowerpulseRestartCommandDef  = "tp-cmd-restart"
	TowerpulseRestartCommandKey  = "restart"

	// TowerpulseBoostMinutes is how long the rule lets a boost run before it gives up.
	TowerpulseBoostMinutes = 15
	// towerpulseBoostExpiry bounds how long the rule's command may wait for a controller that
	// is offline: a boost ordered for a hot room is no use an hour later.
	towerpulseBoostExpiry = "30m"
)

// The rules and the lines they cross.
const (
	TowerpulseZoneHotRuleToken = "tp-rule-zone-hot"
	TowerpulseZoneHotAlarmKey  = "zone-hot"
	// TowerpulseZoneHotCelsius is the average the zone must exceed, and TowerpulseZoneHotWindow
	// the trailing span it is averaged over.
	TowerpulseZoneHotCelsius = 26.0
	TowerpulseZoneHotWindow  = 5 * time.Minute

	TowerpulseCO2RuleToken = "tp-rule-co2-high"
	TowerpulseCO2AlarmKey  = "co2-high"
	TowerpulseCO2Ppm       = 1000.0

	TowerpulseFilterRuleToken = "tp-rule-filter-due"
	TowerpulseFilterAlarmKey  = "filter-service-due"
	TowerpulseFilterPa        = 250.0

	TowerpulseChillerRuleToken = "tp-rule-chiller-silent"
	TowerpulseChillerAlarmKey  = "chiller-silent"
	TowerpulseChillerSilence   = 2 * time.Minute

	// The alarm tiers: both spellings of each live in wirevocabulary.go.
	TowerpulseZoneHotSeverity = SeverityMajor
	TowerpulseCO2Severity     = SeverityMinor
	TowerpulseFilterSeverity  = SeverityMinor
	TowerpulseChillerSeverity = SeverityCritical
)

// towerpulseUnit is one device of the layout.
type towerpulseUnit struct {
	token      string
	externalId string
	building   towerpulseBuilding
	areas      []string
	customer   string
	basePeople int
	constants  map[string]float64
}

// towerpulseLayout is the campus as data. The manifest and the thermal model are BOTH built
// from it, so they cannot disagree about which device is which.
type towerpulseLayout struct {
	zones, ahus, chillers, meters []towerpulseUnit
}

var towerpulseTowerDirections = []string{"north", "east", "south", "west"}
var towerpulseAnnexDirections = []string{"north", "south"}

// towerpulseFloorToken is the area a floor of a building lives in.
func towerpulseFloorToken(b towerpulseBuilding, floor int) string {
	return fmt.Sprintf("tp-%s-f%d", b, floor)
}

func towerpulsePlantToken(b towerpulseBuilding) string { return fmt.Sprintf("tp-%s-plant", b) }

func towerpulseBuildingToken(b towerpulseBuilding) string { return "tp-" + string(b) }

func towerpulseOwner(b towerpulseBuilding) string {
	if b == towerpulseAnnex {
		return TowerpulseAnnexOwnerToken
	}
	return TowerpulseTowerOwnerToken
}

// towerpulseFloorAreas are the floor areas that hold zones, and the floors' total area
// feeds the meter constants below.
var towerpulseFloorArea = map[towerpulseBuilding]float64{towerpulseTower: 6200, towerpulseAnnex: 1850}

func buildTowerpulseLayout() towerpulseLayout {
	var l towerpulseLayout
	add := func(b towerpulseBuilding, floors int, directions []string) {
		for f := 1; f <= floors; f++ {
			for _, dir := range directions {
				token := fmt.Sprintf("tp-vav-%s-f%d-%s", b, f, dir)
				ext := fmt.Sprintf("TP-VAV-%s-F%d-%s", strings.ToUpper(string(b[:1])), f, strings.ToUpper(dir[:1]))
				u := towerpulseUnit{
					token: token, externalId: ext, building: b,
					areas:    []string{towerpulseFloorToken(b, f), towerpulseBuildingToken(b)},
					customer: towerpulseOwner(b),
				}
				// Most rooms have a few people in them; the sunny empty zone has nobody, which is the
				// whole point of it.
				u.basePeople = 1 + int(5*stableUnit(stableSeed(ext), 9))
				if token == TowerpulseSunnyEmptyZone {
					u.basePeople = 0
				}
				l.zones = append(l.zones, u)
			}
		}
	}
	add(towerpulseTower, 5, towerpulseTowerDirections)
	add(towerpulseAnnex, 2, towerpulseAnnexDirections)

	plant := func(b towerpulseBuilding) []string {
		return []string{towerpulsePlantToken(b), towerpulseBuildingToken(b)}
	}
	for _, spec := range []struct {
		b towerpulseBuilding
		n int
	}{{towerpulseTower, 2}, {towerpulseAnnex, 1}} {
		for i := 1; i <= spec.n; i++ {
			l.ahus = append(l.ahus, towerpulseUnit{
				token: fmt.Sprintf("tp-ahu-%s-%d", spec.b, i), externalId: fmt.Sprintf("TP-AHU-%s-%d", strings.ToUpper(string(spec.b[:1])), i),
				building: spec.b, areas: plant(spec.b), customer: towerpulseOwner(spec.b),
			})
		}
	}
	for _, b := range []towerpulseBuilding{towerpulseTower, towerpulseAnnex} {
		l.chillers = append(l.chillers, towerpulseUnit{
			token: "tp-chiller-" + string(b), externalId: "TP-CH-" + strings.ToUpper(string(b[:1])),
			building: b, areas: plant(b), customer: towerpulseOwner(b),
		})
		l.meters = append(l.meters, towerpulseUnit{
			token: "tp-meter-" + string(b), externalId: "TP-EM-" + strings.ToUpper(string(b[:1])),
			building: b, areas: plant(b), customer: towerpulseOwner(b),
			constants: map[string]float64{TowerpulseFloorAreaConstant: towerpulseFloorArea[b]},
		})
	}
	return l
}

func (l towerpulseLayout) instances(units []towerpulseUnit) []InstanceSpec {
	out := make([]InstanceSpec, len(units))
	for i, u := range units {
		out[i] = InstanceSpec{Token: u.token, ExternalId: u.externalId, Areas: u.areas, Customer: u.customer, Constants: u.constants}
	}
	return out
}

// ---- Parameter schemas ---------------------------------------------------------------------

// The schemas are the wire format verbatim, like every command schema here. The bounds are the
// stepper's range: the console renders its control from them.
const (
	towerpulseBoostParameterSchema = `[{"name":"minutes","kind":"SCALAR","dataType":"INT","unit":"min","required":true,"minValue":1,"maxValue":120}]`
	// TowerpulseSetpointParameter is the setpoint command's one parameter, named once because
	// the board's stepper binds it.
	TowerpulseSetpointParameter       = "celsius"
	towerpulseSetpointParameterSchema = `[{"name":"celsius","kind":"SCALAR","dataType":"DOUBLE","unit":"C","required":true,"minValue":18,"maxValue":28}]`
	towerpulseModeParameterSchema     = `[{"name":"mode","kind":"SCALAR","dataType":"STRING","required":true,"enum":["normal","economy","boost"]}]`
)

// towerpulseCalculatedMetricsPerProfile is the most calculated metrics any one profile of the
// scenario declares, i.e. the tier ceiling it needs (the ceiling is per profile).
const towerpulseCalculatedMetricsPerProfile = 1

// TowerpulseCOPFormula and TowerpulseKwhPerM2Formula are the calculated metrics' formulas.
const (
	TowerpulseCOPFormula      = "cooling_kw / power_kw"
	TowerpulseKwhPerM2Formula = "kwh / device." + TowerpulseFloorAreaConstant
)

func (s *towerpulse) Manifest() SimManifest {
	layout := buildTowerpulseLayout()

	var areas []AreaSpec
	for _, b := range []towerpulseBuilding{towerpulseTower, towerpulseAnnex} {
		name := map[towerpulseBuilding]string{towerpulseTower: "Tower", towerpulseAnnex: "Annex"}[b]
		areas = append(areas, AreaSpec{Token: towerpulseBuildingToken(b), Name: name, AreaTypeToken: TowerpulseBuildingTypeToken})
	}
	for _, spec := range []struct {
		b      towerpulseBuilding
		floors int
	}{{towerpulseTower, 5}, {towerpulseAnnex, 2}} {
		name := map[towerpulseBuilding]string{towerpulseTower: "Tower", towerpulseAnnex: "Annex"}[spec.b]
		for f := 1; f <= spec.floors; f++ {
			areas = append(areas, AreaSpec{
				Token: towerpulseFloorToken(spec.b, f), Name: fmt.Sprintf("%s floor %d", name, f), AreaTypeToken: TowerpulseFloorTypeToken,
			})
		}
		areas = append(areas, AreaSpec{Token: towerpulsePlantToken(spec.b), Name: name + " plant", AreaTypeToken: TowerpulseFloorTypeToken})
	}

	manifest := SimManifest{
		Name: "towerpulse",
		Seed: s.seed,
		// A composed fixture: the board and the script name devices chosen for their behaviour
		// (the meeting room, the empty sunny room, the clogging filter, the annex chiller), which
		// a device-count override cannot reproduce.
		FixedTopology: true,
		// The in-process far end answers commands, but not with the cmdreceiver's answer-at-once:
		// see CommandHandler.
		CommandFarEnd: FarEndInternal,
		CustomerTypes: []CustomerTypeSpec{{Token: TowerpulseCustomerTypeToken, Name: "Property Owner"}},
		Customers: []CustomerSpec{
			{Token: TowerpulseTowerOwnerToken, Name: "Meridian Properties", CustomerTypeToken: TowerpulseCustomerTypeToken},
			{Token: TowerpulseAnnexOwnerToken, Name: "Halden Labs", CustomerTypeToken: TowerpulseCustomerTypeToken},
		},
		AreaTypes: []AreaTypeSpec{
			{Token: TowerpulseBuildingTypeToken, Name: "Building"},
			{Token: TowerpulseFloorTypeToken, Name: "Floor"},
		},
		Areas: areas,
		Profiles: []ProfileSpec{
			towerpulseVAVProfile(),
			towerpulseAHUProfile(),
			towerpulseChillerProfile(),
			towerpulseMeterProfile(),
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: TowerpulseVAVTypeToken, Name: "VAV Zone Controller", ProfileToken: TowerpulseVAVProfileToken},
			{Token: TowerpulseAHUTypeToken, Name: "Air Handling Unit", ProfileToken: TowerpulseAHUProfileToken},
			{Token: TowerpulseChillerTypeToken, Name: "Chiller", ProfileToken: TowerpulseChillerProfileToken},
			{Token: TowerpulseMeterTypeToken, Name: "Energy Meter", ProfileToken: TowerpulseMeterProfileToken},
		},
		Populations: []PopulationSpec{
			{OfType: TowerpulseVAVTypeToken, Count: len(layout.zones), FixedCount: true, Instances: layout.instances(layout.zones)},
			{OfType: TowerpulseAHUTypeToken, Count: len(layout.ahus), FixedCount: true, Instances: layout.instances(layout.ahus)},
			{OfType: TowerpulseChillerTypeToken, Count: len(layout.chillers), FixedCount: true, Instances: layout.instances(layout.chillers)},
			{OfType: TowerpulseMeterTypeToken, Count: len(layout.meters), FixedCount: true, Instances: layout.instances(layout.meters)},
		},
		// The annex owner: a member limited to the annex's customer, so the console and the
		// annex board show them the annex and nothing else.
		CustomerUsers: []CustomerUserSpec{{Email: TowerpulseAnnexOwnerEmail, CustomerToken: TowerpulseAnnexOwnerToken}},
	}

	devices := manifest.Expand(manifest.Seed)
	operator, err := buildTowerpulseDashboard(devices, towerpulseTowerDefault)
	if err != nil {
		panic(fmt.Sprintf("towerpulse: build dashboard definition: %v", err))
	}
	annex, err := buildTowerpulseDashboard(devices, towerpulseAnnexDefault)
	if err != nil {
		panic(fmt.Sprintf("towerpulse: build annex dashboard definition: %v", err))
	}
	manifest.Dashboards = []DashboardSpec{
		{
			Token:       TowerpulseDashboardToken,
			Name:        "Towerpulse",
			Description: "The campus at a glance: alarms by severity, devices online, building power, chillers, a floor's zones compared, the alarm table with a tab per kind of equipment, a setpoint control and a power history.",
			Definition:  operator,
		},
		{
			Token:       TowerpulseAnnexDashboardToken,
			Name:        "Towerpulse annex",
			Description: "The same board opened on the annex, for the annex owner.",
			Definition:  annex,
		},
	}
	return manifest
}

// towerpulseVAVProfile is the zone controller: sensing and actuation in one device.
func towerpulseVAVProfile() ProfileSpec {
	return ProfileSpec{
		Token:    TowerpulseVAVProfileToken,
		Name:     "Towerpulse VAV Zone Profile",
		Category: "hvac",
		Metrics: []MetricSpec{
			{Key: TowerpulseTempKey, Name: "Zone temperature", DataType: "DOUBLE", Unit: "C"},
			{Key: TowerpulseSetpointKey, Name: "Setpoint", DataType: "DOUBLE", Unit: "C"},
			{Key: TowerpulseCO2Key, Name: "CO2", DataType: "DOUBLE", Unit: "ppm"},
			{Key: TowerpulseOccupancyKey, Name: "Occupancy", DataType: "INT", Unit: "people"},
			{Key: TowerpulseDamperKey, Name: "Damper", DataType: "DOUBLE", Unit: "%"},
		},
		Commands: []CommandSpec{
			{Token: TowerpulseBoostCommandDef, CommandKey: TowerpulseBoostCommandKey, Name: "Boost cooling", ParameterSchema: towerpulseBoostParameterSchema},
			{Token: TowerpulseSetpointCommandDef, CommandKey: TowerpulseSetpointCommandKey, Name: "Set setpoint", ParameterSchema: towerpulseSetpointParameterSchema},
		},
		DetectionRules: []DetectionRuleSpec{
			towerpulseZoneHotRule(true),
			ThresholdAlarmRule{
				Token: TowerpulseCO2RuleToken, Name: "Towerpulse CO2 high", Metric: TowerpulseCO2Key,
				Op: OpGt, Threshold: TowerpulseCO2Ppm, Severity: TowerpulseCO2Severity,
				AlarmKey: TowerpulseCO2AlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

// towerpulseZoneHotRule is R1. enabled is a parameter only so a test can build its negative
// control from the SAME definition rather than from a hand-edited copy.
func towerpulseZoneHotRule(enabled bool) DetectionRuleSpec {
	return AggregateAlarmRule{
		Token: TowerpulseZoneHotRuleToken, Name: "Towerpulse zone hot while occupied",
		Metric: TowerpulseTempKey, Op: OpGt, Threshold: TowerpulseZoneHotCelsius, Window: TowerpulseZoneHotWindow,
		GateMetric: TowerpulseOccupancyKey, GateOp: OpGt, GateThreshold: 0,
		Severity: TowerpulseZoneHotSeverity, AlarmKey: TowerpulseZoneHotAlarmKey,
		CommandKey:          TowerpulseBoostCommandKey,
		CommandPayload:      map[string]any{"minutes": TowerpulseBoostMinutes},
		CommandExpiresAfter: towerpulseBoostExpiry,
		Enabled:             enabled,
	}.Spec()
}

func towerpulseAHUProfile() ProfileSpec {
	return ProfileSpec{
		Token:    TowerpulseAHUProfileToken,
		Name:     "Towerpulse Air Handler Profile",
		Category: "hvac",
		Metrics: []MetricSpec{
			{Key: TowerpulseSupplyAirKey, Name: "Supply air temperature", DataType: "DOUBLE", Unit: "C"},
			{Key: TowerpulseFanKey, Name: "Fan speed", DataType: "DOUBLE", Unit: "%"},
			{Key: TowerpulseFilterDPKey, Name: "Filter pressure drop", DataType: "DOUBLE", Unit: "Pa"},
			{Key: TowerpulseAHUModeKey, Name: "Mode (0 normal, 1 economy, 2 boost)", DataType: "INT"},
		},
		Commands: []CommandSpec{
			{Token: TowerpulseModeCommandDef, CommandKey: TowerpulseModeCommandKey, Name: "Set mode", ParameterSchema: towerpulseModeParameterSchema},
		},
		DetectionRules: []DetectionRuleSpec{
			// No command: a clogged filter is a job for a person, and that is the beat.
			ThresholdAlarmRule{
				Token: TowerpulseFilterRuleToken, Name: "Towerpulse filter service due", Metric: TowerpulseFilterDPKey,
				Op: OpGt, Threshold: TowerpulseFilterPa, Severity: TowerpulseFilterSeverity,
				AlarmKey: TowerpulseFilterAlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

func towerpulseChillerProfile() ProfileSpec {
	return ProfileSpec{
		Token:    TowerpulseChillerProfileToken,
		Name:     "Towerpulse Chiller Profile",
		Category: "hvac",
		Metrics: []MetricSpec{
			{Key: TowerpulseChillerSupplyKey, Name: "Chilled water supply", DataType: "DOUBLE", Unit: "C"},
			{Key: TowerpulsePowerKey, Name: "Electrical power", DataType: "DOUBLE", Unit: "kW"},
			{Key: TowerpulseCoolingKey, Name: "Cooling output", DataType: "DOUBLE", Unit: "kW"},
			{Key: TowerpulseRunningKey, Name: "Running", DataType: "BOOLEAN"},
			{Key: TowerpulseCOPKey, Name: "Coefficient of performance", DataType: "DOUBLE", Expression: TowerpulseCOPFormula},
		},
		Commands: []CommandSpec{
			{Token: TowerpulseRestartCommandDef, CommandKey: TowerpulseRestartCommandKey, Name: "Restart"},
		},
		DetectionRules: []DetectionRuleSpec{
			AbsenceAlarmRule{
				Token: TowerpulseChillerRuleToken, Name: "Towerpulse chiller silent", Metric: TowerpulsePowerKey,
				Timeout: TowerpulseChillerSilence, Severity: TowerpulseChillerSeverity,
				AlarmKey: TowerpulseChillerAlarmKey, Enabled: true,
			}.Spec(),
		},
	}
}

func towerpulseMeterProfile() ProfileSpec {
	return ProfileSpec{
		Token:    TowerpulseMeterProfileToken,
		Name:     "Towerpulse Energy Meter Profile",
		Category: "energy",
		Metrics: []MetricSpec{
			{Key: TowerpulseMeterKwKey, Name: "Power", DataType: "DOUBLE", Unit: "kW"},
			{Key: TowerpulseMeterKwhKey, Name: "Energy", DataType: "DOUBLE", Unit: "kWh"},
			{Key: TowerpulseKwhPerM2Key, Name: "Energy per floor area", DataType: "DOUBLE", Unit: "kWh/m2", Expression: TowerpulseKwhPerM2Formula},
		},
	}
}

func (s *towerpulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// ---- The device plane ------------------------------------------------------------------------

// ensureModel builds the thermal model on first use. Lazily, because the command far end
// needs the handler during Bootstrap, before the first Tick.
func (s *towerpulse) ensureModel() (*towerpulseModel, *towerpulseCommands) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model == nil {
		s.model = newTowerpulseModel(buildTowerpulseLayout())
		s.commands = newTowerpulseCommands(s.model)
	}
	return s.model, s.commands
}

// Tick advances the campus to the present and publishes every device's reading. A silent
// device (a chiller that has dropped out) publishes nothing: the whole point of its dropout is
// that the platform hears nothing, which is what the absence rule fires on.
func (s *towerpulse) Tick(ctx context.Context, rt *Runtime) error {
	model, commands := s.ensureModel()
	s.mu.Lock()
	if s.started.IsZero() {
		s.started = s.now()
	}
	elapsed := s.now().Sub(s.started).Seconds()
	s.mu.Unlock()

	model.advanceTo(elapsed)
	commands.settle()

	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(_ int, d DeviceInstance) map[string]float64 { return model.reading(d.Token) })
	if err != nil {
		log.Error().Err(err).Msg("emit towerpulse measurements failed")
	}
	return err
}

// DropChiller silences a chiller now, out of turn with the script: how a presenter triggers the
// dropout the showcase's second beat is about. A restart command brings it back.
func (s *towerpulse) DropChiller(token string) error {
	model, _ := s.ensureModel()
	return model.DropChiller(token)
}

// CommandHandler is the device side of every command the campus is sent. It replaces the
// cmdreceiver's answer-at-once behaviour, which would report a boost SUCCESSFUL the instant
// it arrived, for work that takes minutes to do.
func (s *towerpulse) CommandHandler() cmdreceiver.Handler {
	_, commands := s.ensureModel()
	return towerpulseHandler{commands: commands}
}

type towerpulseHandler struct{ commands *towerpulseCommands }

// Receive hands a frame to the command engine.
func (h towerpulseHandler) Receive(device string, c cmdreceiver.Command, answer func(success bool)) {
	h.commands.receive(device, c.Token, c.Name, c.Payload, answer)
}

// commandHandlerSource is what a Sim implements to answer its own commands.
type commandHandlerSource interface {
	CommandHandler() cmdreceiver.Handler
}
