// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/rs/zerolog"
)

// Keep failure diagnostics visible inside the gate's bounded output capture.
func init() { zerolog.SetGlobalLevel(zerolog.ErrorLevel) }

type replacementFake struct {
	mu                sync.Mutex
	t                 *testing.T
	records           []harborReplacementRecord
	emits             []string
	replaces, creates int
	enabled           map[string]bool
	lost              bool
}

func replacementSetup(t *testing.T, defects ...string) (*harborpulse, *Runtime, *replacementFake) {
	s := harborWith(t, defects...)
	s.s5.ledger = &ScenarioS5{OTA: ScenarioOTA{Artifact: "fixture-firmware"}}
	oldToken, oldSecret := deriveCredential(s.seed, HarborpulseReplacementDevice)
	f := &replacementFake{t: t, enabled: map[string]bool{oldToken: true}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	rt := &Runtime{Tenant: "tenant", HTTPClient: server.Client(), Endpoints: Endpoints{DeviceMgmtGraphQL: server.URL, Ingress: server.URL + "/ingress"}, Devices: []DeviceInstance{{Token: HarborpulseReplacementDevice, ExternalId: HarborpulseReplacementExternalID, DeviceTypeToken: HarborpulseGaugeType, CredentialToken: oldToken, CredentialId: oldSecret}}}
	rt.Session = userclient.NewTenantSession(server.Client(), server.URL, "sim@example.com", "password", "tenant")
	s.started = time.Now().UTC().Add(-7 * time.Minute)
	return s, rt, f
}

func (f *replacementFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/ingress/") {
		var body struct{ CredentialID string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.emits = append(f.emits, body.CredentialID)
		w.WriteHeader(202)
		return
	}
	var req struct {
		Query     string
		Variables map[string]json.RawMessage
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	data := map[string]any{}
	value := func(key string) string { var v string; _ = json.Unmarshal(req.Variables[key], &v); return v }
	switch {
	case strings.Contains(req.Query, "login("), strings.Contains(req.Query, "selectTenant("):
		data, _ = (&fakeProfileServer{}).dispatch(req.Query, nil)
	case strings.Contains(req.Query, "refresh("):
		data["refresh"] = map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)}
	case strings.Contains(req.Query, "deviceReplacements("):
		data["deviceReplacements"] = map[string]any{"results": f.records, "pagination": map[string]any{"totalRecords": len(f.records)}}
	case strings.Contains(req.Query, "replaceDevice("):
		var request struct{ DeviceToken, CredentialToken, CredentialType, Reason, UnitIdentifier string }
		_ = json.Unmarshal(req.Variables["request"], &request)
		if request.DeviceToken != HarborpulseReplacementDevice || request.Reason != "water ingress" || request.CredentialType != "ACCESS_TOKEN" {
			f.t.Error("replacement request identity/type/reason")
		}
		f.replaces++
		record := harborReplacementRecord{OccurredTime: time.Now().UTC().Format(time.RFC3339Nano), UnitIdentifier: request.UnitIdentifier, NewCredentialToken: request.CredentialToken}
		for token, enabled := range f.enabled {
			if enabled {
				record.RetiredCredentialTokens = append(record.RetiredCredentialTokens, token)
				f.enabled[token] = false
			}
		}
		record.Device.Token = HarborpulseReplacementDevice
		record.Device.ID = "gauge-id"
		record.Device.ExternalID = HarborpulseReplacementExternalID
		f.enabled[request.CredentialToken] = true
		f.records = append(f.records, record)
		if f.lost {
			http.Error(w, "lost response", 500)
			return
		}
		data["replaceDevice"] = map[string]any{"replacement": record, "newCredential": map[string]any{"token": request.CredentialToken, "credentialId": "once-returned-secret-material"}}
	case strings.Contains(req.Query, "updateDeviceCredential("):
		var request struct{ Enabled bool }
		_ = json.Unmarshal(req.Variables["request"], &request)
		f.enabled[value("token")] = request.Enabled
		data["updateDeviceCredential"] = map[string]any{"token": value("token")}
	case strings.Contains(req.Query, "devicesByToken("):
		data["devicesByToken"] = []any{map[string]any{"token": HarborpulseReplacementDevice}}
	case strings.Contains(req.Query, "deviceCredentialsByToken("):
		data["deviceCredentialsByToken"] = []any{map[string]any{"token": "exists"}}
	case strings.Contains(req.Query, "createDeviceCredential("):
		f.creates++
		data["createDeviceCredential"] = map[string]any{"token": "new"}
	default:
		f.t.Errorf("unexpected document %s", req.Query)
		http.Error(w, "unexpected", 400)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func TestHarborpulseReplacementHandoverOnceAndRedaction(t *testing.T) {
	s, rt, f := replacementSetup(t)
	ctx := context.Background()
	before := rt.Devices[0]
	beat := harborWall(s.started, s.dayLength, 0, HarborpulseReplacementHour)
	if err := s.replacementBeat(ctx, rt, beat.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if err := EmitAll(ctx, rt, 1, constantMetrics); err != nil {
		t.Fatal(err)
	}
	reconnected := 0
	rt.reconnectDevice = func(_ context.Context, old, next DeviceInstance) error {
		reconnected++
		if old.Token != next.Token || old.ExternalId != next.ExternalId || next.CredentialId == old.CredentialId {
			t.Error("handover identity/credential")
		}
		return nil
	}
	if err := s.replacementBeat(ctx, rt, beat); err != nil {
		t.Fatal(err)
	}
	if err := EmitAll(ctx, rt, 1, constantMetrics); err != nil {
		t.Fatal(err)
	}
	if err := s.replacementBeat(ctx, rt, beat.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f.replaces != 1 || reconnected != 1 || f.emits[0] != before.CredentialId || f.emits[1] != "once-returned-secret-material" {
		t.Fatalf("bad handover: replacements=%d reconnects=%d", f.replaces, reconnected)
	}
	if err := s.prepareReplacement(ctx, rt); err != nil {
		t.Fatal(err)
	}
	// Provision's expansion must apply the saved runtime credential before ensureDevice.
	devices := []DeviceInstance{before}
	devices = rt.applyCredentialOverrides(devices)
	if devices[0].CredentialId != "once-returned-secret-material" {
		t.Fatal("re-bootstrap reverted to seed credential")
	}
	if err := ensureDevice(ctx, rt, devices[0]); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 || f.enabled[before.CredentialToken] {
		t.Fatal("retired credential recreated or enabled")
	}
	raw, _ := json.Marshal(s.s5Ledger())
	if bytes.Contains(raw, []byte(before.CredentialId)) || bytes.Contains(raw, []byte("once-returned-secret-material")) || bytes.Contains(raw, []byte("credentialId")) {
		t.Fatal("ledger leaked credential material")
	}
	fresh := &Runtime{Session: rt.Session, Endpoints: rt.Endpoints}
	if err := s.prepareReplacement(ctx, fresh); err == nil {
		t.Fatal("fresh process accepted unrecoverable credential")
	}
}

func TestHarborpulseReplacementDefects(t *testing.T) {
	for _, defect := range []string{HarborpulseDefectReplacementNewDevice, HarborpulseDefectReplacementOldCredentialLeftLive} {
		t.Run(defect, func(t *testing.T) {
			s, rt, f := replacementSetup(t, defect)
			old := rt.Devices[0]
			if err := s.replacementBeat(context.Background(), rt, time.Now()); err != nil {
				t.Fatal(err)
			}
			if defect == HarborpulseDefectReplacementNewDevice {
				if f.replaces != 0 || rt.Devices[0].Token != HarborpulseReplacementSeparateDevice || f.enabled[old.CredentialToken] {
					t.Fatal("new-device control did not retire/move identity")
				}
			} else if !f.enabled[old.CredentialToken] {
				t.Fatal("left-live control did not enable retired credential")
			}
		})
	}
}

func TestHarborpulseReplacementLostSecretPausesGauge(t *testing.T) {
	s, rt, f := replacementSetup(t)
	f.lost = true
	for i := 0; i < 2; i++ {
		if s.replacementBeat(context.Background(), rt, time.Now()) == nil {
			t.Fatal("lost credential was not loud")
		}
	}
	if EmitAll(context.Background(), rt, 1, constantMetrics) == nil || len(f.emits) != 0 || f.replaces != 1 {
		t.Fatal("emitted retired credential or repeated replacement")
	}
}

func TestHarborpulseReplacementBlocksConcurrentEmitter(t *testing.T) {
	s, rt, f := replacementSetup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	rt.reconnectDevice = func(context.Context, DeviceInstance, DeviceInstance) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- s.replacementBeat(context.Background(), rt, time.Now()) }()
	<-entered
	emitted := make(chan error, 1)
	go func() { emitted <- EmitAll(context.Background(), rt, 1, constantMetrics) }()
	select {
	case <-emitted:
		t.Fatal("emitter escaped paused handover")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-emitted; err != nil {
		t.Fatal(err)
	}
	if len(f.emits) != 1 || f.emits[0] != "once-returned-secret-material" {
		t.Fatal("concurrent emit used retired credential")
	}
}

func TestHarborpulseReplacementDocuments(t *testing.T) {
	schema := servedSchema(t, "device-management")
	for _, doc := range []struct {
		q string
		v map[string]any
	}{
		{queryHarborReplacements, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 1, "device": "g"}}},
		{mutationHarborReplace, map[string]any{"request": map[string]any{"deviceToken": "g", "credentialType": "ACCESS_TOKEN", "credentialToken": "c", "reason": "water ingress", "unitIdentifier": "PAW-SN"}}},
		{mutationHarborCredentialEnabled, map[string]any{"token": "c", "request": map[string]any{"enabled": false}}},
	} {
		if errs := schema.ValidateWithVariables(doc.q, doc.v); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
}
