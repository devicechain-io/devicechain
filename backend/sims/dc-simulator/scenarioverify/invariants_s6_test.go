// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func (d *day) cleanS6() {
	s := S6{Ledger: &sim.ScenarioS6{Anchor: "hp-charger-trust", Credentials: map[string]string{}},
		AnchorEnabled: true, States: map[string]S6Presence{}, Credentials: map[string]S6Credential{},
		Configurations: map[string]S6Configuration{}, Readings: map[string][]Reading{}, PullChannel: "HTTP_PULL"}
	for _, device := range append(sim.HarborpulseMQTTChargers(), sim.HarborpulseX509Chargers()...) {
		s.States[device] = S6Presence{DeviceToken: device, Active: true, PresenceSource: "ASSERTED", SessionID: "12345", LastConnectTime: at(8).Format(time.RFC3339Nano)}
		for _, h := range []float64{8, 8.5, 9, 16, 17, 18} {
			if device == sim.HarborpulseRevokedCharger() && h >= sim.HarborpulseX509RevokeHour {
				continue
			}
			s.Readings[device] = append(s.Readings[device], Reading{Name: sim.HarborpulseChargerPowerKey, Value: 7.2, At: at(h)})
		}
	}
	for _, device := range sim.HarborpulseX509Chargers() {
		credential := device + "-x509"
		s.Ledger.Credentials[device] = credential
		s.Credentials[credential] = S6Credential{Token: credential, CredentialType: "X509_CERTIFICATE", UpdatedAt: at(14).Format(time.RFC3339Nano), Enabled: device != sim.HarborpulseRevokedCharger()}
		s.Credentials["token/"+device] = S6Credential{Token: device + "-token", CredentialType: "ACCESS_TOKEN", Enabled: false}
		document := `{"max_kw":11}`
		hash := sha256.Sum256([]byte(document))
		digest := "sha256:" + hex.EncodeToString(hash[:])
		c := S6Configuration{Desired: &S6Revision{Revision: 2, Digest: digest, Document: document}, Reported: &S6Report{Revision: 2, Digest: digest, Status: "APPLIED"}}
		if device == sim.HarborpulseRejectedConfigCharger() {
			c.Reported.Status, c.Reported.Errors, c.Pending = "REJECTED", []string{"power cap refused"}, true
		}
		s.Configurations[device] = c
	}
	p := s.States[sim.HarborpulseRevokedCharger()]
	p.Active = false
	s.States[sim.HarborpulseRevokedCharger()] = p
	for _, device := range sim.HarborpulsePullLights() {
		s.Commands = append(s.Commands, Command{Device: device, Name: sim.HarborpulseSetDimKey, Status: "SUCCESSFUL", Queued: at(10), Responded: at(10).Add(time.Second)})
		s.Readings[device] = []Reading{{Name: sim.HarborpulseDimKey, Value: 35, At: at(10.1)}}
	}
	d.alarms = append(d.alarms, lifecycle{"mqtt-drop", sim.HarborpulseMQTTChargers()[0], sim.HarborpulseConnectivityAlarm, []step{{12.05, minor}}, 13.05})
	d.s6 = s
}

func TestHarborpulseS6ControlsRegistered(t *testing.T) {
	for _, c := range s6Checks() {
		if Controls[c.name] == "" {
			t.Errorf("%s has no planted scenario defect", c.name)
		}
	}
}

func TestEachS6ControlTurnsExactlyItsInvariantRed(t *testing.T) {
	controls := []struct {
		invariant string
		breakDay  func(*day)
	}{
		{InvChargerMQTTStored, func(d *day) { d.s6.Readings[sim.HarborpulseMQTTChargers()[1]] = nil }},
		{InvChargerX509Admitted, func(d *day) {
			token := d.s6.Ledger.Credentials[sim.HarborpulseRevokedCharger()]
			c := d.s6.Credentials[token]
			c.Enabled = true
			d.s6.Credentials[token] = c
		}},
		{InvConnectivityAlarm, func(d *day) {
			var keep []lifecycle
			for _, a := range d.alarms {
				if a.key != sim.HarborpulseConnectivityAlarm {
					keep = append(keep, a)
				}
			}
			d.alarms = keep
		}},
		{InvConfigApplied, func(d *day) {
			for device, c := range d.s6.Configurations {
				c.Reported = nil
				d.s6.Configurations[device] = c
			}
		}},
		{InvPullCommandsComplete, func(d *day) {
			for i := range d.s6.Commands {
				d.s6.Commands[i].Status = "QUEUED"
				d.s6.Commands[i].Responded = time.Time{}
			}
		}},
	}
	for _, control := range controls {
		t.Run(control.invariant, func(t *testing.T) {
			d := cleanDay()
			for _, inv := range Evaluate(d.observe()) {
				if !inv.Passed {
					t.Fatalf("clean baseline %s: %s", inv.Name, inv.Detail)
				}
			}
			control.breakDay(d)
			failed := []string{}
			for _, inv := range Evaluate(d.observe()) {
				if !inv.Passed {
					failed = append(failed, inv.Name)
				}
			}
			if len(failed) != 1 || failed[0] != control.invariant {
				t.Fatalf("failed %v, want exactly %s", failed, control.invariant)
			}
		})
	}
}

func TestS6RejectsInferredPresenceAndStaleOrMissingEvidence(t *testing.T) {
	cases := []struct {
		name  string
		check func(Observations) error
		alter func(*Observations)
	}{
		{"inferred", chargerMQTTStored, func(o *Observations) {
			d := sim.HarborpulseMQTTChargers()[0]
			p := o.S6.States[d]
			p.PresenceSource = "INFERRED"
			o.S6.States[d] = p
		}},
		{"token-fallback", chargerX509Admitted, func(o *Observations) {
			d := "token/" + sim.HarborpulseX509Chargers()[0]
			c := o.S6.Credentials[d]
			c.Enabled = true
			o.S6.Credentials[d] = c
		}},
		{"post-revoke-store", chargerX509Admitted, func(o *Observations) {
			d := sim.HarborpulseRevokedCharger()
			o.S6.Readings[d] = append(o.S6.Readings[d], Reading{Name: sim.HarborpulseChargerPowerKey, Value: 7.2, At: at(16)})
		}},
		{"post-revoke-connect", chargerX509Admitted, func(o *Observations) {
			d := sim.HarborpulseRevokedCharger()
			p := o.S6.States[d]
			p.LastConnectTime = at(16).Format(time.RFC3339Nano)
			o.S6.States[d] = p
		}},
		{"no-clear", connectivityAlarm, func(o *Observations) {
			for i := range o.Alarms {
				if o.Alarms[i].Key == sim.HarborpulseConnectivityAlarm {
					o.Alarms[i].State = sim.AlarmStateActiveWire
				}
			}
		}},
		{"report-digest", configApplied, func(o *Observations) {
			o.S6.Configurations[sim.HarborpulseX509Chargers()[0]].Reported.Digest = "sha256:stale"
		}},
		{"report-revision", configApplied, func(o *Observations) { o.S6.Configurations[sim.HarborpulseX509Chargers()[0]].Reported.Revision-- }},
		{"missing-rejection", configApplied, func(o *Observations) {
			o.S6.Configurations[sim.HarborpulseRejectedConfigCharger()].Reported.Status = "APPLIED"
		}},
		{"published-lamp", pullCommandsComplete, func(o *Observations) { o.S6.PullChannel = "PUBLISH" }},
		{"unacted-lamp", pullCommandsComplete, func(o *Observations) { o.S6.Readings[sim.HarborpulsePullLights()[0]] = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := cleanDay().observe()
			c.alter(&o)
			if c.check(o) == nil {
				t.Fatal("missing or contradictory platform evidence passed")
			}
		})
	}
}

func TestS6ReadDocumentsMatchServedSchemas(t *testing.T) {
	for _, d := range []struct {
		area, doc string
		vars      map[string]any
	}{
		{"device-state", queryS6States, map[string]any{"tokens": []string{"d"}}},
		{"device-management", queryS6Credentials, map[string]any{"tokens": []string{"c"}}},
		{"device-management", queryS6Anchor, map[string]any{"tokens": []string{"a"}}},
		{"device-management", queryS6Configuration, map[string]any{"device": "d"}},
		{"device-management", queryS6PullType, map[string]any{"tokens": []string{"t"}}},
	} {
		if errs := servedSchema(t, d.area).ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s: %v", d.doc, errs)
		}
	}
}
