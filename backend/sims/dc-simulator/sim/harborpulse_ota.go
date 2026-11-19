// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/otafleet"
)

const (
	HarborpulseAllChargersGroup             = "hp-all-chargers"
	HarborpulseFirmwareVersion              = "2.4.1"
	HarborpulseRolloutHour                  = 26.0
	HarborpulseFailedCharger                = "hp-charger-north-1"
	HarborpulseMissingCharger               = "hp-charger-basin-1"
	HarborpulseDefectOTASignatureOmitted    = "ota-signature-omitted"
	HarborpulseDefectOTAGroupMissingCharger = "ota-group-missing-charger"
	HarborpulseDefectOTACapThree            = "ota-cap-three"
	HarborpulseDefectOTAFailureDisabled     = "ota-failure-disabled"
	harborChargerSignerName                 = "harborpulse charger firmware"
	harborRoleCharger                       = "ev-charger"
)

// HarborpulseChargers is the S5 population; it deliberately does not extend S4's group oracle.
func HarborpulseChargers() []string {
	var tokens []string
	for _, d := range harborLayout() {
		if d.kind == harborCharger {
			tokens = append(tokens, d.token)
		}
	}
	return tokens
}

// HarborpulseFirmware is deterministic synthetic content, not executable firmware.
func HarborpulseFirmware() []byte {
	content := make([]byte, 48<<10)
	x := uint32(0x48503241)
	for i := range content {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		content[i] = byte(x)
	}
	return content
}

func HarborpulseFirmwareIdentity() (token, digest string, size int) {
	content := HarborpulseFirmware()
	sum := sha256.Sum256(content)
	h := hex.EncodeToString(sum[:])
	return "hp-charger-fw-2.4.1-" + h[:12], "sha256:" + h, len(content)
}

type harborArtifact struct {
	Token, Digest, Version, Component, SizeBytes, State, TrustStatus string
	CompatibleProfiles                                               []string
	Signatures                                                       []struct {
		Signer            harborSigner
		CountsTowardTrust bool
	}
}
type harborSigner struct{ Token, Name, KeyID, State string }
type harborRollout struct {
	Token, State, GroupToken, CreatedAt, StartedAt string
	MaxInFlight, FailureThreshold                  int
	Artifact                                       struct{ Token string }
}

const queryHarborArtifact = `query HPArtifact($token:String!){artifact(token:$token){token digest version component sizeBytes state trustStatus compatibleProfiles signatures{signer{token keyId state} countsTowardTrust}}}`
const queryHarborSigners = `query HPSigners($c:ArtifactSignerSearchCriteria!){artifactSigners(criteria:$c){results{token name keyId state} pagination{totalRecords}}}`
const mutationHarborRegisterSigner = `mutation HPRegisterSigner($name:String!,$pem:String!){registerArtifactSigner(name:$name,publicKeyPem:$pem){token name keyId state}}`
const mutationHarborRevokeSigner = `mutation HPRevokeSigner($token:String!){revokeArtifactSigner(token:$token){token}}`
const mutationHarborCreateArtifact = `mutation HPCreateArtifact($request:ArtifactCreateRequest!){createArtifact(request:$request){token}}`
const mutationHarborSignArtifact = `mutation HPSignArtifact($artifact:String!,$signer:String!,$signature:String!){addArtifactSignature(artifactToken:$artifact,signerToken:$signer,signature:$signature){token trustStatus}}`
const queryHarborUpdateInfo = `query HPUpdateInfo{updateManagementInfo{downloadsConfigured allowUnverifiedArtifacts}}`
const queryHarborRollout = `query HPRollout($token:String!){updateRollout(token:$token){token state groupToken maxInFlight failureThreshold createdAt startedAt artifact{token}}}`
const mutationHarborCreateRollout = `mutation HPCreateRollout($request:UpdateRolloutCreateRequest!){createUpdateRollout(request:$request){token}}`
const mutationHarborStartRollout = `mutation HPStartRollout($token:String!){startUpdateRollout(token:$token){token}}`

func harborUpdateQuery(ctx context.Context, rt *Runtime, doc string, vars map[string]any, out any) error {
	return rt.Session.Query(ctx, rt.Endpoints.UpdateMgmtGraphQL, doc, vars, out)
}

func readHarborArtifact(ctx context.Context, rt *Runtime, token string) (*harborArtifact, error) {
	var got struct{ Artifact *harborArtifact }
	err := harborUpdateQuery(ctx, rt, queryHarborArtifact, map[string]any{"token": token}, &got)
	return got.Artifact, err
}

// provisionChargerOTA retains only a process-local private key. A retry reads back every write,
// including a registration whose response was lost, before trying it again.
func (s *harborpulse) provisionChargerOTA(ctx context.Context, rt *Runtime) error {
	if strings.TrimSpace(rt.Endpoints.UpdateMgmtGraphQL) == "" {
		return errors.New("harborpulse requires endpoints.updateMgmtGraphQL; re-create the sim with a current dcctl")
	}
	if rt.UpdateFleet != nil {
		return errors.New("harborpulse declares its own update population; --ota cannot also be given")
	}
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	if s.s5.key == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(der)
		s.s5.key = key
		s.s5.publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
		token, digest, size := HarborpulseFirmwareIdentity()
		cap := 2
		if s.defects.otaCapThree {
			cap = 3
		}
		s.s5.ledger = &ScenarioS5{OTA: ScenarioOTA{Artifact: token, Digest: digest, SizeBytes: size, Version: HarborpulseFirmwareVersion,
			KeyID: hex.EncodeToString(sum[:]), Group: HarborpulseAllChargersGroup, MaxInFlight: cap, FailureThreshold: 2, ScheduleHour: HarborpulseRolloutHour}}
	}
	o := &s.s5.ledger.OTA
	var info struct {
		UpdateManagementInfo struct{ DownloadsConfigured, AllowUnverifiedArtifacts bool }
	}
	if err := harborUpdateQuery(ctx, rt, queryHarborUpdateInfo, nil, &info); err != nil {
		return err
	}
	if !info.UpdateManagementInfo.DownloadsConfigured {
		return errors.New("harborpulse OTA downloads are not configured")
	}
	if info.UpdateManagementInfo.AllowUnverifiedArtifacts {
		return errors.New("harborpulse requires the platform to refuse unverified artifacts")
	}
	// Enumerate every active signer before registration, so a lost register response is recoverable.
	for page := 1; ; page++ {
		if page > 100 {
			return errors.New("harborpulse signer search exceeded 100 pages")
		}
		var got struct {
			ArtifactSigners struct {
				Results    []harborSigner
				Pagination struct{ TotalRecords *int }
			}
		}
		c := map[string]any{"pageNumber": page, "pageSize": 100, "state": "ACTIVE"}
		if err := harborUpdateQuery(ctx, rt, queryHarborSigners, map[string]any{"c": c}, &got); err != nil {
			return err
		}
		// Revoke only after collecting, below: mutating a paged result would skip signers.
		sgns := got.ArtifactSigners.Results
		for _, signer := range sgns {
			if signer.KeyID == o.KeyID && signer.State == "ACTIVE" {
				o.Signer = signer.Token
			}
		}
		if got.ArtifactSigners.Pagination.TotalRecords == nil {
			return errors.New("harborpulse signers returned no totalRecords")
		}
		if page*100 >= *got.ArtifactSigners.Pagination.TotalRecords {
			break
		}
		if len(sgns) == 0 {
			return errors.New("harborpulse signer pagination made no progress")
		}
	}
	if o.Signer == "" {
		var got struct{ RegisterArtifactSigner harborSigner }
		if err := harborUpdateQuery(ctx, rt, mutationHarborRegisterSigner, map[string]any{"name": harborChargerSignerName, "pem": s.s5.publicPEM}, &got); err != nil {
			return err
		}
		signer := got.RegisterArtifactSigner
		if signer.Token == "" || signer.KeyID != o.KeyID || signer.State != "ACTIVE" {
			return errors.New("harborpulse signer registration was not confirmed")
		}
		o.Signer = signer.Token
	}
	// Fetch first, revoke second: stable pagination while removing stale keys from the ACTIVE set.
	var stale []string
	for page := 1; page <= 100; page++ {
		var got struct {
			ArtifactSigners struct {
				Results    []harborSigner
				Pagination struct{ TotalRecords *int }
			}
		}
		if err := harborUpdateQuery(ctx, rt, queryHarborSigners, map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": 100, "state": "ACTIVE"}}, &got); err != nil {
			return err
		}
		for _, signer := range got.ArtifactSigners.Results {
			if signer.Name == harborChargerSignerName && signer.State == "ACTIVE" && signer.KeyID != o.KeyID {
				stale = append(stale, signer.Token)
			}
		}
		if got.ArtifactSigners.Pagination.TotalRecords == nil {
			return errors.New("harborpulse signers returned no totalRecords")
		}
		if page*100 >= *got.ArtifactSigners.Pagination.TotalRecords {
			break
		}
		if page == 100 || len(got.ArtifactSigners.Results) == 0 {
			return errors.New("harborpulse stale signer pagination incomplete")
		}
	}
	for _, token := range stale {
		var got any
		if err := harborUpdateQuery(ctx, rt, mutationHarborRevokeSigner, map[string]any{"token": token}, &got); err != nil {
			return err
		}
	}
	artifact, err := readHarborArtifact(ctx, rt, o.Artifact)
	if err != nil {
		return err
	}
	if artifact == nil {
		request := map[string]any{"token": o.Artifact, "kind": "FIRMWARE", "name": "Harborpulse synthetic charger firmware", "version": o.Version, "component": otafleet.DefaultComponent,
			"sizeBytes": strconv.Itoa(o.SizeBytes), "digest": o.Digest, "mediaType": "application/octet-stream", "compatibleProfiles": []string{HarborpulseChargerProfile}}
		var got any
		if err := harborUpdateQuery(ctx, rt, mutationHarborCreateArtifact, map[string]any{"request": request}, &got); err != nil {
			return err
		}
		artifact, err = readHarborArtifact(ctx, rt, o.Artifact)
		if err != nil {
			return err
		}
	}
	if artifact == nil || artifact.Digest != o.Digest || artifact.Version != o.Version || artifact.Component != otafleet.DefaultComponent || artifact.SizeBytes != strconv.Itoa(o.SizeBytes) || len(artifact.CompatibleProfiles) != 1 || artifact.CompatibleProfiles[0] != HarborpulseChargerProfile {
		return errors.New("harborpulse artifact content identity conflicts with the platform catalogue")
	}
	if artifact.State == "UPLOADING" {
		base := strings.TrimSuffix(rt.Endpoints.UpdateMgmtGraphQL, "/graphql")
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/updates/artifacts/"+url.PathEscape(o.Artifact)+"/content", bytes.NewReader(HarborpulseFirmware()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := rt.Session.HTTPClient(base).Do(req)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("harborpulse artifact upload refused: HTTP %d", resp.StatusCode)
		}
		if copyErr != nil {
			return copyErr
		}
	}
	artifact, err = readHarborArtifact(ctx, rt, o.Artifact)
	if err != nil {
		return err
	}
	if artifact == nil || artifact.State != "AVAILABLE" {
		return errors.New("harborpulse artifact upload is not AVAILABLE")
	}
	pinned := false
	for _, sig := range artifact.Signatures {
		if sig.CountsTowardTrust && sig.Signer.Token == o.Signer && sig.Signer.KeyID == o.KeyID && sig.Signer.State == "ACTIVE" {
			pinned = true
		}
	}
	if !s.defects.otaSignatureOmitted && !pinned {
		sum := sha256.Sum256(HarborpulseFirmware())
		signature, err := ecdsa.SignASN1(rand.Reader, s.s5.key, sum[:])
		if err != nil {
			return err
		}
		var got any
		if err := harborUpdateQuery(ctx, rt, mutationHarborSignArtifact, map[string]any{"artifact": o.Artifact, "signer": o.Signer, "signature": base64.StdEncoding.EncodeToString(signature)}, &got); err != nil {
			return err
		}
	}
	artifact, err = readHarborArtifact(ctx, rt, o.Artifact)
	if err != nil {
		return err
	}
	if artifact == nil {
		return errors.New("harborpulse artifact disappeared")
	}
	if s.defects.otaSignatureOmitted {
		if artifact.TrustStatus != "UNVERIFIED" {
			return errors.New("signature-omitted control unexpectedly has a trusted artifact")
		}
	} else {
		pinned = false
		for _, sig := range artifact.Signatures {
			if sig.CountsTowardTrust && sig.Signer.Token == o.Signer && sig.Signer.KeyID == o.KeyID && sig.Signer.State == "ACTIVE" {
				pinned = true
			}
		}
		if artifact.TrustStatus != "SIGNED" || !pinned {
			return errors.New("harborpulse signature was not accepted for the expected signer")
		}
	}
	return nil
}

func (s *harborpulse) UpdatePopulation(_ *Runtime) (*ScenarioUpdateFleet, error) {
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	if s.s5.ledger == nil || s.s5.publicPEM == "" {
		return nil, errors.New("harborpulse OTA has not been provisioned")
	}
	modes := map[string]otafleet.Mode{}
	for _, token := range HarborpulseChargers() {
		modes[token] = otafleet.Healthy
	}
	if !s.defects.otaFailureDisabled {
		modes[HarborpulseFailedCharger] = otafleet.BadHash
	}
	return &ScenarioUpdateFleet{Tokens: HarborpulseChargers(), Options: otafleet.Options{Component: otafleet.DefaultComponent, InitialVersion: "2.4.0", TrustedKeys: []string{s.s5.publicPEM}, ChunkSize: 4096, StepDelay: 2 * time.Second, Modes: modes}}, nil
}

func (s *harborpulse) rolloutChargers(ctx context.Context, rt *Runtime, started time.Time) error {
	o := &s.s5.ledger.OTA
	if o.Rollout == "" {
		o.Rollout = fmt.Sprintf("hp-charger-rollout-%d", started.UnixNano())
	}
	record := func(err error) error {
		var gql *userclient.GraphQLError
		if errors.As(err, &gql) && len(gql.Codes) > 0 {
			o.RefusalCode = gql.Codes[0]
		}
		return err
	}
	read := func() (*harborRollout, error) {
		var got struct{ UpdateRollout *harborRollout }
		err := harborUpdateQuery(ctx, rt, queryHarborRollout, map[string]any{"token": o.Rollout}, &got)
		return got.UpdateRollout, err
	}
	rollout, err := read()
	if err != nil {
		return record(err)
	}
	if rollout == nil {
		request := map[string]any{"token": o.Rollout, "name": "Harborpulse chargers 2.4.1", "artifactToken": o.Artifact, "groupToken": o.Group, "maxInFlight": o.MaxInFlight, "failureThreshold": o.FailureThreshold}
		var got any
		if err := harborUpdateQuery(ctx, rt, mutationHarborCreateRollout, map[string]any{"request": request}, &got); err != nil {
			return record(err)
		}
		rollout, err = read()
		if err != nil {
			return record(err)
		}
	}
	if rollout == nil {
		return errors.New("harborpulse rollout creation was not confirmed")
	}
	if rollout.Artifact.Token != o.Artifact || rollout.GroupToken != o.Group || rollout.MaxInFlight != o.MaxInFlight || rollout.FailureThreshold != o.FailureThreshold {
		return errors.New("harborpulse rollout identity conflicts with the platform")
	}
	o.CreatedAt = rollout.CreatedAt
	if rollout.State == "DRAFT" {
		var got any
		if err := harborUpdateQuery(ctx, rt, mutationHarborStartRollout, map[string]any{"token": o.Rollout}, &got); err != nil {
			return record(err)
		}
		rollout, err = read()
		if err != nil {
			return record(err)
		}
	}
	if rollout == nil || (rollout.State != "RUNNING" && rollout.State != "COMPLETED") {
		return errors.New("harborpulse rollout did not reach RUNNING or COMPLETED")
	}
	o.StartedAt = rollout.StartedAt
	o.RefusalCode = ""
	return nil
}
