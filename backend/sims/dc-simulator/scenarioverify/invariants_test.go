// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// The day as a working platform answers it, built from alarm lifecycles, commands and readings
// shaped like the beats. Each planted defect is then applied to it the way the defect changes the
// platform's answer, and the test asserts that EXACTLY the matching invariant turns red: a check
// is worth nothing until it has been shown to fail, and a control that turns everything red shows
// nothing about the check it is for.

var testClock = Clock{StartedAt: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), DayLength: 24 * time.Minute, StartHour: 6}

func at(h float64) time.Time { return testClock.At(0, h) }

// lifecycle is one alarm: the severity it moves through (from the hour given) and when it clears.
type lifecycle struct {
	token, device, key string
	steps              []step
	clear              float64
}

type step struct {
	hour     float64
	severity string
}

func (l lifecycle) sampleAt(t time.Time) (AlarmSample, bool) {
	if len(l.steps) == 0 || t.Before(at(l.steps[0].hour)) {
		return AlarmSample{}, false
	}
	s := AlarmSample{At: t, Token: l.token, Device: l.device, Key: l.key, State: sim.AlarmStateActiveWire}
	for _, st := range l.steps {
		if !t.Before(at(st.hour)) {
			s.Severity = st.severity
		}
	}
	if !t.Before(at(l.clear)) {
		s.State = sim.AlarmStateClearedWire
	}
	return s, true
}

const (
	minor    = sim.AlarmSeverityMinorWire
	major    = sim.AlarmSeverityMajorWire
	critical = sim.AlarmSeverityCriticalWire
)

type day struct {
	alarms   []lifecycle
	commands []Command
	readings map[string][]Reading
	corr     Preview
	control  Preview
	backfill []Backfill
}

func cleanDay() *day {
	d := &day{readings: map[string][]Reading{}}
	for _, g := range sim.HarborpulseFleet() {
		switch {
		case g.DeviceType == sim.HarborpulseGaugeType && g.Quay == sim.HarborpulseEastQuay:
			d.alarms = append(d.alarms, lifecycle{"a-" + g.Token, g.Token, sim.HarborpulseFloodAlarmKey,
				[]step{{17.2, minor}, {17.6, major}, {18.0, critical}, {20.5, major}, {21.0, minor}}, 21.5})
		case g.DeviceType == sim.HarborpulseGaugeType:
			d.alarms = append(d.alarms, lifecycle{"a-" + g.Token, g.Token, sim.HarborpulseFloodAlarmKey,
				[]step{{17.4, minor}, {17.9, major}, {20.8, minor}}, 21.3})
		case g.DeviceType == sim.HarborpulseLightType && g.Quay == sim.HarborpulseEastQuay:
			d.alarms = append(d.alarms, lifecycle{"s-" + g.Token, g.Token, sim.HarborpulseLightSilentAlarmKey, []step{{20.5, major}}, 21.3})
		case g.DeviceType == sim.HarborpulseChargerType && g.Quay == sim.HarborpulseEastQuay:
			d.alarms = append(d.alarms, lifecycle{"o-" + g.Token, g.Token, sim.HarborpulseChargerOfflineKey, []step{{20.5, major}}, 21.3})
			d.backfill = append(d.backfill, backfillOf(g.Token))
		}
	}
	for _, p := range []string{"hp-pump-north", sim.HarborpulseBasinPump} {
		d.alarms = append(d.alarms, lifecycle{"w-" + p, p, sim.HarborpulseWetWellAlarmKey, []step{{17.2, major}}, 17.5})
		d.pumpStarted(p, 17.2)
	}
	d.alarms = append(d.alarms, lifecycle{"l-east", sim.HarborpulseAirLockPump, sim.HarborpulseAirLockAlarmKey, []step{{19.3, critical}}, 20.6})

	c := sim.HarborpulseHotCharger
	d.alarms = append(d.alarms, lifecycle{"h-" + c, c, sim.HarborpulseConnectorHotAlarmKey, []step{{14.2, major}}, 14.6})
	answered := at(14.2).Add(2 * time.Second)
	d.commands = append(d.commands, Command{Device: c, Name: sim.HarborpulseDerateKey, Status: "SUCCESSFUL", Queued: at(14.2), Responded: answered})
	for t := at(14.0); t.Before(at(15.0)); t = t.Add(5 * time.Second) {
		v := 22.0
		if t.After(answered) {
			v = sim.HarborpulseDeratedKw
		}
		temp := 91.0
		if t.After(answered.Add(10 * time.Second)) {
			temp = 60.5
		}
		d.readings[c] = append(d.readings[c],
			Reading{Name: sim.HarborpulseMaxKwKey, At: t, Value: v},
			Reading{Name: sim.HarborpulseConnectorKey, At: t, Value: temp})
	}

	d.corr = Preview{OK: true, Firings: []Firing{
		{At: at(18.6), Series: sim.HarborpulseEastQuay, Signal: "raised"},
		{At: at(20.5), Series: sim.HarborpulseEastQuay, Signal: "resolved"},
	}}
	d.control = Preview{OK: true}
	return d
}

// pumpStarted records a start-pump answered at hour h, the pump running from then and its well
// falling from just under the platform's level.
func (d *day) pumpStarted(p string, h float64) {
	answered := at(h).Add(2 * time.Second)
	d.commands = append(d.commands, Command{Device: p, Name: sim.HarborpulseStartPumpKey, Status: "SUCCESSFUL", Queued: at(h), Responded: answered})
	for t := at(16); t.Before(at(22)); t = t.Add(5 * time.Second) {
		well, running := 2.6, 0.0
		if t.After(answered) {
			well, running = 2.6-0.025*t.Sub(answered).Seconds(), 1
			if well < 0.8 {
				well = 0.8
			}
		}
		d.readings[p] = append(d.readings[p],
			Reading{Name: sim.HarborpulseWetWellKey, At: t, Value: well},
			Reading{Name: sim.HarborpulsePumpRunningKey, At: t, Value: running})
	}
}

func backfillOf(device string) Backfill {
	b := Backfill{Device: device, First: at(19.75).Add(time.Second)}
	for t := b.First; t.Before(at(21.25)); t = t.Add(5 * time.Second) {
		b.Buffered++
		b.Last = t
		b.Stored = append(b.Stored, t)
	}
	return b
}

func (d *day) observe() Observations {
	o := Observations{Clock: testClock, Day: 0, Commands: d.commands, Readings: d.readings,
		Correlation: d.corr, CorrelationControl: d.control, Backfill: d.backfill}
	for t := at(WatchFrom); !t.After(at(WatchTo).Add(20 * time.Second)); t = t.Add(2 * time.Second) {
		o.Polls = append(o.Polls, t)
		for _, l := range d.alarms {
			if s, ok := l.sampleAt(t); ok {
				o.Alarms = append(o.Alarms, s)
			}
		}
	}
	return o
}

func failing(invs []Invariant) []string {
	var out []string
	for _, i := range invs {
		if !i.Passed {
			out = append(out, i.Name+": "+i.Detail)
		}
	}
	return out
}

func TestCleanDayPassesEveryInvariant(t *testing.T) {
	o := cleanDay().observe()
	if err := Coverage(o); err != nil {
		t.Fatalf("coverage: %v", err)
	}
	invs := Evaluate(o)
	if len(invs) != len(Controls) {
		t.Fatalf("%d invariants judged, %d have a control: every invariant needs one", len(invs), len(Controls))
	}
	if f := failing(invs); len(f) > 0 {
		t.Fatalf("a clean day failed:\n%s", strings.Join(f, "\n"))
	}
}

// Each control is the platform's answer with ONE defect, applied the way that defect changes it.
// The matching invariant must turn red and every other must stay green.
func TestEachControlTurnsExactlyItsInvariantRed(t *testing.T) {
	controls := []struct {
		name    string
		defect  string
		want    string
		mutate  func(*day)
		because string
	}{
		{
			name: "flood tier under its own key", defect: sim.HarborpulseDefectFloodSplitKey, want: InvFloodEscalates,
			because: "2 flood alarms active",
			mutate: func(d *day) {
				for i, l := range d.alarms {
					if l.key == sim.HarborpulseFloodAlarmKey && strings.Contains(l.device, "-east-") {
						d.alarms[i].steps = []step{{17.2, minor}, {17.6, major}, {21.0, minor}}
						d.alarms = append(d.alarms, lifecycle{"c-" + l.device, l.device, sim.HarborpulseFloodSplitAlarmKey,
							[]step{{18.0, critical}}, 20.5})
					}
				}
			},
		},
		{
			name: "nothing answers start-pump", defect: "no-command-far-end", want: InvPumpCommanded,
			because: "no start-pump reached SUCCESSFUL",
			mutate: func(d *day) {
				var kept []Command
				for _, c := range d.commands {
					if c.Name == sim.HarborpulseStartPumpKey {
						c.Status, c.Responded = "SENT", time.Time{}
					}
					kept = append(kept, c)
				}
				d.commands = kept
				for _, p := range []string{"hp-pump-north", sim.HarborpulseBasinPump} {
					d.readings[p] = nil
					for t := at(16); t.Before(at(22)); t = t.Add(5 * time.Second) {
						d.readings[p] = append(d.readings[p], Reading{Name: sim.HarborpulseWetWellKey, At: t, Value: 3.25})
					}
				}
			},
		},
		{
			name: "the charger refuses derate", defect: sim.HarborpulseDefectRefuseDerate, want: InvDerateObeyed,
			because: "no derate reached SUCCESSFUL",
			mutate: func(d *day) {
				for i, c := range d.commands {
					if c.Name == sim.HarborpulseDerateKey {
						d.commands[i].Status = "FAILED"
					}
				}
				for i, l := range d.alarms {
					if l.key == sim.HarborpulseConnectorHotAlarmKey {
						d.alarms[i].clear = 16.1
					}
				}
			},
		},
		{
			name: "the derate is acknowledged but not applied", defect: "(none: a device that answers without acting)", want: InvDerateObeyed,
			because: "did not cool it",
			mutate: func(d *day) {
				c := sim.HarborpulseHotCharger
				for i, r := range d.readings[c] {
					if r.Name == sim.HarborpulseConnectorKey && r.At.Before(at(sim.HarborpulseOverheatTo)) {
						d.readings[c][i].Value = 91
					}
				}
			},
		},
		{
			name: "the air lock on the Basin pump", defect: sim.HarborpulseDefectAirLockBasin, want: InvAirLockOnlyEast,
			because: sim.HarborpulseBasinPump,
			mutate: func(d *day) {
				for i, l := range d.alarms {
					if l.key == sim.HarborpulseAirLockAlarmKey {
						d.alarms[i].device = sim.HarborpulseBasinPump
					}
				}
			},
		},
		{
			name: "a North Quay lamp silent in the outage", defect: sim.HarborpulseDefectSilenceNorthLamp, want: InvOutageBlastRadius,
			because: "hp-light-north-1",
			mutate: func(d *day) {
				d.alarms = append(d.alarms, lifecycle{"s-north", "hp-light-north-1", sim.HarborpulseLightSilentAlarmKey, []step{{20.5, major}}, 21.3})
			},
		},
		{
			name: "every fifth buffered reading lost", defect: sim.HarborpulseDefectDropBackfill, want: InvBackfillStored,
			because: "stored at their own times",
			mutate: func(d *day) {
				for i := range d.backfill {
					var kept []time.Time
					for j, t := range d.backfill[i].Stored {
						if (j+1)%5 != 0 {
							kept = append(kept, t)
						}
					}
					d.backfill[i].Stored = kept
				}
			},
		},
		{
			name: "backfill stamped with its arrival time", defect: "(none: the platform defect the check exists for)", want: InvBackfillStored,
			because: "0 stored",
			mutate: func(d *day) {
				for i := range d.backfill {
					d.backfill[i].Stored = []time.Time{at(21.3)}
				}
			},
		},
		{
			name: "the correlation control fires", defect: "(the verifier's own control)", want: InvCorrelationControl,
			because: "cannot fail",
			mutate:  func(d *day) { d.control = d.corr },
		},
		{
			name: "the correlation fires on every quay", defect: "(none)", want: InvCorrelationFires,
			because: "outside the expected set",
			mutate: func(d *day) {
				d.corr.Firings = append(d.corr.Firings, Firing{At: at(19), Series: sim.HarborpulseBasin, Signal: "raised"})
			},
		},
	}
	for _, c := range controls {
		t.Run(c.name, func(t *testing.T) {
			if want, ok := Controls[c.want]; ok && !strings.HasPrefix(c.defect, "(") && want != c.defect {
				t.Fatalf("Controls[%s] = %q, this test plants %q", c.want, want, c.defect)
			}
			d := cleanDay()
			c.mutate(d)
			invs := Evaluate(d.observe())
			for _, inv := range invs {
				switch {
				case inv.Name == c.want && inv.Passed:
					t.Fatalf("%s stayed green with the defect planted: it cannot fail", inv.Name)
				case inv.Name == c.want && !strings.Contains(inv.Detail, c.because):
					t.Fatalf("%s is red for another reason: %s", inv.Name, inv.Detail)
				case inv.Name != c.want && !inv.Passed:
					t.Fatalf("the control for %s also turned %s red (%s): it is not a control of one thing", c.want, inv.Name, inv.Detail)
				}
			}
		})
	}
}

// The count-above-the-quay control the verifier runs in the same day: the preview it gets back
// (no firing at all) must be judged red by the invariant it controls.
func TestCorrelationJudgesANonFiringRed(t *testing.T) {
	if err := correlationFires(Preview{OK: true}); err == nil {
		t.Fatal("a preview with no firing was judged green")
	}
	if err := correlationFires(Preview{OK: true, Degraded: "the scan cap was reached", Firings: cleanDay().corr.Firings}); err == nil {
		t.Fatal("a partial preview was judged green")
	}
}

func TestEveryNamedDefectIsOneTheSimulatorKnows(t *testing.T) {
	known := map[string]bool{"no-command-far-end": true}
	for _, d := range sim.HarborpulseDefects() {
		known[d] = true
	}
	for inv, ctl := range Controls {
		if inv == InvCorrelationFires || inv == InvCorrelationControl {
			continue
		}
		if !known[ctl] {
			t.Errorf("%s's control %q is not a defect the simulator can plant", inv, ctl)
		}
	}
}

func TestCoverageRefusesAGapATierCouldHideIn(t *testing.T) {
	o := cleanDay().observe()
	var holed []time.Time
	for _, p := range o.Polls {
		if p.Before(at(18)) || p.After(at(18).Add(30*time.Second)) {
			holed = append(holed, p)
		}
	}
	o.Polls = holed
	if err := Coverage(o); err == nil {
		t.Fatal("a 30 s hole in the polls was accepted")
	}
	o = cleanDay().observe()
	o.Polls = o.Polls[:len(o.Polls)/2]
	if err := Coverage(o); err == nil {
		t.Fatal("a watch that stopped half way was accepted")
	}
}

func TestClockMapsSimTimeToWallTime(t *testing.T) {
	if got := testClock.At(0, 6); !got.Equal(testClock.StartedAt) {
		t.Fatalf("06:00 on day 0 is %s, want the first tick", got)
	}
	if got, want := testClock.At(0, 18), testClock.StartedAt.Add(12*time.Minute); !got.Equal(want) {
		t.Fatalf("18:00 on day 0 is %s, want %s", got, want)
	}
	if got, want := testClock.At(1, 6), testClock.StartedAt.Add(24*time.Minute); !got.Equal(want) {
		t.Fatalf("06:00 on day 1 is %s, want %s", got, want)
	}
	if d := testClock.TargetDay(testClock.StartedAt.Add(time.Minute), WatchFrom); d != 0 {
		t.Fatalf("a minute in, the target day is %d, want 0", d)
	}
	if d := testClock.TargetDay(testClock.At(0, 14), WatchFrom); d != 1 {
		t.Fatalf("at 14:00 the target day is %d, want 1", d)
	}
}

func TestClockFromRefusesAnUnstartedDay(t *testing.T) {
	if _, err := ClockFrom(nil); err == nil {
		t.Fatal("no scenario block was accepted")
	}
	if _, err := ClockFrom(&sim.ScenarioClock{DayLengthSeconds: 1440}); err == nil {
		t.Fatal("an unstarted day was accepted")
	}
	c, err := ClockFrom(&sim.ScenarioClock{Started: true, StartedAt: "2026-11-16T12:00:00.5Z", DayLengthSeconds: 1440, StartHour: 6})
	if err != nil || c.DayLength != 24*time.Minute || c.StartedAt.Nanosecond() != 5e8 {
		t.Fatalf("ClockFrom = %+v, %v", c, err)
	}
}

func TestReportExitCodes(t *testing.T) {
	pass := &Report{Invariants: []Invariant{{Name: "x", Passed: true}}}
	fail := &Report{Invariants: []Invariant{{Name: "x", Passed: true}, {Name: "y"}}}
	none := &Report{NoVerdict: "the day never started"}
	for _, c := range []struct {
		r    *Report
		want int
	}{{pass, ExitPass}, {fail, ExitFail}, {none, ExitNoVerdict}} {
		if got := c.r.ExitCode(); got != c.want {
			t.Errorf("exit %d, want %d for %s", got, c.want, c.r.Human())
		}
	}
}

// The control the verifier previews must differ from the published rule ONLY in its count, and
// the published rule must be what the helper builds at the scenario's own count.
func TestQuayFloodingControlIsThePublishedRuleWithAnotherCount(t *testing.T) {
	published, err := manifestRuleDefinition(sim.HarborpulseGaugeProfile, sim.HarborpulseQuayFloodingRule)
	if err != nil {
		t.Fatal(err)
	}
	if got := sim.HarborpulseQuayFloodingDefinition(sim.HarborpulseQuayFloodingN); got != published {
		t.Fatalf("the helper builds\n%s\nthe manifest publishes\n%s", got, published)
	}
	control := sim.HarborpulseQuayFloodingDefinition(4)
	if strings.Replace(control, `"count":4`, fmt.Sprintf(`"count":%d`, sim.HarborpulseQuayFloodingN), 1) != published {
		t.Fatalf("the control differs from the published rule in more than its count:\n%s\n%s", control, published)
	}
}
