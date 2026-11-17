// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-simulator/scenariosink"
	"github.com/devicechain-io/dc-simulator/sim"
)

const testTenant = "hprig-clean"

// Two valid secrets, fixed so the tests are reproducible: the one the verifier rotated in, and the
// control's, which the platform never had.
const (
	testSecret      = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	testWrongSecret = "whsec_dGhlIGNvbnRyb2wncyBvd24gc2VjcmV0IGtleQ=="
)

func signed(t time.Time, path, body string, secrets ...string) scenariosink.Webhook {
	w := scenariosink.Webhook{At: t, Path: path, Body: body, Headers: map[string][]string{"Content-Type": {"application/json"}}}
	if len(secrets) > 0 {
		id := fmt.Sprintf("msg-%d", t.UnixNano())
		sig, err := webhooksig.Sign(id, t, []byte(body), secrets...)
		if err != nil {
			panic(err)
		}
		w.Headers["Webhook-Id"] = []string{id}
		w.Headers["Webhook-Timestamp"] = []string{webhooksig.Timestamp(t)}
		w.Headers["Webhook-Signature"] = []string{sig}
	}
	return w
}

// dutyMailData is the DATA of the mail notification-management sends the duty officer for one
// page, in its default rendering: an ALARM names the transition on its first line, an ESCALATION
// says the alarm is still unacknowledged; both carry the device in a "Device token:" field.
func dutyMailData(recipient, device, key, trigger string) string {
	field := func(label, value string) string { return fmt.Sprintf("%-18s %s\r\n", label+":", value) }
	var subject, body string
	switch trigger {
	case "ESCALATION":
		subject = fmt.Sprintf("[MAJOR] Alarm still unresolved (escalation 1): %s on %s", key, device)
		body = fmt.Sprintf("Alarm %s is still unacknowledged and uncleared after 1 notification(s).\r\n\r\n", key) +
			field("Device", device) + field("Device token", device) + field("Severity", "MAJOR") + field("Escalation", "1")
	default:
		subject = fmt.Sprintf("[MAJOR] Alarm raised: %s on %s", key, device)
		body = fmt.Sprintf("Alarm %s raised on %s.\r\n\r\n", key, device) +
			field("Device", device) + field("Device token", device) + field("Severity", "MAJOR") + field("State", "ACTIVE")
	}
	return "From: alerts@devicechain.example\r\nTo: " + recipient + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
}

func dutyMailAt(t time.Time, recipient, device, key, trigger string) scenariosink.Mail {
	return scenariosink.Mail{At: t, To: []string{recipient}, Data: dutyMailData(recipient, device, key, trigger)}
}

// cleanS3 is the S3 beats as a working platform answers them.
func (d *day) cleanS3() {
	s := S3{Tenant: testTenant, Seed: 1, SigningSecret: testSecret, WrongSecret: testWrongSecret,
		Attempts: map[string][]Attempt{}, Health: map[string]DecoderHealth{}, DecodeFailures: map[string][]DecodeFailure{},
		Sink: &scenariosink.Received{}}
	recipient := sim.HarborpulseDutyRecipient(testTenant)
	for _, g := range fleetOf(sim.HarborpulseGaugeType, "") {
		token := "a-" + g
		s.Sink.Messages = append(s.Sink.Messages, scenariosink.Message{At: at(18.0), Topic: sim.HarborpulseBulletinTopic(testTenant),
			QoS: 1, Payload: `{"device":"` + g + `"}`})
		s.Attempts[token] = append(s.Attempts[token], Attempt{At: at(18.0), AlarmToken: token, Trigger: "ALARM", Status: "SENT"})
		s.Sink.Mail = append(s.Sink.Mail, dutyMailAt(at(18.0), recipient, g, sim.HarborpulseFloodAlarmKey, "ALARM"))
		if g == AckGauge {
			s.Ack = Ack{Device: g, Token: token, At: at(18.1)}
			continue
		}
		s.Attempts[token] = append(s.Attempts[token], Attempt{At: at(19.5), AlarmToken: token, Trigger: "ESCALATION", Status: "SENT"})
		s.Sink.Mail = append(s.Sink.Mail, dutyMailAt(at(19.5), recipient, g, sim.HarborpulseFloodAlarmKey, "ESCALATION"))
	}
	// Mail to the same recipient about other alarms: the pumps' wet wells, a gauge's surge. It
	// stands in for none of the flood pages.
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		s.Sink.Mail = append(s.Sink.Mail, dutyMailAt(at(17.3), recipient, p, sim.HarborpulseWetWellAlarmKey, "ALARM"),
			dutyMailAt(at(17.9), recipient, p, sim.HarborpulseWetWellAlarmKey, "ESCALATION"))
	}
	s.Sink.Mail = append(s.Sink.Mail, dutyMailAt(at(19.6), recipient, "hp-gauge-basin-1", sim.HarborpulseSurgeAlarmKey, "ESCALATION"))
	for _, g := range fleetOf(sim.HarborpulseGaugeType, sim.HarborpulseOutageQuay) {
		s.Sink.Webhooks = append(s.Sink.Webhooks, signed(at(18.3), sim.HarborpulseWebhookPath, `{"device":"`+g+`"}`, testSecret))
	}
	// Another tenant's deliveries reach the same sink and verify under none of this one's keys.
	s.Sink.Webhooks = append(s.Sink.Webhooks, signed(at(18.3), sim.HarborpulseWebhookPath, `{"device":"hp-gauge-north-1"}`,
		"whsec_YW5vdGhlciB0ZW5hbnQncyBzZWNyZXQga2V5IQ=="))
	s.Sink.Messages = append(s.Sink.Messages, scenariosink.Message{At: at(18.0), Topic: sim.HarborpulseBulletinTopic("other"), Payload: `{"device":"hp-truck-1"}`})

	d.alarms = append(d.alarms, lifecycle{"f-truck", "hp-truck-1", sim.HarborpulseTruckFenceAlarmKey,
		[]step{{17.8, sim.AlarmSeverityWarningWire}}, 21.8})

	// Each pump's calculated fill beside every wet well reading it stored (the air-locked pump's
	// well is not otherwise modelled here, so it gets a steady one).
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		var fills []Reading
		for _, r := range d.readings[p] {
			if r.Name == sim.HarborpulseWetWellKey {
				fills = append(fills, Reading{Name: sim.HarborpulseWellFillKey, At: r.At, Value: r.Value / sim.HarborpulseWellDepthM * 100})
			}
		}
		if len(fills) == 0 {
			for t := at(16.1); t.Before(at(21.9)); t = t.Add(5 * time.Second) {
				d.readings[p] = append(d.readings[p], Reading{Name: sim.HarborpulseWetWellKey, At: t, Value: 1.5})
				fills = append(fills, Reading{Name: sim.HarborpulseWellFillKey, At: t, Value: 37.5})
			}
		}
		d.readings[p] = append(d.readings[p], fills...)
	}
	for _, g := range append(sim.HarborpulseLoraGauges(), sim.HarborpulseLegacyGauge) {
		for t, n := at(WatchFrom).Add(time.Second), 0; t.Before(at(WatchTo)); t, n = t.Add(5*time.Second), n+1 {
			if g == sim.HarborpulseLegacyGauge && n%2 == 1 {
				s.DecodeFailures[g] = append(s.DecodeFailures[g], DecodeFailure{ID: fmt.Sprintf("df-%d", n), At: t, Device: g,
					Error: "eval_error at sections[1].outputs[1].expr: u16be: offset 5 is out of range"})
				continue
			}
			v, _ := sim.HarborpulseLoraLevelAt(1, g, t.Sub(testClock.StartedAt), testClock.DayLength)
			d.readings[g] = append(d.readings[g], Reading{Name: sim.HarborpulseWaterLevelKey, At: t, Value: v})
		}
	}
	s.Health[sim.HarborpulseLoraGaugeProfile] = DecoderHealth{Decoded: 360}
	s.Health[sim.HarborpulseLegacyGaugeProfile] = DecoderHealth{Decoded: 60, Failed: 60, LastError: "eval_error"}
	d.s3 = s
}

// evaluateOneRed applies mutate to a clean day and asserts that exactly want turns red, because of
// a detail containing because.
func evaluateOneRed(t *testing.T, want, because string, mutate func(*day)) {
	t.Helper()
	d := cleanDay()
	mutate(d)
	for _, inv := range Evaluate(d.observe()) {
		switch {
		case inv.Name == want && inv.Passed:
			t.Fatalf("%s stayed green with the defect planted: it cannot fail", inv.Name)
		case inv.Name == want && !strings.Contains(inv.Detail, because):
			t.Fatalf("%s is red for another reason: %s", inv.Name, inv.Detail)
		case inv.Name != want && !inv.Passed:
			t.Fatalf("the control for %s also turned %s red (%s): it is not a control of one thing", want, inv.Name, inv.Detail)
		}
	}
}

func TestEachS3ControlTurnsExactlyItsInvariantRed(t *testing.T) {
	controls := []struct {
		name, defect, want, because string
		mutate                      func(*day)
	}{
		{"the flood webhook unsigned", sim.HarborpulseDefectUnsignedWebhook, InvWebhookSigned, "never verified for", func(d *day) {
			for i, w := range d.s3.Sink.Webhooks {
				d.s3.Sink.Webhooks[i] = signed(w.At, w.Path, w.Body)
			}
		}},
		{"the flood webhook signed with another key", "(none: a platform signing with a stale key)", InvWebhookSigned, "never verified for", func(d *day) {
			for i, w := range d.s3.Sink.Webhooks {
				d.s3.Sink.Webhooks[i] = signed(w.At, w.Path, w.Body, "whsec_c3RhbGUga2V5IHN0YWxlIGtleSBzdGFsZSBrZXk=")
			}
		}},
		{"a body tampered after signing", "(none)", InvWebhookSigned, "never verified for", func(d *day) {
			d.s3.Sink.Webhooks[0].Body = strings.Replace(d.s3.Sink.Webhooks[0].Body, "}", " }", 1)
		}},
		{"the control's secret verifies too", "(the verifier's own control)", InvWebhookControl, "never had", func(d *day) {
			for i, w := range d.s3.Sink.Webhooks[:3] {
				d.s3.Sink.Webhooks[i] = signed(w.At, w.Path, w.Body, testSecret, testWrongSecret)
			}
		}},
		{"the bulletin connector unreachable", sim.HarborpulseDefectMQTTUnreachable, InvMQTTBulletin, "never published for", func(d *day) {
			d.s3.Sink.Messages = nil
		}},
		{"a gauge never escalated", "(none)", InvNotifyEscalates, "never escalated", func(d *day) {
			tok := "a-hp-gauge-basin-1"
			d.s3.Attempts[tok] = d.s3.Attempts[tok][:1]
		}},
		{"pages the sink never received", "(none)", InvNotifyEscalates, "the sink received", func(d *day) {
			d.s3.Sink.Mail = d.s3.Sink.Mail[:3]
		}},
		// One gauge's escalation mail lost, every other mail kept: the acknowledged gauge's page and
		// the other alarms' mail to the same recipient must not make up for it.
		{"one gauge's escalation mail dropped", "(none: a platform that loses one page)", InvNotifyEscalates,
			"hp-gauge-basin-1: the platform recorded 1 ESCALATION pages SENT for its flood alarm but the sink received 0", func(d *day) {
				var kept []scenariosink.Mail
				for _, m := range d.s3.Sink.Mail {
					if dev, key, trig, _ := dutyMail(m.Data); dev == "hp-gauge-basin-1" && key == sim.HarborpulseFloodAlarmKey && trig == "ESCALATION" {
						continue
					}
					kept = append(kept, m)
				}
				if len(kept) != len(d.s3.Sink.Mail)-1 {
					panic(fmt.Sprintf("dropped %d mails, want exactly one", len(d.s3.Sink.Mail)-len(kept)))
				}
				d.s3.Sink.Mail = kept
			}},
		{"one gauge's escalation mailed as another trigger", "(none)", InvNotifyEscalates,
			"hp-gauge-basin-2: the platform recorded 1 ESCALATION", func(d *day) {
				for i, m := range d.s3.Sink.Mail {
					if dev, key, trig, _ := dutyMail(m.Data); dev == "hp-gauge-basin-2" && key == sim.HarborpulseFloodAlarmKey && trig == "ESCALATION" {
						d.s3.Sink.Mail[i] = dutyMailAt(m.At, sim.HarborpulseDutyRecipient(testTenant), dev, key, "ALARM")
					}
				}
			}},
		{"the acknowledged gauge escalated anyway", "(the verifier's own control)", InvNotifyAckControl, "although it was acknowledged", func(d *day) {
			tok := "a-" + AckGauge
			d.s3.Attempts[tok] = append(d.s3.Attempts[tok], Attempt{At: at(19.5), AlarmToken: tok, Trigger: "ESCALATION", Status: "SENT"})
		}},
		{"the truck never leaves the depot", sim.HarborpulseDefectTruckStaysHome, InvTruckFence, "never raised for", func(d *day) {
			var kept []lifecycle
			for _, l := range d.alarms {
				if l.key != sim.HarborpulseTruckFenceAlarmKey {
					kept = append(kept, l)
				}
			}
			d.alarms = kept
		}},
		{"the fence alarm never clears", "(none)", InvTruckFence, "still active", func(d *day) {
			for i, l := range d.alarms {
				if l.key == sim.HarborpulseTruckFenceAlarmKey {
					d.alarms[i].clear = 30
				}
			}
		}},
		{"the pumps carry the wrong well depth", sim.HarborpulseDefectWrongWellDepth, InvWellFill, "not level over", func(d *day) {
			for p, rs := range d.readings {
				for i, r := range rs {
					if r.Name == sim.HarborpulseWellFillKey {
						d.readings[p][i].Value = r.Value * sim.HarborpulseWellDepthM / 5
					}
				}
			}
		}},
		{"the gauges encode millimetres", sim.HarborpulseDefectLoraWrongScale, InvLoraDecoded, "not what the gauge sent", func(d *day) {
			for _, g := range sim.HarborpulseLoraGauges() {
				for i := range d.readings[g] {
					d.readings[g][i].Value *= 10
				}
			}
		}},
		{"the legacy gauge's decoder fixed", sim.HarborpulseDefectLegacyDecoderFixed, InvDecoderFailureVisible, "want both", func(d *day) {
			d.s3.Health[sim.HarborpulseLegacyGaugeProfile] = DecoderHealth{Decoded: 120}
			d.s3.DecodeFailures[sim.HarborpulseLegacyGauge] = nil
		}},
	}
	for _, c := range controls {
		t.Run(c.name, func(t *testing.T) {
			if want := Controls[c.want]; !strings.HasPrefix(c.defect, "(") && want != c.defect {
				t.Fatalf("Controls[%s] = %q, this test plants %q", c.want, want, c.defect)
			}
			evaluateOneRed(t, c.want, c.because, c.mutate)
		})
	}
}

// Without a sink the five sink invariants are not judged at all, rather than judged green.
func TestWithoutASinkTheSinkInvariantsAreNotJudged(t *testing.T) {
	d := cleanDay()
	d.s3.Sink = nil
	for _, inv := range Evaluate(d.observe()) {
		if sliceHas(SinkInvariants, inv.Name) {
			t.Fatalf("%s was judged without a sink", inv.Name)
		}
		if !inv.Passed {
			t.Fatalf("%s failed without a sink: %s", inv.Name, inv.Detail)
		}
	}
}

// foldedLegacyDay is a clean day whose legacy gauge wrote its ten records of the hour before the
// watch: through the watch the platform only folds its failures into the newest one's suppressed
// count, which grows from atWatch to atEnd.
func foldedLegacyDay(atWatch, atEnd int) *day {
	d := cleanDay()
	g := sim.HarborpulseLegacyGauge
	var start, end []DecodeFailure
	for i := 0; i < 10; i++ {
		f := DecodeFailure{ID: fmt.Sprintf("early-%d", i), At: at(WatchFrom).Add(-time.Duration(10-i) * time.Minute), Device: g,
			Error: "decode_failed: eval_error: sections[1].outputs[1].expr: u16be: reads 2 byte(s) at offset 5 of a 6-byte frame"}
		start, end = append(start, f), append(end, f)
	}
	start[9].Suppressed, end[9].Suppressed = atWatch, atEnd
	d.s3.LegacyFailuresAtWatch, d.s3.DecodeFailures[g] = start, end
	return d
}

// A steadily failing device spends its hourly record bound in minutes, so a watch later in the hour
// sees its failures only as growth in an older record's suppressed count. That growth is the
// watch's failures on record, and the invariant holds on it.
func TestFoldedDecodeFailuresCountThroughTheWatch(t *testing.T) {
	for _, inv := range Evaluate(foldedLegacyDay(98, 160).observe()) {
		if !inv.Passed {
			t.Fatalf("%s failed on a day whose decode failures were folded: %s", inv.Name, inv.Detail)
		}
	}
}

// ...but only growth during the watch counts: a folded count that did not move, or a record the
// watch never saw at its start, shows nothing about this day.
func TestFoldedDecodeFailuresThatDidNotGrowAreRed(t *testing.T) {
	evaluateOneRed(t, InvDecoderFailureVisible, "suppressed count grew", func(d *day) {
		*d = *foldedLegacyDay(98, 98)
	})
	evaluateOneRed(t, InvDecoderFailureVisible, "suppressed count grew", func(d *day) {
		*d = *foldedLegacyDay(98, 160)
		d.s3.LegacyFailuresAtWatch = nil
	})
	evaluateOneRed(t, InvDecoderFailureVisible, "not the evaluation error", func(d *day) {
		*d = *foldedLegacyDay(98, 160)
		d.s3.DecodeFailures[sim.HarborpulseLegacyGauge][9].Error = "decode_failed: timeout"
	})
}

// An alarm the platform reactivates keeps its token across sim days, and its attempt list is the
// token's whole history. Another day's pages must neither stand in for today's nor count against
// today's acknowledgement.
func TestNotificationAttemptsAreJudgedOnTheWatchedDayOnly(t *testing.T) {
	yesterday := func(h float64) time.Time { return at(h).Add(-testClock.DayLength) }

	// Yesterday the acknowledged gauge went unacknowledged and was escalated; today it was
	// acknowledged in time. The control holds, and nothing else turns red.
	d := cleanDay()
	ack := "a-" + AckGauge
	d.s3.Attempts[ack] = append([]Attempt{
		{At: yesterday(18.0), AlarmToken: ack, Trigger: "ALARM", Status: "SENT"},
		{At: yesterday(19.5), AlarmToken: ack, Trigger: "ESCALATION", Status: "SENT"},
	}, d.s3.Attempts[ack]...)
	for _, inv := range Evaluate(d.observe()) {
		if !inv.Passed {
			t.Fatalf("yesterday's escalation of the acknowledged gauge turned %s red: %s", inv.Name, inv.Detail)
		}
	}

	// Yesterday a gauge was paged and escalated; today it was paged and never escalated.
	// Yesterday's escalation does not stand in for today's.
	evaluateOneRed(t, InvNotifyEscalates, "hp-gauge-basin-1: paged but never escalated", func(d *day) {
		tok := "a-hp-gauge-basin-1"
		d.s3.Attempts[tok] = append([]Attempt{
			{At: yesterday(18.0), AlarmToken: tok, Trigger: "ALARM", Status: "SENT"},
			{At: yesterday(19.5), AlarmToken: tok, Trigger: "ESCALATION", Status: "SENT"},
		}, d.s3.Attempts[tok][:1]...)
	})

	// Yesterday's attempts are not counted against today's mail either: a gauge paged twice
	// yesterday and once today needs today's mail only.
	d = cleanDay()
	tok := "a-hp-gauge-east-1"
	d.s3.Attempts[tok] = append([]Attempt{
		{At: yesterday(18.0), AlarmToken: tok, Trigger: "ALARM", Status: "SENT"},
		{At: yesterday(18.2), AlarmToken: tok, Trigger: "ALARM", Status: "SENT"},
		{At: yesterday(19.5), AlarmToken: tok, Trigger: "ESCALATION", Status: "SENT"},
	}, d.s3.Attempts[tok]...)
	for _, inv := range Evaluate(d.observe()) {
		if !inv.Passed {
			t.Fatalf("yesterday's pages turned %s red: %s", inv.Name, inv.Detail)
		}
	}
}

// The mail parser reads the rendering it is matched against, and refuses what it cannot attribute.
func TestDutyMailIsAttributedToItsDeviceAlarmAndTrigger(t *testing.T) {
	r := sim.HarborpulseDutyRecipient(testTenant)
	for _, c := range []struct{ trigger string }{{"ALARM"}, {"ESCALATION"}} {
		dev, key, trig, ok := dutyMail(dutyMailData(r, "hp-gauge-east-2", sim.HarborpulseFloodAlarmKey, c.trigger))
		if !ok || dev != "hp-gauge-east-2" || key != sim.HarborpulseFloodAlarmKey || trig != c.trigger {
			t.Fatalf("%s mail read as %q %q %q %v", c.trigger, dev, key, trig, ok)
		}
	}
	if _, _, _, ok := dutyMail("Subject: hello\r\n\r\nAlarm flood-level raised on somewhere.\r\n"); ok {
		t.Fatal("a mail naming no device was attributed")
	}
}

// One stale failure folded in after the baseline was read is not failures inside the watch.
func TestFoldedGrowthOfOneIsNotTheWatchsFailures(t *testing.T) {
	evaluateOneRed(t, InvDecoderFailureVisible, "suppressed count grew", func(d *day) {
		*d = *foldedLegacyDay(98, 99)
	})
	for _, inv := range Evaluate(foldedLegacyDay(98, 98+minSuppressedGrowth).observe()) {
		if !inv.Passed {
			t.Fatalf("%s failed on growth of %d: %s", inv.Name, minSuppressedGrowth, inv.Detail)
		}
	}
}
