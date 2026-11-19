// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/otafleet"
)

type hpOTAPlatform struct {
	t                                                                   *testing.T
	mu                                                                  sync.Mutex
	signers                                                             []harborSigner
	public                                                              map[string]*ecdsa.PublicKey
	artifact                                                            *harborArtifact
	rollout                                                             *harborRollout
	registers, revokes, creates, uploads, signs, rolloutCreates, starts int
	lose                                                                string
	refuse                                                              bool
	downloads                                                           bool
	allowUnverified                                                     bool
	retireOnUpload, hideSignatures                                      bool
	rolloutReads                                                        int
}

func hpOTASetup(t *testing.T) (*hpOTAPlatform, *Runtime) {
	t.Helper()
	f := &hpOTAPlatform{t: t, public: map[string]*ecdsa.PublicKey{}, downloads: true, signers: []harborSigner{{Token: "stale", Name: harborChargerSignerName, KeyID: "stale-key", State: "ACTIVE"}, {Token: "unrelated", Name: "other firmware", KeyID: "other-key", State: "ACTIVE"}}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	rt := &Runtime{Tenant: "tenant", InstanceId: "instance", HTTPClient: srv.Client(), Endpoints: Endpoints{UpdateMgmtGraphQL: srv.URL + "/graphql"}}
	rt.Session = userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.com", "password", "tenant")
	return f, rt
}

func (f *hpOTAPlatform) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPut {
		if r.Header.Get("Authorization") == "" {
			f.t.Error("upload has no tenant bearer")
		}
		content, _ := io.ReadAll(r.Body)
		if !bytes.Equal(content, HarborpulseFirmware()) {
			f.t.Error("upload differs from deterministic payload")
		}
		if f.artifact == nil {
			http.Error(w, "undeclared", 400)
			return
		}
		f.uploads++
		f.artifact.State = "AVAILABLE"
		if f.retireOnUpload {
			f.artifact.State = "RETIRED"
		}
		w.WriteHeader(204)
		return
	}
	var req struct {
		Query     string
		Variables map[string]json.RawMessage
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Error(err)
		http.Error(w, "bad json", 400)
		return
	}
	data := map[string]any{}
	value := func(key string) string { var s string; _ = json.Unmarshal(req.Variables[key], &s); return s }
	op := ""
	switch {
	case strings.Contains(req.Query, "login("), strings.Contains(req.Query, "selectTenant("):
		data, _ = (&fakeProfileServer{}).dispatch(req.Query, nil)
	case strings.Contains(req.Query, "refresh("):
		data["refresh"] = map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)}
	case strings.Contains(req.Query, "updateManagementInfo"):
		data["updateManagementInfo"] = map[string]any{"downloadsConfigured": f.downloads, "allowUnverifiedArtifacts": f.allowUnverified}
	case strings.Contains(req.Query, "artifactSigners("):
		var signers []harborSigner
		for _, s := range f.signers {
			if s.State == "ACTIVE" {
				signers = append(signers, s)
			}
		}
		data["artifactSigners"] = map[string]any{"results": signers, "pagination": map[string]any{"totalRecords": len(signers)}}
	case strings.Contains(req.Query, "registerArtifactSigner("):
		op = "register"
		f.registers++
		block, _ := pem.Decode([]byte(value("pem")))
		if block == nil {
			f.t.Error("invalid public PEM")
			http.Error(w, "bad key", 400)
			return
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			f.t.Error(err)
			return
		}
		sum := sha256.Sum256(block.Bytes)
		signer := harborSigner{Token: fmt.Sprintf("signer-%d", f.registers), Name: value("name"), KeyID: hex.EncodeToString(sum[:]), State: "ACTIVE"}
		f.public[signer.Token] = key.(*ecdsa.PublicKey)
		f.signers = append(f.signers, signer)
		data["registerArtifactSigner"] = signer
	case strings.Contains(req.Query, "revokeArtifactSigner("):
		op = "revoke"
		f.revokes++
		for i := range f.signers {
			if f.signers[i].Token == value("token") {
				f.signers[i].State = "REVOKED"
			}
		}
		if f.artifact != nil {
			active := false
			for i := range f.artifact.Signatures {
				sig := &f.artifact.Signatures[i]
				if sig.Signer.Token == value("token") {
					sig.Signer.State = "REVOKED"
					sig.CountsTowardTrust = false
				}
				active = active || sig.CountsTowardTrust
			}
			if !active {
				f.artifact.TrustStatus = "UNVERIFIED"
			}
		}
		data["revokeArtifactSigner"] = map[string]any{"token": value("token")}
	case strings.Contains(req.Query, "createArtifact("):
		op = "artifact"
		f.creates++
		var a harborArtifact
		_ = json.Unmarshal(req.Variables["request"], &a)
		a.State = "UPLOADING"
		a.TrustStatus = "UNVERIFIED"
		f.artifact = &a
		data["createArtifact"] = a
	case strings.Contains(req.Query, "addArtifactSignature("):
		op = "sign"
		f.signs++
		signature, _ := base64.StdEncoding.DecodeString(value("signature"))
		sum := sha256.Sum256(HarborpulseFirmware())
		key := f.public[value("signer")]
		if key == nil || !ecdsa.VerifyASN1(key, sum[:], signature) {
			f.t.Error("signature does not verify over SHA-256(content)")
		}
		for _, signer := range f.signers {
			if signer.Token == value("signer") {
				f.artifact.Signatures = append(f.artifact.Signatures, struct {
					Signer            harborSigner
					CountsTowardTrust bool
				}{signer, true})
			}
		}
		f.artifact.TrustStatus = "SIGNED"
		data["addArtifactSignature"] = f.artifact
	case strings.Contains(req.Query, "artifact("):
		data["artifact"] = f.artifact
		if f.hideSignatures && f.artifact != nil {
			copy := *f.artifact
			copy.Signatures = nil
			data["artifact"] = &copy
		}
	case strings.Contains(req.Query, "createUpdateRollout("):
		if f.refuse || f.artifact.TrustStatus != "SIGNED" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "unverified artifact refused", "extensions": map[string]any{"code": "UNVERIFIED_ARTIFACT_REFUSED"}}}})
			return
		}
		op = "rollout"
		f.rolloutCreates++
		var request struct {
			Token, GroupToken, ArtifactToken string
			MaxInFlight, FailureThreshold    int
		}
		_ = json.Unmarshal(req.Variables["request"], &request)
		rollout := &harborRollout{Token: request.Token, State: "DRAFT", GroupToken: request.GroupToken, MaxInFlight: request.MaxInFlight, FailureThreshold: request.FailureThreshold, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		rollout.Artifact.Token = request.ArtifactToken
		f.rollout = rollout
		data["createUpdateRollout"] = rollout
	case strings.Contains(req.Query, "startUpdateRollout("):
		op = "start"
		f.starts++
		f.rollout.State = "RUNNING"
		f.rollout.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		data["startUpdateRollout"] = f.rollout
	case strings.Contains(req.Query, "updateRollout("):
		f.rolloutReads++
		data["updateRollout"] = f.rollout
	default:
		f.t.Errorf("unexpected document %s", req.Query)
		http.Error(w, "unexpected", 400)
		return
	}
	if f.lose == op && op != "" {
		f.lose = ""
		http.Error(w, "response lost after write", 500)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func TestHarborpulseS5ProvisioningReusesIdentityAndRevokesStale(t *testing.T) {
	f, rt := hpOTASetup(t)
	s := harborWith(t)
	if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	key := s.s5.key
	if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if f.registers != 1 || f.revokes != 1 || f.creates != 1 || f.uploads != 1 || f.signs != 1 || s.s5.key != key {
		t.Fatalf("not idempotent: registers=%d revokes=%d creates=%d uploads=%d signs=%d", f.registers, f.revokes, f.creates, f.uploads, f.signs)
	}
	for _, signer := range f.signers {
		if signer.Token == "unrelated" && signer.State != "ACTIVE" {
			t.Fatal("revoked unrelated signer")
		}
	}
	raw, err := json.Marshal(s.ScenarioClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := x509.MarshalECPrivateKey(key)
	if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(secret))) || bytes.Contains(raw, []byte(hex.EncodeToString(key.D.Bytes()))) {
		t.Fatal("private key leaked in ledger JSON")
	}
	f.artifact.Digest = "sha256:conflicting"
	if err := s.provisionChargerOTA(context.Background(), rt); err == nil {
		t.Fatal("conflicting artifact identity was accepted")
	}
}

func TestHarborpulseS5NewProcessReusesArtifactWithNewSigner(t *testing.T) {
	f, rt := hpOTASetup(t)
	first := harborWith(t)
	if err := first.provisionChargerOTA(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	second := harborWith(t)
	if err := second.provisionChargerOTA(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if f.registers != 2 || f.creates != 1 || f.uploads != 1 || f.signs != 2 {
		t.Fatalf("restart did not reuse content: %d %d %d %d", f.registers, f.creates, f.uploads, f.signs)
	}
	if first.s5Ledger().OTA.KeyID == second.s5Ledger().OTA.KeyID {
		t.Fatal("fresh process reused private key")
	}
}

func TestHarborpulseS5LostProvisionResponsesRetryByReadback(t *testing.T) {
	for _, op := range []string{"register", "artifact", "sign"} {
		t.Run(op, func(t *testing.T) {
			f, rt := hpOTASetup(t)
			s := harborWith(t)
			f.lose = op
			if err := s.provisionChargerOTA(context.Background(), rt); err == nil {
				t.Fatal("lost response did not fail")
			}
			if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			if f.registers != 1 || f.creates != 1 || f.uploads != 1 || f.signs != 1 {
				t.Fatalf("repeated a persisted operation: %+v", s.s5Ledger())
			}
		})
	}
}

func TestHarborpulseS5RolloutCrossesOnceAndRecoversWrites(t *testing.T) {
	for _, op := range []string{"", "rollout", "start"} {
		t.Run(op, func(t *testing.T) {
			f, rt := hpOTASetup(t)
			s := harborWith(t)
			if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			s.started = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			beat := harborWall(s.started, s.dayLength, 0, HarborpulseRolloutHour)
			if err := s.s5Beats(context.Background(), rt, beat.Add(-time.Nanosecond)); err != nil || f.rolloutCreates != 0 {
				t.Fatalf("early beat: %v", err)
			}
			f.lose = op
			err := s.s5Beats(context.Background(), rt, beat.Add(time.Second))
			if (err != nil) != (op != "") {
				t.Fatalf("lost %s error=%v", op, err)
			}
			if err := s.s5Beats(context.Background(), rt, beat.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			// A re-bootstrap retains the run's key and completed beat.
			if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			reads := f.rolloutReads
			if err := s.s5Beats(context.Background(), rt, beat.Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if f.rolloutReads != reads {
				t.Fatal("successful once-per-run beat queried the platform again")
			}
			if f.rolloutCreates != 1 || f.starts != 1 {
				t.Fatalf("rollout repeated: create=%d start=%d", f.rolloutCreates, f.starts)
			}
			ledger := s.s5Ledger().OTA
			if ledger.MaxInFlight != 2 || ledger.FailureThreshold != 2 || ledger.CreatedAt == "" || ledger.StartedAt == "" {
				t.Fatalf("incomplete rollout ledger %+v", ledger)
			}
			// Simulate a beat runner losing its completion flag: the platform remains authoritative.
			s.s5.rolloutDone = false
			f.rollout.State = "COMPLETED"
			if err := s.s5Beats(context.Background(), rt, beat.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if f.starts != 1 || f.rolloutCreates != 1 {
				t.Fatal("completed rollout restarted")
			}
		})
	}
}

func TestHarborpulseS5DefectsAndNamedPopulation(t *testing.T) {
	for _, defect := range []string{"", HarborpulseDefectOTAFailureDisabled, HarborpulseDefectOTACapThree, HarborpulseDefectOTASignatureOmitted, HarborpulseDefectOTAGroupMissingCharger} {
		t.Run(defect, func(t *testing.T) {
			f, rt := hpOTASetup(t)
			s := harborWith(t)
			if defect != "" {
				s = harborWith(t, defect)
			}
			if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
				t.Fatal(err)
			}
			population, err := s.UpdatePopulation(rt)
			if err != nil {
				t.Fatal(err)
			}
			if len(population.Tokens) != 6 || len(population.Options.TrustedKeys) != 1 || population.Options.InitialVersion != "2.4.0" || population.Options.AllowUnsigned {
				t.Fatalf("population %+v", population)
			}
			for i, token := range population.Tokens {
				want := otafleet.Healthy
				if token == HarborpulseFailedCharger && defect != HarborpulseDefectOTAFailureDisabled {
					want = otafleet.BadHash
				}
				if got := population.Options.ModeFor(token, i+30); got != want {
					t.Fatalf("%s mode=%s want=%s", token, got, want)
				}
			}
			s.started = time.Now().Add(-20 * time.Minute)
			err = s.s5Beats(context.Background(), rt, harborWall(s.started, s.dayLength, 0, HarborpulseRolloutHour))
			if defect == HarborpulseDefectOTASignatureOmitted {
				if err == nil || s.s5Ledger().OTA.RefusalCode != "UNVERIFIED_ARTIFACT_REFUSED" || f.rollout != nil || f.artifact.TrustStatus != "UNVERIFIED" {
					t.Fatalf("control did not model platform refusal: %v %+v", err, s.s5Ledger())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if defect == HarborpulseDefectOTACapThree && f.rollout.MaxInFlight != 3 {
				t.Fatal("cap-three did not set platform maxInFlight=3")
			}
			m := s.Manifest()
			for _, device := range m.Expand(1) {
				if device.Token == HarborpulseMissingCharger {
					hasRole := false
					for _, a := range device.Attributes {
						if a.Key == HarborpulseRoleAttr {
							hasRole = true
						}
					}
					if hasRole == (defect == HarborpulseDefectOTAGroupMissingCharger) {
						t.Fatal("missing-charger must have no role attribute")
					}
				}
			}
		})
	}
}

func TestHarborpulseS5ReadinessFailsLoudly(t *testing.T) {
	s := harborWith(t)
	if err := s.Bootstrap(context.Background(), &Runtime{}); err == nil || !strings.Contains(err.Error(), "re-create the sim with a current dcctl") {
		t.Fatalf("old handshake error %v", err)
	}
	f, rt := hpOTASetup(t)
	f.downloads = false
	if err := s.provisionChargerOTA(context.Background(), rt); err == nil {
		t.Fatal("downloads unconfigured accepted")
	}
	f.downloads = true
	f.allowUnverified = true
	if err := s.provisionChargerOTA(context.Background(), rt); err == nil {
		t.Fatal("instance allows unsigned control")
	}
}

func TestHarborpulseS5DocumentsValidate(t *testing.T) {
	schema := servedSchema(t, "update-management")
	docs := []struct {
		doc  string
		vars map[string]any
	}{
		{queryHarborArtifact, map[string]any{"token": "a"}},
		{queryHarborSigners, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100, "state": "ACTIVE"}}},
		{mutationHarborRegisterSigner, map[string]any{"name": "harborpulse", "pem": "public key"}},
		{mutationHarborRevokeSigner, map[string]any{"token": "s"}},
		{mutationHarborCreateArtifact, map[string]any{"request": map[string]any{"token": "a", "kind": "FIRMWARE", "name": "synthetic", "version": "2.4.1", "component": "firmware", "sizeBytes": "49152", "digest": "sha256:abc", "mediaType": "application/octet-stream", "compatibleProfiles": []string{HarborpulseChargerProfile}}}},
		{mutationHarborSignArtifact, map[string]any{"artifact": "a", "signer": "s", "signature": "base64"}},
		{queryHarborUpdateInfo, nil},
		{queryHarborRollout, map[string]any{"token": "r"}},
		{mutationHarborCreateRollout, map[string]any{"request": map[string]any{"token": "r", "name": "rollout", "artifactToken": "a", "groupToken": HarborpulseAllChargersGroup, "maxInFlight": 2, "failureThreshold": 2}}},
		{mutationHarborStartRollout, map[string]any{"token": "r"}},
	}
	for _, d := range docs {
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s: %v", d.doc, errs)
		}
	}
}

func TestHarborpulseS5RolloutErrorStillEmitsTelemetry(t *testing.T) {
	_, rt := hpOTASetup(t)
	s := harborWith(t, HarborpulseDefectOTASignatureOmitted)
	if err := s.provisionChargerOTA(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	accepted := 0
	ingress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { accepted++; w.WriteHeader(http.StatusAccepted) }))
	defer ingress.Close()
	rt.Endpoints.Ingress = ingress.URL
	rt.Devices = []DeviceInstance{{Token: HarborpulseFailedCharger, CredentialId: "credential"}}
	now := time.Now()
	s.started = now.Add(-20 * time.Minute)
	s.now = func() time.Time { return now }
	// The S3b/day schedulers are independent of this once-per-run rollout test.
	s.s3b.done = map[string]bool{}
	for _, beat := range []string{"sampling-reset", "threshold-reset", "depth-withheld", "depth-restored", "calculated-backfill", "sampling-command", "threshold-change"} {
		s.s3b.done[fmt.Sprintf("%d/%s", 1, beat)] = true
	}
	err := s.Tick(context.Background(), rt)
	if err == nil || s.s5Ledger().OTA.RefusalCode != "UNVERIFIED_ARTIFACT_REFUSED" {
		t.Fatalf("rollout refusal was not returned: %v", err)
	}
	if accepted == 0 || rt.Stats.Emitted.Load() == 0 {
		t.Fatal("beat failure suppressed telemetry")
	}
}

func TestHarborpulseS5UploadAndSignatureReadiness(t *testing.T) {
	for _, test := range []struct {
		name         string
		retire, hide bool
	}{
		{"upload not available", true, false}, {"signature not pinned", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, rt := hpOTASetup(t)
			f.retireOnUpload = test.retire
			f.hideSignatures = test.hide
			if err := harborWith(t).provisionChargerOTA(context.Background(), rt); err == nil {
				t.Fatal("bootstrap returned before platform readiness")
			}
		})
	}
}
