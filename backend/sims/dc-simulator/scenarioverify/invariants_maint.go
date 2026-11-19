// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	InvMaintenanceHoldsPage    = "maintenance-holds-page"
	InvMaintenanceControlPages = "maintenance-control-pages"
	// Two default 60-second scheduler passes plus margin for release and API/SMTP work.
	releaseWithin = 150 * time.Second
	promptWithin  = 30 * time.Second
)

// Maintenance contains platform read-backs; the ledger only identifies the judged day's window.
type Maintenance struct {
	Window *AlarmWindow
	Lamps  map[string]*LampCycle
}

type AlarmWindow struct {
	Token   string              `json:"token"`
	Start   time.Time           `json:"startTime"`
	End     time.Time           `json:"endTime"`
	Targets []AlarmWindowTarget `json:"targets"`
}

type AlarmWindowTarget struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// LampCycle is the CURRENT cycle of a reused alarm row, read after 13:00 of the judged day.
type LampCycle struct {
	Token          string    `json:"token"`
	Device         string    `json:"originatorToken"`
	Key            string    `json:"alarmKey"`
	State          string    `json:"state"`
	Raised         time.Time `json:"raisedTime"`
	Stamp          string    `json:"raisedDuringSuppression"`
	OriginatorType string    `json:"originatorType"`
	OriginatorID   string    `json:"originatorId"`
}

func init() {
	Controls[InvMaintenanceHoldsPage] = sim.HarborpulseDefectSuppressionWrongDevice
	Controls[InvMaintenanceControlPages] = "none: it IS the control of " + InvMaintenanceHoldsPage + " (an unwindowed North Quay lamp with the same flicker)"
}

func maintenanceHoldsPage(o Observations) error { return lampHeld(o, sim.HarborpulseMaintenanceLamp) }

func maintenanceSpan(o Observations) (time.Time, time.Time) {
	return o.at(sim.HarborpulseLampServiceFrom), o.at(sim.HarborpulseLampServiceTo)
}

func lampCycleProblems(o Observations, device string) (*LampCycle, []string) {
	a := o.Maint.Lamps[device]
	if a == nil {
		return nil, []string{device + ": no lamp-flicker alarm cycle"}
	}
	var problems []string
	opens, closes := maintenanceSpan(o)
	if a.Token == "" || a.Device != device || a.Key != sim.HarborpulseFlickerAlarmKey {
		problems = append(problems, device+": returned a different or unnamed alarm")
	}
	if a.Raised.Before(opens) || !a.Raised.Before(closes) {
		problems = append(problems, fmt.Sprintf("%s: current cycle raisedTime %s is outside the judged day's 10:45-11:30 [%s, %s)", device, a.Raised.Format(time.RFC3339Nano), opens.Format(time.RFC3339Nano), closes.Format(time.RFC3339Nano)))
	}
	return a, problems
}

// lampHeld is deliberately the SAME hold check for the windowed lamp and its positive control.
// Attempts are scoped to this cycle/day: another day's release on the reused token proves nothing.
func lampHeld(o Observations, device string) error {
	var problems []string
	w := o.Maint.Window
	opens, closes := maintenanceSpan(o)
	if w == nil {
		problems = append(problems, "no platform suppression window for the judged day's lamp-service job")
	} else {
		// PostgreSQL stores microseconds; the sim's wall-clock ledger retains nanoseconds.
		if w.Token == "" || !w.Start.Truncate(time.Microsecond).Equal(opens.Truncate(time.Microsecond)) || !w.End.Truncate(time.Microsecond).Equal(closes.Truncate(time.Microsecond)) {
			problems = append(problems, "platform window does not have the exact judged day's 10:45-11:30 instants")
		}
		if len(w.Targets) != 1 || w.Targets[0].Type != "DEVICE" || w.Targets[0].Token != device {
			problems = append(problems, device+": window does not target exactly this DEVICE")
		}
	}
	a, cycleProblems := lampCycleProblems(o, device)
	problems = append(problems, cycleProblems...)
	if a != nil {
		if w == nil || a.Stamp == "" || a.Stamp != w.Token {
			problems = append(problems, device+": raisedDuringSuppression is not the window token")
		}
		released := false
		for _, attempt := range o.S3.Attempts[a.Token] {
			if attempt.Status != "SENT" {
				continue
			}
			if !attempt.At.Before(a.Raised) && attempt.At.Before(closes.Add(-windowEarly)) {
				problems = append(problems, fmt.Sprintf("%s: early page SENT %s at %s before the window closed", device, attempt.Trigger, attempt.At.Format(time.RFC3339Nano)))
			}
			if attempt.Trigger == "QUIET_HOURS_RELEASE" && !attempt.At.Before(a.Raised) && inWindow(attempt.At, closes.Add(-windowEarly), closes.Add(releaseWithin)) {
				released = true
			}
		}
		if !released {
			problems = append(problems, device+": no SENT QUIET_HOURS_RELEASE within 150s of window close (two default 60s scheduler passes plus margin)")
		}
		if o.S3.Sink != nil {
			after := false
			for _, m := range o.S3.Sink.Mail {
				if !sliceHas(m.To, sim.HarborpulseDutyRecipient(o.S3.Tenant)) || m.At.Before(a.Raised) || m.At.After(o.end()) {
					continue
				}
				d, key, _, ok := dutyMail(m.Data)
				// Reuse the duty-mail parser and its subject-originator attribution for a missing token field.
				if !ok && mailDeviceField.FindStringSubmatch(m.Data) == nil {
					subjectKey, source := mailSource(m.Data)
					if subjectKey == key && source == a.OriginatorType+" "+a.OriginatorID && a.OriginatorID != "" {
						d, ok = device, true
					}
				}
				if !ok || d != device || key != sim.HarborpulseFlickerAlarmKey {
					continue
				}
				if m.At.Before(closes.Add(-windowEarly)) {
					problems = append(problems, device+": early duty mail before the window closed")
				} else {
					after = true
				}
			}
			if !after {
				problems = append(problems, device+": no duty mail after the window closed")
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func maintenanceControlPages(o Observations) error {
	return maintenanceControlWithHold(o, lampHeld)
}

// The injected check lets a negative test prove the control rejects a green hold verdict even
// when the independent prompt-page checks pass, as notify-ack-control proves its own judge red.
func maintenanceControlWithHold(o Observations, hold func(Observations, string) error) error {
	device := sim.HarborpulseControlLamp
	a, problems := lampCycleProblems(o, device)
	_, closes := maintenanceSpan(o)
	if a != nil {
		if a.Stamp != "" {
			problems = append(problems, device+": unwindowed control was also held (raisedDuringSuppression="+a.Stamp+")")
		}
		prompt := false
		for _, attempt := range o.S3.Attempts[a.Token] {
			if attempt.Status == "SENT" && attempt.Trigger == "ALARM" && inWindow(attempt.At, a.Raised, a.Raised.Add(promptWithin)) && attempt.At.Before(closes) {
				prompt = true
			}
		}
		if !prompt {
			problems = append(problems, device+": no SENT ALARM within 30s of raisedTime and before window close")
		}
	}
	if hold(o, device) == nil {
		problems = append(problems, device+": hold check was green for the unwindowed control; "+InvMaintenanceHoldsPage+" cannot fail")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}
