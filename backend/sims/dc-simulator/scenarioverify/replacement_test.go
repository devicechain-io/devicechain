// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func (d *day) cleanReplacement() {
	old, _ := sim.DeriveDeviceCredential(1, sim.HarborpulseReplacementDevice)
	next := sim.HarborpulseReplacementDevice + "-replacement"
	device := ReplacementDevice{ID: "gauge-id", Token: sim.HarborpulseReplacementDevice, ExternalID: sim.HarborpulseReplacementExternalID}
	r := ReplacementRecord{Device: device, OccurredTime: at(12.5).Format(time.RFC3339Nano), UnitIdentifier: sim.HarborpulseReplacementUnit(1), RetiredCredentialTokens: []string{old}, NewCredentialToken: next}
	d.s5.Ledger.Replacement = &sim.ScenarioReplacement{Device: device.Token, At: r.OccurredTime, UnitIdentifier: r.UnitIdentifier, RetiredCredentialTokens: r.RetiredCredentialTokens, NewCredentialToken: next}
	s := ReplacementEvidence{Device: &device, Records: []ReplacementRecord{r}, ProbeStatus: 401, Credentials: map[string]ReplacementCredential{}}
	for _, token := range []string{old, next} {
		c := ReplacementCredential{Token: token, Enabled: token == next}
		c.Device.Token = device.Token
		s.Credentials[token] = c
	}
	for h := 12.0; h < WatchTo; h += 0.5 {
		s.Readings = append(s.Readings, Reading{Name: sim.HarborpulseWaterLevelKey, At: at(h).Add(time.Second), Value: 1})
	}
	d.s5.Replacement = s
}

func TestEachS5ReplacementControlModelsLiveEvidence(t *testing.T) {
	t.Run(sim.HarborpulseDefectReplacementOldCredentialLeftLive, func(t *testing.T) {
		evaluateOneRed(t, InvReplacementCredential, "enabled", func(d *day) {
			old, _ := sim.DeriveDeviceCredential(1, sim.HarborpulseReplacementDevice)
			c := d.s5.Replacement.Credentials[old]
			c.Enabled = true
			d.s5.Replacement.Credentials[old] = c
			d.s5.Replacement.ProbeStatus = 202
			d.s5.Replacement.ProbeAt = at(27)
			d.s5.Replacement.Readings = append(d.s5.Replacement.Readings, Reading{Name: sim.HarborpulseBatteryKey, At: at(27), Value: 3.14159})
		})
	})
	t.Run(sim.HarborpulseDefectReplacementNewDevice, func(t *testing.T) {
		// Retiring the original gauge and moving its stream also moves its flood
		// alarm, MQTT bulletins and duty pages. The East-only correlation/webhook
		// and the acknowledged north-2 control retain their evidence.
		evaluateRed(t, InvReplacementIdentity, "records", []string{InvFloodEscalates, InvMQTTBulletin, InvNotifyEscalates}, func(d *day) {
			old, next := sim.HarborpulseReplacementDevice, sim.HarborpulseReplacementSeparateDevice
			d.s5.Replacement.Records = nil
			// The new logical device did not exist before the replacement beat.
			d.s5.Replacement.Readings = d.s5.Replacement.Readings[1:]
			d.s5.Ledger.Replacement.Device = next
			newToken, _ := sim.DeriveDeviceCredential(1, next)
			delete(d.s5.Replacement.Credentials, d.s5.Ledger.Replacement.NewCredentialToken)
			d.s5.Ledger.Replacement.NewCredentialToken = newToken
			c := ReplacementCredential{Token: newToken, Enabled: true}
			c.Device.Token = next
			d.s5.Replacement.Credentials[newToken] = c
			for i := range d.alarms {
				if d.alarms[i].device == old {
					d.alarms[i].device = next
					d.alarms[i].token = "a-" + next
				}
			}
			d.s3.Attempts["a-"+next] = d.s3.Attempts["a-"+old]
			delete(d.s3.Attempts, "a-"+old)
			for i := range d.s3.Attempts["a-"+next] {
				d.s3.Attempts["a-"+next][i].AlarmToken = "a-" + next
			}
			for i := range d.s3.Sink.Messages {
				d.s3.Sink.Messages[i].Payload = strings.ReplaceAll(d.s3.Sink.Messages[i].Payload, old, next)
			}
			for i := range d.s3.Sink.Mail {
				d.s3.Sink.Mail[i].Data = strings.ReplaceAll(d.s3.Sink.Mail[i].Data, old, next)
			}
		})
	})
}

func TestS5ReplacementIdentityGuards(t *testing.T) {
	for name, mutate := range map[string]func(*S5){
		"unit":     func(s *S5) { s.Replacement.Records[0].UnitIdentifier = "other" },
		"id":       func(s *S5) { s.Replacement.Records[0].Device.ID = "other" },
		"external": func(s *S5) { s.Replacement.Device.ExternalID = "other" },
		"twice":    func(s *S5) { s.Replacement.Records = append(s.Replacement.Records, s.Replacement.Records[0]) },
		"before":   func(s *S5) { s.Replacement.Readings = s.Replacement.Readings[1:] },
		"after":    func(s *S5) { s.Replacement.Readings = s.Replacement.Readings[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			d := cleanDay()
			mutate(&d.s5)
			if replacementIdentity(d.observe()) == nil {
				t.Fatal("invalid replacement identity passed")
			}
		})
	}
}

func TestS5ReplacementCredentialGuards(t *testing.T) {
	for name, mutate := range map[string]func(*S5){
		"probe": func(s *S5) { s.Replacement.ProbeStatus = 202 },
		"new-disabled": func(s *S5) {
			token := s.Ledger.Replacement.NewCredentialToken
			c := s.Replacement.Credentials[token]
			c.Enabled = false
			s.Replacement.Credentials[token] = c
		},
		"retired-mismatch": func(s *S5) { s.Replacement.Records[0].RetiredCredentialTokens = []string{"other"} },
		"no-after":         func(s *S5) { s.Replacement.Readings = s.Replacement.Readings[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			d := cleanDay()
			mutate(&d.s5)
			if replacementCredential(d.observe()) == nil {
				t.Fatal("invalid replacement credential passed")
			}
		})
	}
}

func TestS5ReplacementProbeClassificationAndRetry(t *testing.T) {
	for _, status := range []int{401, 202, 400, 403, 500, 503} {
		if (classifyReplacementProbe(status) == nil) != (status == 401 || status == 202) {
			t.Fatalf("status %d classification", status)
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/instance/tenant/events" {
			t.Error("probe did not use the real ingress event route")
		}
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, secret := sim.DeriveDeviceCredential(1, sim.HarborpulseReplacementDevice)
		if body["credentialId"] != secret || body["device"] != sim.HarborpulseReplacementDevice {
			t.Error("probe did not use original credential")
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(401)
	}))
	defer server.Close()
	r := &Reader{http: server.Client(), seed: 1, ingress: server.URL + "/instance/tenant/events", endpoints: sim.Endpoints{Ingress: server.URL}}
	start := time.Now()
	status, _, err := r.probeOldCredential(context.Background())
	if err != nil || status != 401 || calls != 2 || time.Since(start) < time.Second {
		t.Fatalf("probe=%d calls=%d err=%v", status, calls, err)
	}
}

func TestReplacementVerifierDocuments(t *testing.T) {
	schema := servedSchema(t, "device-management")
	for _, doc := range []struct {
		q string
		v map[string]any
	}{
		{queryReplacementDevice, map[string]any{"tokens": []string{"g"}}},
		{queryReplacementCredentials, map[string]any{"tokens": []string{"c"}}},
		{queryReplacements, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100, "device": "g"}}},
	} {
		if errs := schema.ValidateWithVariables(doc.q, doc.v); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
}

// TestReplacementInstantIsComparedAsAnInstant is the live finding from the rig: replaceDevice
// answers occurredTime with the server clock's nanoseconds and the stored record reads back at
// the store's microseconds, so the two strings of ONE replacement differ. That is not a second
// replacement; an instant a whole second away is.
func TestReplacementInstantIsComparedAsAnInstant(t *testing.T) {
	d := cleanDay()
	stored := at(12.5).Add(123456 * time.Microsecond)
	d.s5.Replacement.Records[0].OccurredTime = stored.Format(time.RFC3339Nano)
	d.s5.Ledger.Replacement.At = stored.Add(789 * time.Nanosecond).In(time.FixedZone("x", 3600)).Format(time.RFC3339Nano)
	if err := replacementIdentity(d.observe()); err != nil {
		t.Fatalf("one replacement at two precisions judged different: %v", err)
	}
	d.s5.Ledger.Replacement.At = stored.Add(time.Second).Format(time.RFC3339Nano)
	if replacementIdentity(d.observe()) == nil {
		t.Fatal("a declared instant a second away passed")
	}
}
