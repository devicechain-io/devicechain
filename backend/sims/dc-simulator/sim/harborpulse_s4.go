// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	// The SOLAR campaigns name the town's zone and their holiday is a date in it, so the zone
	// database travels with the binary rather than depending on the host's.
	_ "time/tzdata"

	"github.com/rs/zerolog/log"
)

// harborpulse's fleet operations (S4): who is in which group, where the lamps stand, what the
// district is made of and what feeds what, and the jobs the Works runs on the fleet.
//
//	groups             four dynamic groups over SHARED attributes (role, quay, critical): every
//	                   street light, East Quay's lights, the Basin's pump, the critical pumps
//	wet-well-watch     a rule SCOPED to the critical pumps: an early warning at 2.0 m that the
//	                   North Quay pump's well crosses too, and that does not fire for it
//	dusk / dawn        two SOLAR campaigns over every street light, planned per lamp from its
//	                   surveyed position, so the planned instants run east to west along the
//	                   promenade; tomorrow (in Europe/London) is a public holiday on the dusk one
//	night dim          a CALENDAR campaign each sim day at 21:30 that dims East Quay's lights to 40 %
//	basin flush        a CALENDAR campaign each sim day at 14:00 that sends the Basin pump the
//	                   DISRUPTIVE flush-well; the pump controllers' maintenance window holds it
//	                   until the crew's slot opens at 15:00
//	hierarchy          the waterfront -> its quays -> buildings, pump stations and feeders, with
//	                   the customer meters assigned to their buildings and the pumps to their
//	                   stations
//	feeds              each quay's feeder feeds that quay's lights and chargers: the graph that
//	                   names the East Quay outage's blast radius before it happens
//
// 🔴 REAL TIME IS NOT COMPRESSED. A campaign trigger and a maintenance window are wall-clock
// schedules. The sim day's CALENDAR jobs and the window's slot are therefore written AS THE DAY
// RUNS, at the first whole wall minute after the sim time they belong to (the platform's
// resolution is a minute, and a sim hour is a wall minute at the default day). Real dusk and dawn
// cannot be brought inside a 24-minute day at all, so the SOLAR campaigns are judged on what the
// platform PLANNED (each lamp's instant, from its position), not on waiting for the sun.

// Group, asset and job tokens.
const (
	HarborpulseAllLightsGroup     = "hp-all-lights"
	HarborpulseEastLightsGroup    = "hp-east-quay-lights"
	HarborpulseBasinPumpsGroup    = "hp-basin-pumps"
	HarborpulseCriticalPumpsGroup = "hp-critical-pumps"

	HarborpulseRoleAttr     = "role"
	HarborpulseQuayAttr     = "quay"
	HarborpulseCriticalAttr = "critical"
	harborRoleLight         = "street-light"
	harborRolePump          = "pump"

	// The wet-well watch: an early warning scoped to the critical pumps.
	HarborpulseWellWatchRule     = "hp-wet-well-watch"
	HarborpulseWellWatchAlarmKey = "wet-well-watch"
	HarborpulseWellWatchM        = 2.0

	// The pump controllers' maintenance window and the disruptive command it holds.
	HarborpulsePumpWindow   = "hp-pump-service-window"
	HarborpulseFlushDef     = "hp-cmd-flush-well"
	HarborpulseFlushKey     = "flush-well"
	harborWindowZone        = "Etc/UTC"
	harborWindowMinutes     = 2
	harborFlushMargin       = 30 * time.Minute
	harborNightDimLevel     = 40
	harborDuskLevel         = 100
	harborDawnLevel         = 50
	HarborpulseNightDimPct  = harborNightDimLevel
	HarborpulseFlushAtHour  = 14.0
	HarborpulseWindowAtHour = 15.0
	HarborpulseNightDimHour = 21.5

	// The SOLAR campaigns, in the town's zone.
	HarborpulseDuskCampaign = "hp-lights-dusk"
	HarborpulseDawnCampaign = "hp-lights-dawn"
	HarborpulseTownZone     = "Europe/London"

	// The prefixes of the campaigns the sim creates as each day runs.
	HarborpulseNightDimPrefix = "hp-night-dim-"
	HarborpulseFlushPrefix    = "hp-basin-flush-"

	// The district, as assets.
	HarborpulseDistrictAsset = "hp-waterfront"
	HarborpulseLoftsBuilding = "hp-bldg-quayside-lofts"
	HarborpulseFerryBuilding = "hp-bldg-ferry-terminal"
	HarborpulseFeedsType     = "hp-feeds"

	harborDistrictType = "hp-district"
	harborSiteType     = "hp-quay-site"
	harborBuildingType = "hp-building"
	harborStationType  = "hp-pump-station"
	harborFeederType   = "hp-feeder"

	// The promenade the street lights stand along: one latitude, longitudes spaced by quay.
	harborPromenadeLat = 49.8020
	// harborLampSpacing is about 430 m: the platform plans a solar instant to the whole second,
	// so lamps closer than a second of sun (about 0.004 degrees here) would tie.
	harborLampSpacing = 0.006
)

// The S4 planted defects. Each breaks one fleet operation.
const (
	// HarborpulseDefectEastLampMislabelled tags one East Quay lamp as North Quay's.
	HarborpulseDefectEastLampMislabelled = "east-lamp-mislabelled"
	// HarborpulseDefectUnscopedWatch publishes the wet-well watch without its group scope.
	HarborpulseDefectUnscopedWatch = "unscoped-watch"
	// HarborpulseDefectNightDimAllLights aims the night dim at every street light.
	HarborpulseDefectNightDimAllLights = "night-dim-all-lights"
	// HarborpulseDefectLampUnsited leaves one lamp without an installed position.
	HarborpulseDefectLampUnsited = "lamp-unsited"
	// HarborpulseDefectHolidayDropped publishes the dusk campaign without its holiday.
	HarborpulseDefectHolidayDropped = "holiday-dropped"
	// HarborpulseDefectFlushUngated provisions the pump controllers with no maintenance window.
	HarborpulseDefectFlushUngated = "flush-ungated"
	// HarborpulseDefectLoftsUnderBasin files the Quayside Lofts building under the Basin.
	HarborpulseDefectLoftsUnderBasin = "lofts-under-basin"
	// HarborpulseDefectFeederMiswired feeds one East Quay lamp from the North Quay feeder.
	HarborpulseDefectFeederMiswired = "feeder-miswired"
)

// The devices the S4 defects move.
const (
	harborMislabelledLamp = "hp-light-east-2"
	harborUnsitedLamp     = "hp-light-basin-4"
	harborMiswiredLamp    = "hp-light-east-4"
)

// harborQuayBaseLon is where each quay's first lamp stands: East Quay furthest east, the Basin
// furthest west, so a sunset or a sunrise reaches East Quay first.
var harborQuayBaseLon = map[string]float64{
	HarborpulseEastQuay:  -12.1700,
	HarborpulseNorthQuay: -12.2000,
	HarborpulseBasin:     -12.2300,
}

// HarborpulseQuaySite is the asset that stands for a quay (an area) in the asset hierarchy.
func HarborpulseQuaySite(quay string) string { return "hp-site-" + quayName(quay) }

// HarborpulseStation is a quay's pump station asset.
func HarborpulseStation(quay string) string { return "hp-station-" + quayName(quay) }

// HarborpulseFeeder is a quay's electrical feeder asset.
func HarborpulseFeeder(quay string) string { return "hp-feeder-" + quayName(quay) }

func quayName(quay string) string {
	for _, q := range harborQuays {
		if q.token == quay {
			return q.name
		}
	}
	return strings.TrimPrefix(quay, "hp-")
}

// harborCritical is whether a quay's pump is critical: East Quay sits lowest, and the Basin
// pump keeps the ferry terminal dry.
func harborCritical(quay string) bool { return quay == HarborpulseEastQuay || quay == HarborpulseBasin }

// harborFleetOps is the S4 part of a device's InstanceSpec.
func (s *harborpulse) harborFleetOps(d harborDevice, in *InstanceSpec) {
	switch d.kind {
	case harborCharger:
		in.Attributes = []AttributeSpec{{Key: HarborpulseQuayAttr, ValueType: "STRING", Value: quayName(d.quay)}}
		if !(s.defects.otaGroupMissingCharger && d.token == HarborpulseMissingCharger) {
			in.Attributes = append(in.Attributes, AttributeSpec{Key: HarborpulseRoleAttr, ValueType: "STRING", Value: harborRoleCharger})
		}
	case harborLight:
		quay := quayName(d.quay)
		if s.defects.eastLampMislabelled && d.token == harborMislabelledLamp {
			quay = quayName(HarborpulseNorthQuay)
		}
		in.Attributes = []AttributeSpec{
			{Key: HarborpulseRoleAttr, ValueType: "STRING", Value: harborRoleLight},
			{Key: HarborpulseQuayAttr, ValueType: "STRING", Value: quay},
		}
		if !(s.defects.lampUnsited && d.token == harborUnsitedLamp) {
			p := harborLampPosition(d)
			in.Position = &p
		}
	case harborPump:
		in.Attributes = []AttributeSpec{
			{Key: HarborpulseRoleAttr, ValueType: "STRING", Value: harborRolePump},
			{Key: HarborpulseQuayAttr, ValueType: "STRING", Value: quayName(d.quay)},
			{Key: HarborpulseCriticalAttr, ValueType: "BOOLEAN", Value: fmt.Sprint(harborCritical(d.quay))},
		}
		in.Assets = []string{HarborpulseStation(d.quay)}
	case harborMeter:
		building := HarborpulseLoftsBuilding
		if d.token == harborFerryMeter {
			building = HarborpulseFerryBuilding
		}
		in.Assets = []string{building}
	}
}

// harborLampPosition is where a street light stands on the promenade.
func harborLampPosition(d harborDevice) PositionSpec {
	return PositionSpec{Latitude: harborPromenadeLat, Longitude: harborQuayBaseLon[d.quay] + harborLampSpacing*float64(d.index-1)}
}

// HarborpulseLampPosition is the surveyed position of a street light, or false for any other
// device.
func HarborpulseLampPosition(token string) (PositionSpec, bool) {
	for _, d := range harborLayout() {
		if d.token == token && d.kind == harborLight {
			return harborLampPosition(d), true
		}
	}
	return PositionSpec{}, false
}

func harborpulseGroups() []EntityGroupSpec {
	attr := func(k string) string { return `attr["` + k + `"]` }
	light := attr(HarborpulseRoleAttr) + ` == "` + harborRoleLight + `"`
	pump := attr(HarborpulseRoleAttr) + ` == "` + harborRolePump + `"`
	return []EntityGroupSpec{
		{Token: HarborpulseAllChargersGroup, Name: "All EV chargers", Description: "Every charger on the waterfront.", Selector: attr(HarborpulseRoleAttr) + ` == "` + harborRoleCharger + `"`},
		{Token: HarborpulseAllLightsGroup, Name: "All street lights", Description: "Every street light on the waterfront.",
			Selector: light},
		{Token: HarborpulseEastLightsGroup, Name: "East Quay street lights", Description: "The street lights on East Quay.",
			Selector: light + ` && ` + attr(HarborpulseQuayAttr) + ` == "east"`},
		{Token: HarborpulseBasinPumpsGroup, Name: "Basin pumps", Description: "The Basin pump station.",
			Selector: pump + ` && ` + attr(HarborpulseQuayAttr) + ` == "basin"`},
		{Token: HarborpulseCriticalPumpsGroup, Name: "Critical pumps", Description: "Pumps whose failure floods property first.",
			Selector: pump + ` && ` + attr(HarborpulseCriticalAttr) + ` == true`},
	}
}

// HarborpulseGroupMembers is who each group must hold, from the layout: the expectation a
// verifier holds the platform's resolution to.
func HarborpulseGroupMembers() map[string][]string {
	out := map[string][]string{}
	for _, d := range harborLayout() {
		switch d.kind {
		case harborLight:
			out[HarborpulseAllLightsGroup] = append(out[HarborpulseAllLightsGroup], d.token)
			if d.quay == HarborpulseEastQuay {
				out[HarborpulseEastLightsGroup] = append(out[HarborpulseEastLightsGroup], d.token)
			}
		case harborPump:
			if d.quay == HarborpulseBasin {
				out[HarborpulseBasinPumpsGroup] = append(out[HarborpulseBasinPumpsGroup], d.token)
			}
			if harborCritical(d.quay) {
				out[HarborpulseCriticalPumpsGroup] = append(out[HarborpulseCriticalPumpsGroup], d.token)
			}
		}
	}
	return out
}

// harborpulseAssets is the district's asset hierarchy.
func (s *harborpulse) harborpulseAssets() ([]AssetTypeSpec, []AssetSpec) {
	types := []AssetTypeSpec{
		{Token: harborDistrictType, Name: "District"},
		{Token: harborSiteType, Name: "Quay"},
		{Token: harborBuildingType, Name: "Building"},
		{Token: harborStationType, Name: "Pump station"},
		{Token: harborFeederType, Name: "Electrical feeder"},
	}
	assets := []AssetSpec{{Token: HarborpulseDistrictAsset, Name: "Port Alder waterfront", AssetTypeToken: harborDistrictType}}
	for _, q := range harborQuays {
		title := map[string]string{"north": "North Quay", "east": "East Quay", "basin": "The Basin"}[q.name]
		assets = append(assets,
			AssetSpec{Token: HarborpulseQuaySite(q.token), Name: title, AssetTypeToken: harborSiteType, Parent: HarborpulseDistrictAsset},
			AssetSpec{Token: HarborpulseStation(q.token), Name: title + " pump station", AssetTypeToken: harborStationType, Parent: HarborpulseQuaySite(q.token)},
			AssetSpec{Token: HarborpulseFeeder(q.token), Name: title + " feeder", AssetTypeToken: harborFeederType, Parent: HarborpulseQuaySite(q.token)},
		)
	}
	loftsQuay := HarborpulseNorthQuay
	if s.defects.loftsUnderBasin {
		loftsQuay = HarborpulseBasin
	}
	assets = append(assets,
		AssetSpec{Token: HarborpulseLoftsBuilding, Name: "Quayside Lofts", AssetTypeToken: harborBuildingType, Parent: HarborpulseQuaySite(loftsQuay)},
		AssetSpec{Token: HarborpulseFerryBuilding, Name: "Ferry Terminal", AssetTypeToken: harborBuildingType, Parent: HarborpulseQuaySite(HarborpulseBasin)},
	)
	return types, assets
}

// HarborpulseBuildingOf is the building a customer meter is assigned to, and the quay it stands on.
func HarborpulseBuildingOf(meter string) (building, quay string, ok bool) {
	switch meter {
	case harborLoftsMeter:
		return HarborpulseLoftsBuilding, HarborpulseNorthQuay, true
	case harborFerryMeter:
		return HarborpulseFerryBuilding, HarborpulseBasin, true
	}
	return "", "", false
}

// HarborpulseMeters are the two customer meters.
func HarborpulseMeters() []string { return []string{harborLoftsMeter, harborFerryMeter} }

// harborpulseFeeds is each feeder's edges to the lights and chargers it powers.
func (s *harborpulse) harborpulseFeeds() []RelationshipSpec {
	var out []RelationshipSpec
	for _, d := range harborLayout() {
		if d.kind != harborLight && d.kind != harborCharger {
			continue
		}
		feeder := HarborpulseFeeder(d.quay)
		if s.defects.feederMiswired && d.token == harborMiswiredLamp {
			feeder = HarborpulseFeeder(HarborpulseNorthQuay)
		}
		out = append(out, RelationshipSpec{Token: "hp-feeds-" + d.token, Type: HarborpulseFeedsType,
			SourceType: entityTypeAsset, Source: feeder, TargetType: entityTypeDevice, Target: d.token})
	}
	return out
}

// HarborpulseFedBy is the devices a quay's feeder powers, from the layout.
func HarborpulseFedBy(quay string) []string {
	var out []string
	for _, d := range harborLayout() {
		if (d.kind == harborLight || d.kind == harborCharger) && d.quay == quay {
			out = append(out, d.token)
		}
	}
	return out
}

// harborpulseHoliday is the public holiday the dusk campaign declares: tomorrow, in the town.
func harborpulseHoliday(now time.Time) string {
	zone, err := time.LoadLocation(HarborpulseTownZone)
	if err != nil {
		zone = time.UTC // unreachable: the zone database is embedded
	}
	return HarborpulseHolidayAfter(now.In(zone))
}

// HarborpulseHolidayAfter is the date after t's civil date, in t's location: the holiday a
// campaign created at t declares.
func HarborpulseHolidayAfter(t time.Time) string {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 12, 0, 0, 0, t.Location()).Format("2006-01-02")
}

// harborpulseSolarCampaigns are the street lights following the sun.
func (s *harborpulse) harborpulseSolarCampaigns(now time.Time) []CampaignSpec {
	var holidays []string
	var holidayAtCreation func() string
	if !s.defects.holidayDropped {
		holidays = []string{harborpulseHoliday(now)}
		holidayAtCreation = func() string { return harborpulseHoliday(s.now()) }
	}
	return []CampaignSpec{
		{Token: HarborpulseDuskCampaign, Name: "Street lights to full at dusk", GroupToken: HarborpulseAllLightsGroup,
			holidayAtCreation: holidayAtCreation,
			Trigger:           CampaignTrigger{Kind: campaignKindSolar, Zone: HarborpulseTownZone, Event: solarEventSunset, Holidays: holidays},
			Steps:             []CampaignStepSpec{{Command: HarborpulseSetDimKey, Payload: map[string]any{HarborpulseLevelParam: harborDuskLevel}}},
			// A lamp that missed its dusk by more than five minutes waits for tomorrow's.
			MaxLatenessSeconds: 300, ExpiresInSeconds: 3600},
		{Token: HarborpulseDawnCampaign, Name: "Street lights trimmed at dawn", GroupToken: HarborpulseAllLightsGroup,
			Trigger:            CampaignTrigger{Kind: campaignKindSolar, Zone: HarborpulseTownZone, Event: solarEventSunrise},
			Steps:              []CampaignStepSpec{{Command: HarborpulseSetDimKey, Payload: map[string]any{HarborpulseLevelParam: harborDawnLevel}}},
			MaxLatenessSeconds: 300, ExpiresInSeconds: 3600},
	}
}

func harborpulsePumpWindow() MaintenanceWindowSpec {
	// The slot is rewritten as each sim day runs (see dayJobs); this one only has to be valid.
	return MaintenanceWindowSpec{Token: HarborpulsePumpWindow, Name: "Pump station service slot",
		Description: "When the Works crew may start disruptive work on the pump controllers.",
		Timezone:    harborWindowZone, Slots: []WindowSlot{{Day: "SUNDAY", Start: "03:00", DurationMinutes: 1}}}
}

// harborpulseWellWatch is the early warning on the critical pumps' wells.
func (s *harborpulse) harborpulseWellWatch() DetectionRuleSpec {
	spec := ThresholdAlarmRule{
		Token: HarborpulseWellWatchRule, Name: "Harborpulse wet well watch (critical pumps)", Metric: HarborpulseWetWellKey,
		Op: OpGt, Threshold: HarborpulseWellWatchM, Severity: SeverityWarning, AlarmKey: HarborpulseWellWatchAlarmKey, Enabled: true,
	}.Spec()
	if !s.defects.unscopedWatch {
		spec.Group = HarborpulseCriticalPumpsGroup
	}
	return spec
}

// ---- The jobs each sim day carries ------------------------------------------------------------

// ScenarioJob is one job the sim scheduled for a sim day: a campaign it created and, for the
// flush, the maintenance window's opening. A verifier reads the campaign and the window back from
// the platform; this only tells it which ones belong to the day it judges.
type ScenarioJob struct {
	Day      int    `json:"day"`
	Name     string `json:"name"`
	Campaign string `json:"campaign"`
	Group    string `json:"group"`
	// FireAt is the campaign's CALENDAR instant, RFC 3339.
	FireAt string `json:"fireAt"`
	// WindowOpens and WindowCloses are the maintenance window's opening for the flush job.
	WindowOpens  string `json:"windowOpens,omitempty"`
	WindowCloses string `json:"windowCloses,omitempty"`
}

// The job names on the ledger.
const (
	HarborpulseJobNightDim = "night-dim"
	HarborpulseJobFlush    = "basin-flush"
)

// harborWall is the wall instant of sim hour h on sim day d.
func harborWall(started time.Time, dayLength time.Duration, d int, h float64) time.Time {
	simHours := float64(d)*24 + h - harborpulseStartHour
	return started.Add(time.Duration(simHours / 24 * float64(dayLength)))
}

// ceilMinute is the first whole minute at or after t.
func ceilMinute(t time.Time) time.Time {
	m := t.Truncate(time.Minute)
	if m.Before(t) {
		m = m.Add(time.Minute)
	}
	return m.UTC()
}

// calendarAt is a one-shot CALENDAR trigger at t (a whole minute), in UTC.
func calendarAt(t time.Time) CampaignTrigger {
	t = t.UTC()
	return CampaignTrigger{Kind: campaignKindCalendar, Zone: harborWindowZone, Dates: []string{t.Format("2006-01-02")}, Times: []string{t.Format("15:04")}}
}

// dayJobs plans sim day d's jobs: the basin flush and its window, and the night dim.
func (s *harborpulse) dayJobs(started time.Time, d int) (jobs []ScenarioJob, campaigns []CampaignSpec, window []WindowSlot) {
	run := started.Unix()
	flushAt := ceilMinute(harborWall(started, s.dayLength, d, HarborpulseFlushAtHour))
	opens := ceilMinute(harborWall(started, s.dayLength, d, HarborpulseWindowAtHour))
	closes := opens.Add(harborWindowMinutes * time.Minute)
	dimAt := ceilMinute(harborWall(started, s.dayLength, d, HarborpulseNightDimHour))
	dimGroup := HarborpulseEastLightsGroup
	if s.defects.nightDimAllLights {
		dimGroup = HarborpulseAllLightsGroup
	}
	flush := CampaignSpec{Token: fmt.Sprintf("%s%d-d%d", HarborpulseFlushPrefix, run, d), Name: fmt.Sprintf("Basin wet well flush, sim day %d", d),
		GroupToken: HarborpulseBasinPumpsGroup, Trigger: calendarAt(flushAt),
		// Cover the whole planned hold and slot, plus a fixed delivery margin (also the expiry floor).
		Steps: []CampaignStepSpec{{Command: HarborpulseFlushKey}}, ExpiresInSeconds: int((closes.Sub(flushAt) + harborFlushMargin) / time.Second)}
	dim := CampaignSpec{Token: fmt.Sprintf("%s%d-d%d", HarborpulseNightDimPrefix, run, d), Name: fmt.Sprintf("East Quay night dim, sim day %d", d),
		GroupToken: dimGroup, Trigger: calendarAt(dimAt),
		Steps: []CampaignStepSpec{{Command: HarborpulseSetDimKey, Payload: map[string]any{HarborpulseLevelParam: harborNightDimLevel}}}}
	jobs = []ScenarioJob{
		{Day: d, Name: HarborpulseJobFlush, Campaign: flush.Token, Group: flush.GroupToken, FireAt: flushAt.Format(time.RFC3339),
			WindowOpens: opens.Format(time.RFC3339), WindowCloses: closes.Format(time.RFC3339)},
		{Day: d, Name: HarborpulseJobNightDim, Campaign: dim.Token, Group: dim.GroupToken, FireAt: dimAt.Format(time.RFC3339)},
	}
	window = []WindowSlot{{Day: strings.ToUpper(opens.Weekday().String()), Start: opens.Format("15:04"), DurationMinutes: harborWindowMinutes}}
	return jobs, []CampaignSpec{flush, dim}, window
}

// harborJobLead is how far ahead of its first fire a day's jobs must be written: the campaign
// scheduler looks for work every 15 seconds.
const harborJobLead = 20 * time.Second

// scheduleDay writes sim day d's jobs once: the window's slot, then the two campaigns. It is
// called every tick and does nothing once the day is scheduled, or once it is too late to.
func (s *harborpulse) scheduleDay(ctx context.Context, rt *Runtime, now time.Time) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started.IsZero() || rt.Endpoints.CommandMgmtGraphQL == "" {
		return nil
	}
	d := harborDayOf(now.Sub(started), s.dayLength)
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.scheduled[d] {
		return nil
	}
	jobs, campaigns, slots := s.dayJobs(started, d)
	if fire, err := time.Parse(time.RFC3339, jobs[0].FireAt); err == nil && now.Add(harborJobLead).After(fire) {
		// The day is already past its first job (a sim started mid-afternoon): nothing to judge.
		s.scheduled[d] = true
		log.Warn().Int("day", d).Msg("too late to schedule this sim day's jobs; they start tomorrow")
		return nil
	}
	if err := setMaintenanceWindowSlots(ctx, rt, HarborpulsePumpWindow, harborWindowZone, slots); err != nil {
		return err
	}
	var errs []error
	for _, c := range campaigns {
		if err := createCampaign(ctx, rt, c); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		// A campaign created before the failure is left: the retry next tick refuses it as
		// TOKEN_IN_USE, so a failed day is retried by deleting what it made.
		for _, c := range campaigns {
			_ = deleteCampaign(ctx, rt, c.Token)
		}
		return err
	}
	s.scheduled[d] = true
	s.jobs = append(s.jobs, jobs...)
	// The day before yesterday's campaigns have long fired; their record is kept by the platform
	// against the cycle, and deleting the definition cancels nothing that was sent.
	for _, j := range s.jobs {
		if j.Day == d-3 {
			_ = deleteCampaign(ctx, rt, j.Campaign)
		}
	}
	log.Info().Int("day", d).Str("flush", jobs[0].FireAt).Str("windowOpens", jobs[0].WindowOpens).
		Str("nightDim", jobs[1].FireAt).Msg("scheduled the sim day's jobs")
	return nil
}

// jobLedger is what every day's jobs were, for /status.
func (s *harborpulse) jobLedger() []ScenarioJob {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	return append([]ScenarioJob(nil), s.jobs...)
}

// harborSunTolerance is how far a planned solar instant may sit from the one computed here.
const harborSunTolerance = time.Second

// HarborpulseSunTolerance is harborSunTolerance, for a verifier.
const HarborpulseSunTolerance = harborSunTolerance
