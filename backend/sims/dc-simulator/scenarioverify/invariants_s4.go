// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/solar"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The S4 invariants: the fleet operations. Groups and a rule scoped to one, campaigns on three
// kinds of trigger, a maintenance window holding a disruptive command, the asset hierarchy and
// the relationship graph.
const (
	InvGroupsResolve     = "groups-resolve"
	InvGroupScopedRule   = "group-scoped-rule"
	InvNightDim          = "night-dim-campaign"
	InvSolarWave         = "solar-wave"
	InvHolidaySkipped    = "holiday-skipped"
	InvWindowHoldsFlush  = "window-holds-flush"
	InvAssetHierarchy    = "asset-hierarchy"
	InvFeederBlastRadius = "feeder-blast-radius"
)

// S4 is what the verifier read for the fleet-operation beats.
type S4 struct {
	// Groups is each group's members as the platform resolves it for a command.
	Groups map[string][]string
	// Jobs are the sim's jobs for the judged day (the ledger names the campaigns; the platform is
	// read for what they did).
	Jobs []sim.ScenarioJob
	// Campaigns, by token, with each one's cycles and the elements of the cycles that were read.
	Campaigns map[string]*Campaign
	Cycles    map[string][]Cycle
	Elements  map[string][]Element // campaign + "/" + cycle key
	// BatchCommands are the commands of each batch a judged element names.
	BatchCommands map[string][]HeldCommand
	// Positions are the lamps' installed positions as the platform holds them.
	Positions map[string]sim.PositionSpec
	// Ancestors and Assigned are each building's and station's path to the root and the devices
	// assigned to it; Fed is each feeder's typed relationships.
	Ancestors map[string][]string
	Assigned  map[string][]string
	Fed       map[string][]string
}

func init() {
	Controls[InvGroupsResolve] = sim.HarborpulseDefectEastLampMislabelled
	Controls[InvGroupScopedRule] = sim.HarborpulseDefectUnscopedWatch
	Controls[InvNightDim] = sim.HarborpulseDefectNightDimAllLights
	Controls[InvSolarWave] = sim.HarborpulseDefectLampUnsited
	Controls[InvHolidaySkipped] = sim.HarborpulseDefectHolidayDropped
	Controls[InvWindowHoldsFlush] = sim.HarborpulseDefectFlushUngated
	Controls[InvAssetHierarchy] = sim.HarborpulseDefectLoftsUnderBasin
	Controls[InvFeederBlastRadius] = sim.HarborpulseDefectFeederMiswired
}

// s4Checks are the S4 invariants in report order.
func s4Checks() []struct {
	name string
	fn   func(Observations) error
} {
	return []struct {
		name string
		fn   func(Observations) error
	}{
		{InvGroupsResolve, groupsResolve},
		{InvGroupScopedRule, groupScopedRule},
		{InvNightDim, nightDim},
		{InvSolarWave, solarWave},
		{InvHolidaySkipped, holidaySkipped},
		{InvWindowHoldsFlush, windowHoldsFlush},
		{InvMaintenanceHoldsPage, maintenanceHoldsPage},
		{InvMaintenanceControlPages, maintenanceControlPages},
		{InvAssetHierarchy, assetHierarchy},
		{InvFeederBlastRadius, feederBlastRadius},
	}
}

// The S4 timing tolerances.
const (
	// campaignLate is how long after its instant a CALENDAR element may be sent: the scheduler
	// looks for due work every 15 seconds.
	campaignLate = 20 * time.Second
	// dimWithin is how long after the night dim's instant every lamp must report the new level:
	// the scheduler's lag, the command's round trip and the next reading.
	dimWithin = 90 * time.Second
	// windowEarly is how far before the window's opening a held command may be sent (clock skew
	// between the platform's replicas and this process); none should be.
	windowEarly = 2 * time.Second
	// minWave is the least spread of planned instants along the promenade that shows a wave:
	// East Quay's first lamp to the Basin's last is about 0.08 degrees of longitude, some nineteen
	// seconds of sun.
	minWave = 10 * time.Second
)

func elementsKey(campaign, cycle string) string { return campaign + "/" + cycle }

func sameStrings(have, want []string) string {
	seen := map[string]time.Time{}
	for _, h := range have {
		seen[h] = time.Time{}
	}
	diff := sameSet(seen, want)
	diff = strings.ReplaceAll(diff, "raised outside the expected set", "unexpected")
	return strings.ReplaceAll(diff, "never raised for", "missing")
}

// groupsResolve: every group resolves, as a command target, to exactly the devices whose
// attributes put them in it.
func groupsResolve(o Observations) error {
	var problems []string
	want := sim.HarborpulseGroupMembers()
	for _, g := range sortedKeys(want) {
		have, ok := o.S4.Groups[g]
		if !ok {
			problems = append(problems, g+": not resolved")
			continue
		}
		if diff := sameStrings(have, want[g]); diff != "" {
			problems = append(problems, g+": "+diff)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// groupScopedRule: the wet-well watch is raised for exactly the critical pumps, while a pump
// outside the group crossed the same level: the scope, not the water, is what kept it quiet.
func groupScopedRule(o Observations) error {
	members := sim.HarborpulseGroupMembers()[sim.HarborpulseCriticalPumpsGroup]
	seen := activeDevices(o, sim.HarborpulseWellWatchAlarmKey, o.at(WatchFrom), o.end())
	if diff := sameSet(seen, members); diff != "" {
		return fmt.Errorf("%s %s", sim.HarborpulseWellWatchAlarmKey, diff)
	}
	in := setOf(members)
	excluded := 0
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		if in[p] {
			continue
		}
		peak := math.Inf(-1)
		for _, r := range readingsOf(o, p, sim.HarborpulseWetWellKey, o.at(stormFrom), o.at(pumpTo)) {
			peak = math.Max(peak, r.Value)
		}
		if peak <= sim.HarborpulseWellWatchM {
			return fmt.Errorf("%s is outside the group but its well peaked at %.2f m, never above the watch's %.1f m: "+
				"nothing shows the scope excluded it", p, peak, sim.HarborpulseWellWatchM)
		}
		excluded++
	}
	if excluded == 0 {
		return fmt.Errorf("every pump is in the group, so nothing shows a scope")
	}
	return nil
}

// jobOf is the judged day's job of a name.
func jobOf(o Observations, name string) (sim.ScenarioJob, error) {
	for _, j := range o.S4.Jobs {
		if j.Name == name && j.Day == o.Day {
			return j, nil
		}
	}
	return sim.ScenarioJob{}, fmt.Errorf("the sim scheduled no %s job for day %d", name, o.Day)
}

// jobCycle is the one cycle a job's CALENDAR campaign fired at its instant, and its elements.
func jobCycle(o Observations, j sim.ScenarioJob) (*Campaign, time.Time, []Element, error) {
	fire, err := time.Parse(time.RFC3339, j.FireAt)
	if err != nil {
		return nil, time.Time{}, nil, fmt.Errorf("job %s fireAt %q: %v", j.Name, j.FireAt, err)
	}
	c := o.S4.Campaigns[j.Campaign]
	if c == nil {
		return nil, fire, nil, fmt.Errorf("campaign %s does not exist on the platform", j.Campaign)
	}
	cycles := o.S4.Cycles[j.Campaign]
	if len(cycles) != 1 || !cycles[0].At.Equal(fire) {
		var ats []string
		for _, cy := range cycles {
			ats = append(ats, cy.At.Format(time.RFC3339))
		}
		return c, fire, nil, fmt.Errorf("campaign %s fired %d cycles (%s), want one at %s", j.Campaign, len(cycles), strings.Join(ats, ", "), j.FireAt)
	}
	return c, fire, o.S4.Elements[elementsKey(j.Campaign, cycles[0].Key)], nil
}

// nightDim: the night dim delivered once to each observed group member, each command succeeded,
// and each member reported the dimmed level while no other lamp did. groupsResolve alone judges
// whether the observed membership matches the layout.
func nightDim(o Observations) error {
	j, err := jobOf(o, sim.HarborpulseJobNightDim)
	if err != nil {
		return err
	}
	c, fire, elements, err := jobCycle(o, j)
	if err != nil {
		return err
	}
	if c.GroupToken != sim.HarborpulseEastLightsGroup {
		return fmt.Errorf("campaign %s targets group %s, not %s", c.Token, c.GroupToken, sim.HarborpulseEastLightsGroup)
	}
	east := o.S4.Groups[sim.HarborpulseEastLightsGroup]
	if len(east) == 0 {
		return fmt.Errorf("cannot judge the night dim: observed membership of %s is missing or empty", sim.HarborpulseEastLightsGroup)
	}
	var devices []string
	var problems []string
	counts := map[string]int{}
	for _, e := range elements {
		devices = append(devices, e.Device)
		counts[e.Device]++
		if e.Status != "DISPATCHED" || e.CommandOutcome != "SUCCEEDED" {
			problems = append(problems, fmt.Sprintf("%s: %s/%s %s", e.Device, e.Status, e.CommandOutcome, e.Reason))
		}
	}
	if diff := sameStrings(devices, east); diff != "" {
		return fmt.Errorf("the night dim's elements: %s", diff)
	}
	for _, device := range east {
		if counts[device] != 1 {
			problems = append(problems, fmt.Sprintf("%s: %d elements, want one", device, counts[device]))
		}
	}
	targeted := setOf(east)
	lamps := setOf(fleetOf(sim.HarborpulseLightType, ""))
	for _, lamp := range east {
		lamps[lamp] = true
	}
	for _, lamp := range sortedKeys(lamps) {
		dimmed := false
		for _, r := range readingsOf(o, lamp, sim.HarborpulseDimKey, fire, fire.Add(dimWithin)) {
			dimmed = dimmed || r.Value == sim.HarborpulseNightDimPct
		}
		switch {
		case targeted[lamp] && !dimmed:
			problems = append(problems, fmt.Sprintf("%s never reported %d %% within %s of the dim", lamp, sim.HarborpulseNightDimPct, dimWithin))
		case !targeted[lamp] && dimmed:
			problems = append(problems, fmt.Sprintf("%s is outside the group and reported %d %%", lamp, sim.HarborpulseNightDimPct))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// townDay is the civil date of t in the town's zone.
func townDay(t time.Time) string {
	zone, err := time.LoadLocation(sim.HarborpulseTownZone)
	if err != nil {
		zone = time.UTC
	}
	return t.In(zone).Format("2006-01-02")
}

func townMidnight(day string) (time.Time, error) {
	zone, err := time.LoadLocation(sim.HarborpulseTownZone)
	if err != nil {
		return time.Time{}, err
	}
	return time.ParseInLocation("2006-01-02", day, zone)
}

// solarCycle is the cycle of a SOLAR campaign for a civil day, and its elements.
func solarCycle(o Observations, campaign, day string) ([]Element, bool) {
	for _, cy := range o.S4.Cycles[campaign] {
		if cy.Day == day {
			return o.S4.Elements[elementsKey(campaign, cy.Key)], true
		}
	}
	return nil, false
}

// solarWave: for the dusk and the dawn campaign, every street light has a planned instant on the
// day the campaign was created, and that instant is the sunset (sunrise) at the position the
// platform holds for the lamp; ordered by instant, the lamps run east to west along the promenade.
func solarWave(o Observations) error {
	var problems []string
	lamps := sim.HarborpulseGroupMembers()[sim.HarborpulseAllLightsGroup]
	for _, k := range []struct {
		campaign string
		sunset   bool
	}{{sim.HarborpulseDuskCampaign, true}, {sim.HarborpulseDawnCampaign, false}} {
		c := o.S4.Campaigns[k.campaign]
		if c == nil {
			problems = append(problems, k.campaign+": does not exist")
			continue
		}
		day := townDay(c.CreatedAt)
		elements, ok := solarCycle(o, k.campaign, day)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no cycle planned for %s, the day it was created", k.campaign, day))
			continue
		}
		civil, _ := time.Parse("2006-01-02", day)
		by := map[string]Element{}
		for _, e := range elements {
			by[e.Device] = e
		}
		type planned struct {
			lamp string
			at   time.Time
			lon  float64
		}
		var wave []planned
		for _, lamp := range lamps {
			e, ok := by[lamp]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: nothing planned for %s", k.campaign, lamp))
				continue
			}
			if e.Status == "SKIPPED" && (e.Reason == "NO_SITE" || strings.HasPrefix(e.Reason, "POLAR")) {
				problems = append(problems, fmt.Sprintf("%s: %s skipped, %s", k.campaign, lamp, e.Reason))
				continue
			}
			pos, ok := o.S4.Positions[lamp]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: %s has no installed position on the platform", k.campaign, lamp))
				continue
			}
			ev, err := solar.Compute(civil.Year(), civil.Month(), civil.Day(), pos.Latitude, pos.Longitude)
			if err != nil || ev.Status != solar.Normal {
				problems = append(problems, fmt.Sprintf("%s: no sun event at %s's position (%v)", k.campaign, lamp, err))
				continue
			}
			want := ev.Rise
			if k.sunset {
				want = ev.Set
			}
			if d := e.DueAt.Sub(want); d > sim.HarborpulseSunTolerance || d < -sim.HarborpulseSunTolerance {
				problems = append(problems, fmt.Sprintf("%s: %s planned at %s, its position's sun is at %s",
					k.campaign, lamp, e.DueAt.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano)))
				continue
			}
			wave = append(wave, planned{lamp, e.DueAt, pos.Longitude})
		}
		if len(wave) != len(lamps) {
			continue
		}
		sort.Slice(wave, func(i, j int) bool { return wave[i].at.Before(wave[j].at) })
		for i := 1; i < len(wave); i++ {
			if wave[i].lon >= wave[i-1].lon {
				problems = append(problems, fmt.Sprintf("%s: %s is planned after %s but stands east of it", k.campaign, wave[i].lamp, wave[i-1].lamp))
				break
			}
		}
		if spread := wave[len(wave)-1].at.Sub(wave[0].at); spread < minWave {
			problems = append(problems, fmt.Sprintf("%s: the planned instants span %s, less than the %s the promenade's length gives", k.campaign, spread, minWave))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// holidaySkipped: the dusk campaign declares the day after it was created a holiday, has planned
// past that day's midnight, and planned no cycle for it, while the dawn campaign, which declares
// no holiday, planned that day.
func holidaySkipped(o Observations) error {
	dusk, dawn := o.S4.Campaigns[sim.HarborpulseDuskCampaign], o.S4.Campaigns[sim.HarborpulseDawnCampaign]
	if dusk == nil || dawn == nil {
		return fmt.Errorf("the dusk or the dawn campaign does not exist")
	}
	zone, err := time.LoadLocation(sim.HarborpulseTownZone)
	if err != nil {
		return err
	}
	holiday := sim.HarborpulseHolidayAfter(dusk.CreatedAt.In(zone))
	var trigger struct {
		Holidays []string `json:"holidays"`
	}
	if err := json.Unmarshal([]byte(dusk.Trigger), &trigger); err != nil {
		return fmt.Errorf("the dusk campaign's trigger %q: %v", dusk.Trigger, err)
	}
	midnight, err := townMidnight(holiday)
	if err != nil {
		return err
	}
	var problems []string
	if !sliceHas(trigger.Holidays, holiday) {
		problems = append(problems, fmt.Sprintf("the dusk campaign declares holidays %v, not %s", trigger.Holidays, holiday))
	}
	if !dusk.PlannedThrough.After(midnight) {
		return fmt.Errorf("the dusk campaign is planned only through %s, not past %s's midnight: the holiday is not judged yet",
			dusk.PlannedThrough.Format(time.RFC3339), holiday)
	}
	if _, planned := solarCycle(o, sim.HarborpulseDuskCampaign, holiday); planned {
		problems = append(problems, fmt.Sprintf("the dusk campaign planned a cycle for %s, the holiday", holiday))
	}
	if _, planned := solarCycle(o, sim.HarborpulseDawnCampaign, holiday); !planned {
		problems = append(problems, fmt.Sprintf("the dawn campaign (no holiday) planned no cycle for %s either, so the skip shows nothing", holiday))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// windowHoldsFlush: the flush campaign fired once for the Basin pump, and its DISRUPTIVE command
// was held by the pump controllers' maintenance window: queued before the window opened, sent
// once it had, and answered.
func windowHoldsFlush(o Observations) error {
	j, err := jobOf(o, sim.HarborpulseJobFlush)
	if err != nil {
		return err
	}
	_, fire, elements, err := jobCycle(o, j)
	if err != nil {
		return err
	}
	opens, err1 := time.Parse(time.RFC3339, j.WindowOpens)
	closes, err2 := time.Parse(time.RFC3339, j.WindowCloses)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("the flush job's window %q .. %q is unreadable", j.WindowOpens, j.WindowCloses)
	}
	want := sim.HarborpulseGroupMembers()[sim.HarborpulseBasinPumpsGroup]
	if len(elements) != len(want) || len(elements) != 1 || elements[0].Device != want[0] {
		var ds []string
		for _, e := range elements {
			ds = append(ds, e.Device)
		}
		return fmt.Errorf("the flush planned elements for [%s], want %v", strings.Join(ds, ", "), want)
	}
	e := elements[0]
	if e.Status != "DISPATCHED" || e.BatchToken == "" {
		return fmt.Errorf("the flush element is %s (%s), not dispatched", e.Status, e.Reason)
	}
	cmds := o.S4.BatchCommands[e.BatchToken]
	if len(cmds) != 1 {
		return fmt.Errorf("the flush batch %s created %d commands, want one", e.BatchToken, len(cmds))
	}
	c := cmds[0]
	switch {
	case c.Device != want[0] || c.Name != sim.HarborpulseFlushKey:
		return fmt.Errorf("the flush batch commanded %s %s", c.Device, c.Name)
	case c.Queued.Before(fire.Add(-campaignLate)) || c.Queued.After(fire.Add(campaignLate)):
		return fmt.Errorf("the flush was queued at %s, not at its instant %s", c.Queued.Format(time.RFC3339), j.FireAt)
	case c.Sent.IsZero():
		return fmt.Errorf("the flush was never sent (%s)", c.Status)
	case c.Sent.Before(opens.Add(-windowEarly)):
		return fmt.Errorf("the flush was sent at %s, before the window opened at %s (window %q): nothing held it",
			c.Sent.Format(time.RFC3339), j.WindowOpens, c.Window)
	case !c.Sent.Before(closes):
		return fmt.Errorf("the flush was sent at %s, after the window closed at %s", c.Sent.Format(time.RFC3339), j.WindowCloses)
	case c.Window != sim.HarborpulsePumpWindow:
		return fmt.Errorf("the flush carries maintenance window %q, not %s", c.Window, sim.HarborpulsePumpWindow)
	case c.Status != "SUCCESSFUL":
		return fmt.Errorf("the flush ended %s", c.Status)
	}
	return nil
}

// assetHierarchy: each customer meter is assigned to its building and each pump to its station,
// and each building and station sits under the quay the device stands on, under the waterfront.
func assetHierarchy(o Observations) error {
	var problems []string
	check := func(device, asset, quay string) {
		if !sliceHas(o.S4.Assigned[asset], device) {
			problems = append(problems, fmt.Sprintf("%s is not assigned to %s (assigned: %v)", device, asset, o.S4.Assigned[asset]))
		}
		want := []string{sim.HarborpulseQuaySite(quay), sim.HarborpulseDistrictAsset}
		if have := o.S4.Ancestors[asset]; strings.Join(have, ">") != strings.Join(want, ">") {
			problems = append(problems, fmt.Sprintf("%s sits under %v, want %v", asset, have, want))
		}
	}
	for _, m := range sim.HarborpulseMeters() {
		building, quay, _ := sim.HarborpulseBuildingOf(m)
		check(m, building, quay)
	}
	for _, d := range sim.HarborpulseFleet() {
		if d.DeviceType == sim.HarborpulsePumpType {
			check(d.Token, sim.HarborpulseStation(d.Quay), d.Quay)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// feederBlastRadius: each quay's feeder feeds exactly that quay's lights and chargers, and every
// device the failed feeder feeds went silent in the outage: the graph named the blast radius.
func feederBlastRadius(o Observations) error {
	var problems []string
	for _, q := range []string{sim.HarborpulseNorthQuay, sim.HarborpulseEastQuay, sim.HarborpulseBasin} {
		feeder := sim.HarborpulseFeeder(q)
		if diff := sameStrings(o.S4.Fed[feeder], sim.HarborpulseFedBy(q)); diff != "" {
			problems = append(problems, feeder+" feeds: "+diff)
		}
	}
	outFrom, outTo := o.at(sim.HarborpulseOutageFrom), o.at(sim.HarborpulseOutageTo)
	silent := activeDevices(o, sim.HarborpulseLightSilentAlarmKey, outFrom, outTo)
	for d, at := range activeDevices(o, sim.HarborpulseChargerOfflineKey, outFrom, outTo) {
		silent[d] = at
	}
	for _, d := range o.S4.Fed[sim.HarborpulseFeeder(sim.HarborpulseOutageQuay)] {
		if _, ok := silent[d]; !ok {
			problems = append(problems, fmt.Sprintf("%s is fed by the failed feeder and never went silent", d))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}
