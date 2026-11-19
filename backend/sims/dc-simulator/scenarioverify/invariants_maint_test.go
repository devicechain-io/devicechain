// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const testLampWindow = "hp-lamp-service-1-d0"

// cleanMaintenance models both current cycles and ALL their pages while still active, including
// the escalation after release. The same duty policy pages both lamps at MAJOR, max one escalation.
func (d *day) cleanMaintenance() {
	opens, closes := at(sim.HarborpulseLampServiceFrom), at(sim.HarborpulseLampServiceTo)
	d.s4.Jobs = append(d.s4.Jobs, sim.ScenarioJob{Day: 0, Name: sim.HarborpulseJobLampService,
		Window: testLampWindow, Device: sim.HarborpulseMaintenanceLamp,
		WindowOpens: opens.Format(time.RFC3339Nano), WindowCloses: closes.Format(time.RFC3339Nano)})
	d.maint = Maintenance{Window: &AlarmWindow{Token: testLampWindow, Start: opens, End: closes,
		Targets: []AlarmWindowTarget{{Type: "DEVICE", Token: sim.HarborpulseMaintenanceLamp}}}, Lamps: map[string]*LampCycle{}}
	for _, device := range []string{sim.HarborpulseMaintenanceLamp, sim.HarborpulseControlLamp} {
		token := "f-" + device
		raised := at(11).Add(10 * time.Second)
		stamp := ""
		first, trigger := raised.Add(3*time.Second), "ALARM"
		if device == sim.HarborpulseMaintenanceLamp {
			stamp, first, trigger = testLampWindow, closes.Add(25*time.Second), "QUIET_HOURS_RELEASE"
		}
		d.maint.Lamps[device] = &LampCycle{Token: token, Device: device, Key: sim.HarborpulseFlickerAlarmKey,
			State: sim.AlarmStateClearedWire, Raised: raised, Stamp: stamp}
		d.alarms = append(d.alarms, lifecycle{token, device, sim.HarborpulseFlickerAlarmKey, []step{{11 + 10.0/60, major}}, 12 + 50.0/60})
		d.lampPages(device, first, trigger)
	}
}

func (d *day) lampPages(device string, first time.Time, trigger string) {
	a := d.maint.Lamps[device]
	d.s3.Attempts[a.Token] = []Attempt{
		{At: first, AlarmToken: a.Token, Trigger: trigger, Status: "SENT", Channel: sim.HarborpulseDutyChannel},
		{At: first.Add(45 * time.Second), AlarmToken: a.Token, Trigger: "ESCALATION", Status: "SENT", Channel: sim.HarborpulseDutyChannel},
	}
	if d.s3.Sink == nil {
		return
	}
	var kept = d.s3.Sink.Mail[:0]
	for _, m := range d.s3.Sink.Mail {
		dev, key, _, _ := dutyMail(m.Data)
		if dev != device || key != sim.HarborpulseFlickerAlarmKey {
			kept = append(kept, m)
		}
	}
	d.s3.Sink.Mail = kept
	for _, attempt := range d.s3.Attempts[a.Token] {
		d.s3.Sink.Mail = append(d.s3.Sink.Mail, dutyMailAt(attempt.At.Add(time.Second), sim.HarborpulseDutyRecipient(d.s3.Tenant), device, sim.HarborpulseFlickerAlarmKey, attempt.Trigger))
	}
}

// The live defect holds east-2 (which never flickers), so east-3 pages promptly, escalates,
// carries no cycle stamp and has no release attempt. Every other observation remains clean.
func wrongMaintenanceDevice(d *day) {
	d.maint.Window.Targets[0].Token = "hp-light-east-2"
	for i := range d.s4.Jobs {
		if d.s4.Jobs[i].Name == sim.HarborpulseJobLampService {
			d.s4.Jobs[i].Device = "hp-light-east-2"
		}
	}
	a := d.maint.Lamps[sim.HarborpulseMaintenanceLamp]
	a.Stamp = ""
	d.lampPages(sim.HarborpulseMaintenanceLamp, a.Raised.Add(3*time.Second), "ALARM")
}

func TestMaintenanceNegativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, because string
		mutate        func(*day)
	}{
		{"late release", "no SENT QUIET_HOURS_RELEASE within 150s", func(d *day) {
			d.lampPages(sim.HarborpulseMaintenanceLamp, d.maint.Window.End.Add(200*time.Second), "QUIET_HOURS_RELEASE")
		}},
		{"early SENT", "early page", func(d *day) {
			a := d.maint.Lamps[sim.HarborpulseMaintenanceLamp]
			d.s3.Attempts[a.Token] = append(d.s3.Attempts[a.Token], Attempt{At: d.maint.Window.End.Add(-10 * time.Second), Status: "SENT", Trigger: "ALARM"})
		}},
		{"cleared before release", "no SENT QUIET_HOURS_RELEASE", func(d *day) {
			a := d.maint.Lamps[sim.HarborpulseMaintenanceLamp]
			d.s3.Attempts[a.Token] = nil
			// A resolved hold sends no mail either; keep it here so the missing-release check itself
			// must reject the evidence (M2), independently of the optional sink check.
			for i := range d.alarms {
				if d.alarms[i].token == a.Token {
					d.alarms[i].clear = 11.4
				}
			}
			d.s3.Sink = nil
		}},
		{"absent window", "no platform suppression window", func(d *day) { d.maint.Window = nil }},
		{"previous cycle", "current cycle raisedTime", func(d *day) { d.maint.Lamps[sim.HarborpulseMaintenanceLamp].Raised = at(11).Add(-24 * time.Minute) }},
		{"early mail", "early duty mail", func(d *day) {
			d.s3.Sink.Mail = append(d.s3.Sink.Mail, dutyMailAt(d.maint.Window.End.Add(-10*time.Second), sim.HarborpulseDutyRecipient(d.s3.Tenant), sim.HarborpulseMaintenanceLamp, sim.HarborpulseFlickerAlarmKey, "ALARM"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { evaluateOneRed(t, InvMaintenanceHoldsPage, tc.because, tc.mutate) })
	}
}

func TestMaintenanceControlRejectsHeldNorth(t *testing.T) {
	evaluateOneRed(t, InvMaintenanceControlPages, "unwindowed control was also held", func(d *day) {
		a := d.maint.Lamps[sim.HarborpulseControlLamp]
		a.Stamp = testLampWindow
		d.lampPages(sim.HarborpulseControlLamp, d.maint.Window.End.Add(25*time.Second), "QUIET_HOURS_RELEASE")
	})
}

func TestMaintenanceControlRequiresRedHoldCheck(t *testing.T) {
	// Directly exercise the cannot-fail guard: prompt/no-stamp evidence stays valid while a
	// faulty hold judge returns green. A real held North cycle also fails the test above.
	err := maintenanceControlWithHold(cleanDay().observe(), func(Observations, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot fail") {
		t.Fatalf("green hold judge accepted by control: %v", err)
	}
}

func TestMaintenanceStillJudgedWithoutSink(t *testing.T) {
	d := cleanDay()
	d.s3.Sink = nil
	for _, inv := range Evaluate(d.observe()) {
		if (inv.Name == InvMaintenanceHoldsPage || inv.Name == InvMaintenanceControlPages) && !inv.Passed {
			t.Fatal(inv)
		}
	}
	wrongMaintenanceDevice(d)
	if maintenanceHoldsPage(d.observe()) == nil {
		t.Fatal("platform attempts must reject early page without sink")
	}
}
