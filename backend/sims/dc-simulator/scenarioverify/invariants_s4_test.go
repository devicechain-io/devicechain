// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/solar"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The S4 day as a working platform answers it. The SOLAR campaigns were created the morning the
// test day runs (2026-11-16 in the town), so tomorrow is their holiday.
const (
	testSolarDay = "2026-11-16"
	testHoliday  = "2026-11-17"
	testFlush    = "hp-basin-flush-1-d0"
	testNightDim = "hp-night-dim-1-d0"
	testFlushBat = "b-flush"
)

func solarElements(t0 string, sunset bool, skip map[string]string) []Element {
	day, _ := time.Parse("2006-01-02", t0)
	var out []Element
	for _, lamp := range sim.HarborpulseGroupMembers()[sim.HarborpulseAllLightsGroup] {
		if reason, ok := skip[lamp]; ok {
			out = append(out, Element{Device: lamp, DueAt: at(6), Status: "SKIPPED", Reason: reason})
			continue
		}
		p, _ := sim.HarborpulseLampPosition(lamp)
		ev, err := solar.Compute(day.Year(), day.Month(), day.Day(), p.Latitude, p.Longitude)
		if err != nil {
			panic(err)
		}
		due := ev.Rise
		if sunset {
			due = ev.Set
		}
		out = append(out, Element{Device: lamp, DueAt: due, Status: "PENDING"})
	}
	return out
}

func solarTrigger(event string, holidays ...string) string {
	raw, _ := json.Marshal(map[string]any{"kind": "SOLAR", "zone": sim.HarborpulseTownZone, "event": event, "holidays": holidays})
	return string(raw)
}

// cleanS4 is the S4 beats as a working platform answers them.
func (d *day) cleanS4() {
	s := S4{Groups: sim.HarborpulseGroupMembers(), Campaigns: map[string]*Campaign{}, Cycles: map[string][]Cycle{},
		Elements: map[string][]Element{}, BatchCommands: map[string][]HeldCommand{}, Positions: map[string]sim.PositionSpec{},
		Ancestors: map[string][]string{}, Assigned: map[string][]string{}, Fed: map[string][]string{}}

	// The critical pumps' early warning (the North Quay pump's well crosses the same level).
	for _, p := range s.Groups[sim.HarborpulseCriticalPumpsGroup] {
		d.alarms = append(d.alarms, lifecycle{"ww-" + p, p, sim.HarborpulseWellWatchAlarmKey, []step{{17.1, sim.AlarmSeverityWarningWire}}, 17.6})
	}

	// The basin flush, held by the window and sent once it opened.
	flushAt, opens := at(sim.HarborpulseFlushAtHour), at(sim.HarborpulseWindowAtHour)
	s.Jobs = append(s.Jobs, sim.ScenarioJob{Day: 0, Name: sim.HarborpulseJobFlush, Campaign: testFlush, Group: sim.HarborpulseBasinPumpsGroup,
		FireAt: flushAt.Format(time.RFC3339), WindowOpens: opens.Format(time.RFC3339), WindowCloses: opens.Add(2 * time.Minute).Format(time.RFC3339)})
	s.Campaigns[testFlush] = &Campaign{Token: testFlush, GroupToken: sim.HarborpulseBasinPumpsGroup, CreatedAt: at(6)}
	s.Cycles[testFlush] = []Cycle{{Key: "t-flush", At: flushAt, Resolved: 1}}
	s.Elements[elementsKey(testFlush, "t-flush")] = []Element{{Device: sim.HarborpulseBasinPump, DueAt: flushAt, Status: "DISPATCHED",
		BatchToken: testFlushBat, CommandStatus: "SUCCESSFUL", CommandOutcome: "SUCCEEDED"}}
	s.BatchCommands[testFlushBat] = []HeldCommand{{Device: sim.HarborpulseBasinPump, Name: sim.HarborpulseFlushKey, Status: "SUCCESSFUL",
		Queued: flushAt.Add(4 * time.Second), Sent: opens.Add(3 * time.Second), Responded: opens.Add(4 * time.Second),
		Window: sim.HarborpulsePumpWindow, BatchToken: testFlushBat}}

	// The night dim targets East Quay; delivery is filled from the observed group below.
	dimAt := at(sim.HarborpulseNightDimHour)
	s.Jobs = append(s.Jobs, sim.ScenarioJob{Day: 0, Name: sim.HarborpulseJobNightDim, Campaign: testNightDim,
		Group: sim.HarborpulseEastLightsGroup, FireAt: dimAt.Format(time.RFC3339)})
	s.Campaigns[testNightDim] = &Campaign{Token: testNightDim, GroupToken: sim.HarborpulseEastLightsGroup, CreatedAt: at(6)}
	for _, lamp := range s.Groups[sim.HarborpulseAllLightsGroup] {
		p, _ := sim.HarborpulseLampPosition(lamp)
		s.Positions[lamp] = p
	}

	// The lights following the sun: the dusk campaign skips tomorrow, the dawn one plans it.
	created := time.Date(2026, 11, 16, 11, 59, 0, 0, time.UTC)
	through := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	s.Campaigns[sim.HarborpulseDuskCampaign] = &Campaign{Token: sim.HarborpulseDuskCampaign, GroupToken: sim.HarborpulseAllLightsGroup,
		Trigger: solarTrigger("SUNSET", testHoliday), CreatedAt: created, PlannedThrough: through}
	s.Campaigns[sim.HarborpulseDawnCampaign] = &Campaign{Token: sim.HarborpulseDawnCampaign, GroupToken: sim.HarborpulseAllLightsGroup,
		Trigger: solarTrigger("SUNRISE"), CreatedAt: created, PlannedThrough: through}
	s.Cycles[sim.HarborpulseDuskCampaign] = []Cycle{{Key: "d2026-11-16", Day: testSolarDay, Resolved: 12}, {Key: "d2026-11-15", Day: "2026-11-15", Resolved: 12}}
	s.Cycles[sim.HarborpulseDawnCampaign] = []Cycle{{Key: "d2026-11-17", Day: testHoliday, Resolved: 12},
		{Key: "d2026-11-16", Day: testSolarDay, Resolved: 12}, {Key: "d2026-11-15", Day: "2026-11-15", Resolved: 12}}
	s.Elements[elementsKey(sim.HarborpulseDuskCampaign, "d2026-11-16")] = solarElements(testSolarDay, true, nil)
	s.Elements[elementsKey(sim.HarborpulseDawnCampaign, "d2026-11-16")] = solarElements(testSolarDay, false, nil)

	// The district's hierarchy and its feeders.
	quays := []string{sim.HarborpulseNorthQuay, sim.HarborpulseEastQuay, sim.HarborpulseBasin}
	for _, m := range sim.HarborpulseMeters() {
		b, q, _ := sim.HarborpulseBuildingOf(m)
		s.Ancestors[b] = []string{sim.HarborpulseQuaySite(q), sim.HarborpulseDistrictAsset}
		s.Assigned[b] = []string{m}
	}
	for _, q := range quays {
		st := sim.HarborpulseStation(q)
		s.Ancestors[st] = []string{sim.HarborpulseQuaySite(q), sim.HarborpulseDistrictAsset}
		s.Assigned[st] = fleetOf(sim.HarborpulsePumpType, q)
		s.Fed[sim.HarborpulseFeeder(q)] = sim.HarborpulseFedBy(q)
	}
	d.s4 = s
	d.nightDimDelivery(sim.HarborpulseEastLightsGroup)
}

// nightDimDelivery models the CALENDAR planner resolving its target group, dispatching one
// command per member, and the model retaining each lamp's level until another command arrives.
func (d *day) nightDimDelivery(group string) {
	dimAt := at(sim.HarborpulseNightDimHour)
	members := d.s4.Groups[group]
	d.s4.Cycles[testNightDim] = []Cycle{{Key: "t-dim", At: dimAt, Resolved: len(members)}}
	var elements []Element
	var commands []HeldCommand
	for _, lamp := range members {
		elements = append(elements, Element{Device: lamp, DueAt: dimAt, Status: "DISPATCHED", BatchToken: "b-dim",
			CommandStatus: "SUCCESSFUL", CommandOutcome: "SUCCEEDED"})
		commands = append(commands, HeldCommand{Device: lamp, Name: sim.HarborpulseSetDimKey, Status: "SUCCESSFUL",
			Queued: dimAt.Add(4 * time.Second), Sent: dimAt.Add(19 * time.Second), Responded: dimAt.Add(20 * time.Second), BatchToken: "b-dim"})
	}
	d.s4.Elements[elementsKey(testNightDim, "t-dim")] = elements
	d.s4.BatchCommands["b-dim"] = commands
	for _, lamp := range d.s4.Groups[sim.HarborpulseAllLightsGroup] {
		var kept []Reading
		for _, r := range d.readings[lamp] {
			if (r.Name != sim.HarborpulseDimKey && r.Name != sim.HarborpulseLampPowerKey) || !inWindow(r.At, dimAt, dimAt.Add(dimWithin)) {
				kept = append(kept, r)
			}
		}
		for tick := dimAt.Add(time.Second); tick.Before(dimAt.Add(dimWithin)); tick = tick.Add(5 * time.Second) {
			v := 100.0
			if sliceHas(members, lamp) && tick.After(dimAt.Add(20*time.Second)) {
				v = sim.HarborpulseNightDimPct
			}
			// The lamps are on at night: harborModel reports 80 W scaled by dim.
			kept = append(kept, Reading{Name: sim.HarborpulseDimKey, At: tick, Value: v},
				Reading{Name: sim.HarborpulseLampPowerKey, At: tick, Value: 80 * v / 100})
		}
		d.readings[lamp] = kept
	}
}

func without(xs []string, x string) []string {
	var out []string
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// Each S4 control is the platform's answer with its defect planted; exactly its invariant turns red.
func TestEachS4ControlTurnsExactlyItsInvariantRed(t *testing.T) {
	controls := []struct {
		name, defect, want, because string
		mutate                      func(*day)
	}{
		{"lamp service targets a neighbour", sim.HarborpulseDefectSuppressionWrongDevice, InvMaintenanceHoldsPage, "early page", wrongMaintenanceDevice},
		{"an East Quay lamp tagged as North Quay's", sim.HarborpulseDefectEastLampMislabelled, InvGroupsResolve, "missing: hp-light-east-2",
			func(d *day) {
				d.s4.Groups = map[string][]string{}
				for g, m := range sim.HarborpulseGroupMembers() {
					d.s4.Groups[g] = m
				}
				d.s4.Groups[sim.HarborpulseEastLightsGroup] = without(d.s4.Groups[sim.HarborpulseEastLightsGroup], "hp-light-east-2")
				d.nightDimDelivery(sim.HarborpulseEastLightsGroup)
			}},
		{"the watch published without its scope", sim.HarborpulseDefectUnscopedWatch, InvGroupScopedRule, "hp-pump-north",
			func(d *day) {
				d.alarms = append(d.alarms, lifecycle{"ww-north", "hp-pump-north", sim.HarborpulseWellWatchAlarmKey,
					[]step{{17.1, sim.AlarmSeverityWarningWire}}, 17.6})
			}},
		{"the night dim aimed at every light", sim.HarborpulseDefectNightDimAllLights, InvNightDim, sim.HarborpulseAllLightsGroup,
			func(d *day) {
				d.s4.Jobs[1].Group = sim.HarborpulseAllLightsGroup
				d.s4.Campaigns[testNightDim].GroupToken = sim.HarborpulseAllLightsGroup
				d.nightDimDelivery(sim.HarborpulseAllLightsGroup)
			}},
		{"a lamp with no installed position", sim.HarborpulseDefectLampUnsited, InvSolarWave, "NO_SITE",
			func(d *day) {
				delete(d.s4.Positions, "hp-light-basin-4")
				skip := map[string]string{"hp-light-basin-4": "NO_SITE"}
				d.s4.Elements[elementsKey(sim.HarborpulseDuskCampaign, "d2026-11-16")] = solarElements(testSolarDay, true, skip)
				d.s4.Elements[elementsKey(sim.HarborpulseDawnCampaign, "d2026-11-16")] = solarElements(testSolarDay, false, skip)
			}},
		{"the dusk campaign without its holiday", sim.HarborpulseDefectHolidayDropped, InvHolidaySkipped, "planned a cycle for " + testHoliday,
			func(d *day) {
				d.s4.Campaigns[sim.HarborpulseDuskCampaign].Trigger = solarTrigger("SUNSET")
				d.s4.Cycles[sim.HarborpulseDuskCampaign] = append([]Cycle{{Key: "d2026-11-17", Day: testHoliday, Resolved: 12}},
					d.s4.Cycles[sim.HarborpulseDuskCampaign]...)
			}},
		{"the pump controllers with no window", sim.HarborpulseDefectFlushUngated, InvWindowHoldsFlush, "before the window opened",
			func(d *day) {
				c := &d.s4.BatchCommands[testFlushBat][0]
				c.Sent, c.Responded, c.Window = c.Queued.Add(time.Second), c.Queued.Add(2*time.Second), ""
			}},
		{"the Lofts filed under the Basin", sim.HarborpulseDefectLoftsUnderBasin, InvAssetHierarchy, sim.HarborpulseLoftsBuilding,
			func(d *day) {
				d.s4.Ancestors[sim.HarborpulseLoftsBuilding] = []string{sim.HarborpulseQuaySite(sim.HarborpulseBasin), sim.HarborpulseDistrictAsset}
			}},
		{"an East Quay lamp wired to the North Quay feeder", sim.HarborpulseDefectFeederMiswired, InvFeederBlastRadius, "missing: hp-light-east-4",
			func(d *day) {
				east, north := sim.HarborpulseFeeder(sim.HarborpulseEastQuay), sim.HarborpulseFeeder(sim.HarborpulseNorthQuay)
				d.s4.Fed[east] = without(d.s4.Fed[east], "hp-light-east-4")
				d.s4.Fed[north] = append(append([]string(nil), d.s4.Fed[north]...), "hp-light-east-4")
			}},
	}
	for _, c := range controls {
		t.Run(c.name, func(t *testing.T) {
			if Controls[c.want] != c.defect {
				t.Fatalf("Controls[%s] = %q, this test plants %q", c.want, Controls[c.want], c.defect)
			}
			evaluateOneRed(t, c.want, c.because, c.mutate)
			if c.defect == sim.HarborpulseDefectEastLampMislabelled || c.defect == sim.HarborpulseDefectNightDimAllLights {
				d := cleanDay()
				c.mutate(d)
				want := sim.HarborpulseGroupMembers()[sim.HarborpulseAllLightsGroup]
				if c.defect == sim.HarborpulseDefectEastLampMislabelled {
					want = without(sim.HarborpulseGroupMembers()[sim.HarborpulseEastLightsGroup], "hp-light-east-2")
				}
				assertNightDimDelivery(t, d, want)
			}
		})
	}
}

func assertNightDimDelivery(t *testing.T, d *day, want []string) {
	t.Helper()
	if got := d.s4.Cycles[testNightDim][0].Resolved; got != len(want) {
		t.Fatalf("night dim resolved %d devices, want %d", got, len(want))
	}
	var elements, commands []string
	for _, e := range d.s4.Elements[elementsKey(testNightDim, "t-dim")] {
		elements = append(elements, e.Device)
		if e.Status != "DISPATCHED" || e.CommandStatus != "SUCCESSFUL" || e.CommandOutcome != "SUCCEEDED" || e.BatchToken != "b-dim" {
			t.Fatalf("incomplete delivered element: %+v", e)
		}
	}
	for _, c := range d.s4.BatchCommands["b-dim"] {
		commands = append(commands, c.Device)
	}
	if len(elements) != len(want) || sameStrings(elements, want) != "" || len(commands) != len(want) || sameStrings(commands, want) != "" {
		t.Fatalf("night dim elements %v and commands %v, want %v", elements, commands, want)
	}
	for _, lamp := range sim.HarborpulseGroupMembers()[sim.HarborpulseAllLightsGroup] {
		wantLevel := 100.0
		if sliceHas(want, lamp) {
			wantLevel = sim.HarborpulseNightDimPct
		}
		rs := readingsOf(Observations{Readings: d.readings}, lamp, sim.HarborpulseDimKey, at(sim.HarborpulseNightDimHour).Add(21*time.Second), at(sim.HarborpulseNightDimHour).Add(dimWithin))
		if len(rs) == 0 {
			t.Fatalf("%s has no dim readings", lamp)
		}
		for _, r := range readingsOf(Observations{Readings: d.readings}, lamp, sim.HarborpulseLampPowerKey,
			at(sim.HarborpulseNightDimHour).Add(21*time.Second), at(sim.HarborpulseNightDimHour).Add(dimWithin)) {
			if r.Value != 80*wantLevel/100 {
				t.Fatalf("%s reported %v W, want %v W", lamp, r.Value, 80*wantLevel/100)
			}
		}
		for _, r := range rs {
			if r.Value != wantLevel {
				t.Fatalf("%s reported %v after delivery, want %v", lamp, r.Value, wantLevel)
			}
		}
	}
}

func TestNightDimRequiresObservedDelivery(t *testing.T) {
	t.Run("member without element", func(t *testing.T) {
		evaluateOneRed(t, InvNightDim, "missing: hp-light-east-1", func(d *day) {
			key := elementsKey(testNightDim, "t-dim")
			d.s4.Elements[key] = d.s4.Elements[key][1:]
		})
	})
	t.Run("member never dimmed", func(t *testing.T) {
		evaluateOneRed(t, InvNightDim, "hp-light-east-1 never reported", func(d *day) {
			for i := range d.readings["hp-light-east-1"] {
				d.readings["hp-light-east-1"][i].Value = 100
			}
		})
	})
	t.Run("observed member beyond layout never dimmed", func(t *testing.T) {
		mutate := func(d *day) {
			d.s4.Groups[sim.HarborpulseEastLightsGroup] = append(d.s4.Groups[sim.HarborpulseEastLightsGroup], "another-lamp")
			key := elementsKey(testNightDim, "t-dim")
			d.s4.Elements[key] = append(d.s4.Elements[key], Element{Device: "another-lamp", Status: "DISPATCHED", CommandOutcome: "SUCCEEDED"})
		}
		evaluateOneRedOf(t, []string{InvGroupsResolve, InvNightDim}, mutate)
		d := cleanDay()
		mutate(d)
		if err := nightDim(d.observe()); err == nil || !strings.Contains(err.Error(), "another-lamp never reported") {
			t.Fatalf("must judge every observed member's delivery: %v", err)
		}
	})

	t.Run("outside member dimmed", func(t *testing.T) {
		evaluateOneRed(t, InvNightDim, "hp-light-north-1 is outside the group", func(d *day) {
			d.readings["hp-light-north-1"] = append(d.readings["hp-light-north-1"], Reading{Name: sim.HarborpulseDimKey,
				At: at(sim.HarborpulseNightDimHour).Add(30 * time.Second), Value: sim.HarborpulseNightDimPct})
		})
	})
	for _, missing := range []bool{false, true} {
		name := "empty membership"
		if missing {
			name = "missing membership"
		}
		t.Run(name, func(t *testing.T) {
			mutate := func(d *day) {
				d.s4.Groups[sim.HarborpulseEastLightsGroup] = nil
				d.nightDimDelivery(sim.HarborpulseEastLightsGroup)
				if missing {
					delete(d.s4.Groups, sim.HarborpulseEastLightsGroup)
				}
			}
			evaluateOneRedOf(t, []string{InvGroupsResolve, InvNightDim}, mutate)
			d := cleanDay()
			mutate(d)
			if err := nightDim(d.observe()); err == nil || !strings.Contains(err.Error(), "cannot judge") {
				t.Fatalf("missing membership must say cannot judge: %v", err)
			}
		})
	}
}

func TestNightDimRejectsDuplicateElements(t *testing.T) {
	evaluateOneRed(t, InvNightDim, "hp-light-east-1: 2 elements, want one", func(d *day) {
		key := elementsKey(testNightDim, "t-dim")
		d.s4.Elements[key] = append(d.s4.Elements[key], d.s4.Elements[key][0])
	})
}

// The wave check reads the platform's positions, not the layout's: a lamp planned for a position
// the platform does not hold is red even when the instant matches the layout.
func TestSolarWaveJudgesAgainstThePlatformsPositions(t *testing.T) {
	evaluateOneRed(t, InvSolarWave, "planned at", func(d *day) {
		p := d.s4.Positions["hp-light-north-2"]
		p.Longitude += 0.05
		d.s4.Positions["hp-light-north-2"] = p
	})
}

// A day the sim scheduled no jobs for, or a holiday not yet planned past, says so rather than
// passing.
func TestS4MissingDataIsRed(t *testing.T) {
	evaluateOneRedOf(t, []string{InvNightDim, InvWindowHoldsFlush, InvMaintenanceHoldsPage}, func(d *day) { d.s4.Jobs = nil; d.maint.Window = nil })
	evaluateOneRed(t, InvHolidaySkipped, "not judged yet", func(d *day) {
		d.s4.Campaigns[sim.HarborpulseDuskCampaign].PlannedThrough = time.Date(2026, 11, 16, 23, 0, 0, 0, time.UTC)
	})
	// The same curve, lower: it still falls after the start, and never reaches the watch. A well
	// that never rises also never crosses the pump's own shared threshold, so the attribute
	// invariant, which judges the same readings, is red with it; the group-scoped rule must still
	// say why it is red.
	lower := func(d *day) {
		for i, r := range d.readings["hp-pump-north"] {
			switch r.Name {
			case sim.HarborpulseWetWellKey:
				d.readings["hp-pump-north"][i].Value -= 0.8
			case sim.HarborpulseWellFillKey:
				d.readings["hp-pump-north"][i].Value -= 0.8 / sim.HarborpulseWellDepthM * 100
			}
		}
	}
	evaluateOneRedOf(t, []string{InvGroupScopedRule, InvAttributeThresholdMoves}, lower)
	d := cleanDay()
	lower(d)
	for _, inv := range Evaluate(d.observe()) {
		if inv.Name == InvGroupScopedRule && !strings.Contains(inv.Detail, "never above") {
			t.Fatalf("%s: %q does not say the well was never above the watch", inv.Name, inv.Detail)
		}
	}
}

// evaluateOneRedOf asserts exactly the named invariants turn red.
func evaluateOneRedOf(t *testing.T, want []string, mutate func(*day)) {
	t.Helper()
	d := cleanDay()
	mutate(d)
	red := setOf(want)
	for _, inv := range Evaluate(d.observe()) {
		if red[inv.Name] == inv.Passed {
			t.Fatalf("%s passed=%v (%s); want red exactly %v", inv.Name, inv.Passed, inv.Detail, want)
		}
	}
}
