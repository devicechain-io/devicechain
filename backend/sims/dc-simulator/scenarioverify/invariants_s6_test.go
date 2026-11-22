// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func (d *day) cleanS6() {
	s := S6{Tenant: "tenant", Rejections: []S6Rejection{{Event: "x509_credential_rejected", Tenant: "tenant", Fingerprint: "cert-" + sim.HarborpulseRevokedCharger(), RejectedAt: at(14.1).Format(time.RFC3339Nano)}}, Ledger: &sim.ScenarioS6{Anchor: "hp-charger-trust", Credentials: map[string]string{}},
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
		s.Credentials[credential] = S6Credential{Token: credential, CredentialType: "X509_CERTIFICATE", CredentialID: "cert-" + device, UpdatedAt: at(14).Format(time.RFC3339Nano), Enabled: device != sim.HarborpulseRevokedCharger()}
		s.Credentials["token/"+device] = S6Credential{Token: device + "-token", CredentialType: "ACCESS_TOKEN", Enabled: false}
		document := `{"max_kw":11}`
		hash := sha256.Sum256([]byte(document))
		digest := "sha256:" + hex.EncodeToString(hash[:])
		c := S6Configuration{Desired: &S6Revision{Revision: 2, Digest: digest, Document: document, CreatedAt: at(9).Format(time.RFC3339Nano)}, Reported: &S6Report{Revision: 2, Digest: digest, Status: "APPLIED", ReportedAt: at(9.1).Format(time.RFC3339Nano)}}
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

func TestS6FirstDayEvidenceCannotPassAnotherDay(t *testing.T) {
	for _, check := range s6Checks() {
		if check.name == InvConnectivityAlarm {
			continue
		}
		t.Run(check.name, func(t *testing.T) {
			o := cleanDay().observe()
			o.Day = 1
			if check.fn(o) == nil {
				t.Fatal("day zero evidence passed day one")
			}
		})
	}
}

func TestS6RequiresCertificateRejectionEvidence(t *testing.T) {
	o := cleanDay().observe()
	if err := json.Unmarshal([]byte(`{"Rejections":[]}`), &o.S6); err != nil {
		t.Fatal(err)
	}
	if chargerX509Admitted(o) == nil {
		t.Fatal("disabled/offline snapshot passed without platform certificate rejection")
	}
}

func TestS6EvidenceFromSelectedDayPasses(t *testing.T) {
	for _, day := range []int{1, 3} {
		o := cleanDay().observe()
		raw, err := json.Marshal(o.S6)
		if err != nil {
			t.Fatal(err)
		}
		var tree any
		if err := json.Unmarshal(raw, &tree); err != nil {
			t.Fatal(err)
		}
		var shift func(any) any
		shift = func(v any) any {
			switch x := v.(type) {
			case string:
				if at, err := time.Parse(time.RFC3339Nano, x); err == nil {
					return at.Add(time.Duration(day) * o.Clock.DayLength).Format(time.RFC3339Nano)
				}
			case map[string]any:
				for k, v := range x {
					x[k] = shift(v)
				}
			case []any:
				for k, v := range x {
					x[k] = shift(v)
				}
			}
			return v
		}
		raw, err = json.Marshal(shift(tree))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &o.S6); err != nil {
			t.Fatal(err)
		}
		o.Day = day
		for _, check := range s6Checks() {
			if check.name != InvConnectivityAlarm {
				if err := check.fn(o); err != nil {
					t.Errorf("day %d %s: %v", day, check.name, err)
				}
			}
		}
	}
}

func TestS6RejectsUnrelatedOrOldCertificateRejection(t *testing.T) {
	for _, change := range []map[string]string{
		{"Fingerprint": "another-certificate"},
		{"Tenant": "another-tenant"},
		{"RejectedAt": at(13).Format(time.RFC3339Nano)},
		{"RejectedAt": at(38).Format(time.RFC3339Nano)},
	} {
		o := cleanDay().observe()
		record := map[string]string{"Event": "x509_credential_rejected", "Tenant": "tenant", "Fingerprint": "cert-" + sim.HarborpulseRevokedCharger(), "RejectedAt": at(14.1).Format(time.RFC3339Nano)}
		for k, v := range change {
			record[k] = v
		}
		raw, err := json.Marshal(map[string]any{"Rejections": []any{record}})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &o.S6); err != nil {
			t.Fatal(err)
		}
		if chargerX509Admitted(o) == nil {
			t.Errorf("unrelated rejection passed: %v", change)
		}
	}
}

func TestS6CurrentReadingsCannotHideAnOldPullCompletion(t *testing.T) {
	o := cleanDay().observe()
	o.Day = 1
	for _, device := range sim.HarborpulsePullLights() {
		for i := range o.S6.Readings[device] {
			o.S6.Readings[device][i].At = o.S6.Readings[device][i].At.Add(o.Clock.DayLength)
		}
	}
	if pullCommandsComplete(o) == nil {
		t.Fatal("fresh readings hid an earlier day's command completion")
	}
}

func TestS6EvidenceCannotComeFromTheNextDay(t *testing.T) {
	for _, check := range []func(Observations) error{chargerMQTTStored, pullCommandsComplete} {
		o := cleanDay().observe()
		for device, rows := range o.S6.Readings {
			for i := range rows {
				rows[i].At = rows[i].At.Add(o.Clock.DayLength)
			}
			o.S6.Readings[device] = rows
		}
		if check(o) == nil {
			t.Fatal("next day's readings passed the selected day")
		}
	}
}

func TestS6ConfigurationNeedsBothTransitionAndReportThisDay(t *testing.T) {
	for _, stale := range []string{"desired", "reported"} {
		o := cleanDay().observe()
		o.Day = 1
		for _, c := range o.S6.Configurations {
			if stale != "desired" {
				c.Desired.CreatedAt = at(33).Format(time.RFC3339Nano)
			}
			if stale != "reported" {
				c.Reported.ReportedAt = at(33.1).Format(time.RFC3339Nano)
			}
		}
		if configApplied(o) == nil {
			t.Errorf("old %s evidence passed", stale)
		}
	}
}

func TestReadS6RejectionsUsesPlatformRecordsForThisTenant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callout.jsonl")
	raw := "kubectl diagnostic\n" + `{"event":"x509_credential_rejected","tenant":"other","fingerprint":"other"}` + "\n" + `{"event":"other","tenant":"tenant","fingerprint":"noise"}` + "\n" + `{"event":"x509_credential_rejected","tenant":"tenant","fingerprint":"certificate","rejectedAt":"2026-11-16T12:08:01Z"}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := readS6Rejections(path, "tenant")
	if err != nil || len(records) != 1 || records[0].Fingerprint != "certificate" || records[0].RejectedAt != "2026-11-16T12:08:01Z" {
		t.Fatalf("records=%+v error=%v", records, err)
	}
	if _, err := readS6Rejections(path+".missing", "tenant"); err == nil {
		t.Fatal("missing log source was silently accepted")
	}
}
