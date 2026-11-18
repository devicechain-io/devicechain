// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
)

// mailDeviceLines are the "Device:" and "Device token:" fields device-management fills only
// when its best-effort device lookup answered.
var mailDeviceLines = regexp.MustCompile(`(?m)^Device(?: token)?:.*\r\n`)

// A duty mail whose device lookup timed out names no device, and its subject names the alarm's
// originator instead. Every gauge's flood alarm shares one key, so only the originator can
// attribute it.
func TestFloodMailOriginatorFallback(t *testing.T) {
	const gauge = "hp-gauge-basin-1"
	const other = AckGauge
	for _, tc := range []struct {
		name      string
		ambiguous bool // a second gauge's alarm carries the same originator
		unmatched bool // no observed alarm carries the mail's originator
		device    string
	}{
		{name: "unique originator"},
		{name: "ambiguous originator", ambiguous: true},
		{name: "unmatched originator", unmatched: true},
		{name: "device token takes precedence", device: other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := cleanDay().observe()
			ids := map[string]int{}
			for i := range o.Alarms {
				a := &o.Alarms[i]
				if _, ok := ids[a.Device]; !ok {
					ids[a.Device] = len(ids) + 100
				}
				a.Originator = fmt.Sprintf("device %d", ids[a.Device])
			}
			if tc.ambiguous {
				for i := range o.Alarms {
					if o.Alarms[i].Device == other {
						o.Alarms[i].Originator = fmt.Sprintf("device %d", ids[gauge])
					}
				}
			}
			source := fmt.Sprintf("device %d", ids[gauge])
			if tc.unmatched {
				source = "device 9999"
			}
			rewritten := 0
			for i := range o.S3.Sink.Mail {
				m := &o.S3.Sink.Mail[i]
				device, alarm, trigger, ok := dutyMail(m.Data)
				if !ok || device != gauge || alarm != sim.HarborpulseFloodAlarmKey {
					continue
				}
				m.Data = mailDeviceLines.ReplaceAllString(dutyMailData(sim.HarborpulseDutyRecipient(testTenant), source, alarm, trigger), "")
				if tc.device != "" {
					m.Data += "Device token: " + tc.device + "\r\n"
				}
				rewritten++
			}
			if rewritten != 2 {
				t.Fatalf("fixture rewrote %d mails, want the page and the escalation", rewritten)
			}
			mail, unattributed := floodMail(o, sim.HarborpulseDutyRecipient(testTenant))
			wantCount, wantUnattributed := 1, 0
			if tc.ambiguous || tc.unmatched || tc.device != "" {
				wantCount = 0
			}
			if tc.ambiguous || tc.unmatched {
				wantUnattributed = 2
			}
			for _, trigger := range []string{"ALARM", "ESCALATION"} {
				if got := mail[gauge][trigger]; got != wantCount {
					t.Errorf("%s count = %d, want %d", trigger, got, wantCount)
				}
			}
			if unattributed != wantUnattributed {
				t.Errorf("unattributed = %d, want %d", unattributed, wantUnattributed)
			}
			if tc.device != "" && (mail[other]["ALARM"] != 2 || mail[other]["ESCALATION"] != 1) {
				t.Errorf("device attribution lost or counted twice: %v", mail[other])
			}
			err := notifyEscalates(o)
			if wantCount == 1 {
				if err != nil {
					t.Fatalf("a mail naming its gauge's originator was not attributed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), gauge+": the platform recorded") {
				t.Fatalf("missing gauge mail should be red: %v", err)
			}
			if wantUnattributed > 0 && !strings.Contains(err.Error(), "2 mails to ") {
				t.Fatalf("unattributed count missing: %v", err)
			}
			if err := notifyAckControlIsRed(o); err != nil {
				t.Fatalf("acknowledgement control changed: %v", err)
			}
			o.S3.Attempts[o.S3.Ack.Token] = append(o.S3.Attempts[o.S3.Ack.Token], Attempt{
				At: at(19.5), Trigger: "ESCALATION", Status: "SENT",
			})
			if notifyAckControlIsRed(o) == nil {
				t.Fatal("acknowledgement control accepted an escalation")
			}
		})
	}
}

func TestMailSourceDecodesTheSubject(t *testing.T) {
	data := "From: a\r\nSubject: =?utf-8?q?[MAJOR]_Alarm_raised:_flood-level_on_device_7?=\r\n\r\nbody"
	if key, source := mailSource(data); key != "flood-level" || source != "device 7" {
		t.Fatalf("mailSource = %q, %q", key, source)
	}
	if key, source := mailSource("Subject: unrelated\r\n"); key != "" || source != "" {
		t.Fatalf("an unrelated subject named %q, %q", key, source)
	}
}
