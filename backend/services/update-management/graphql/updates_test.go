// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

type devices struct {
	profiles map[string]string
	disabled map[string]bool
}

func (d devices) DeviceOf(_ context.Context, tenant, device string) (model.DeviceFacts, bool, error) {
	p, ok := d.profiles[tenant+"/"+device]
	return model.DeviceFacts{Profile: p, Disabled: d.disabled[tenant+"/"+device]}, ok, nil
}

type sink struct{ n int }

func (s *sink) PublishDesired(context.Context, string, []byte) error { s.n++; return nil }

func updatePlane(t *testing.T, allowUnverified bool) (*plane, *sink) {
	t.Helper()
	p := newPlane(t)
	kp, err := secrets.NewInstanceKeyProvider(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	out := &sink{}
	p.api.Secrets = secrets.NewStore(p.api.RDB.Database, kp)
	p.api.Devices = devices{profiles: map[string]string{"acme/dev-1": "gateway", "acme/dev-2": "other", "acme/dev-off": "gateway", "acme/dev-off2": "gateway", "globex/dev-1": "gateway"},
		disabled: map[string]bool{"acme/dev-off": true}}
	p.api.Desired = out
	p.api.AllowUnverified = allowUnverified
	p.schema = gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", AllowUnverifiedArtifacts: allowUnverified})
	return p, out
}

// available declares an artifact and completes its upload through the model, as the upload
// endpoint does.
func (p *plane) available(token string, content []byte) {
	p.t.Helper()
	ctx := p.ctx("acme", auth.ArtifactWrite)
	require.Empty(p.t, p.exec(ctx, createMutation, createVars(token, content)).Errors)
	a, err := p.api.ArtifactByToken(ctx, token)
	require.NoError(p.t, err)
	ref, err := p.api.Blobs.Put(ctx, blob.Key{Tenant: "acme", Purpose: model.BlobPurpose, ID: token + "-object"},
		bytes.NewReader(content), blob.PutOptions{MaxSize: int64(len(content))})
	require.NoError(p.t, err)
	require.NoError(p.t, p.api.CompleteUpload(ctx, a, ref))
}

const assignMutation = `mutation($d: String!, $a: String!) { assignArtifact(deviceToken: $d, artifactToken: $a) {
  token deviceToken component current supersededAt actor
  artifact { token version state }
  attempt { token state targetVersion targetDigest requiresReboot cancelRequested reconciled progress { bytes total } failure { code } } } }`

func TestAssignRoundTripThroughTheSchema(t *testing.T) {
	p, out := updatePlane(t, true)
	content := []byte("image")
	p.available("fw-1", content)
	writer, reader := p.ctx("acme", auth.ArtifactWrite), p.ctx("acme", auth.ArtifactRead)

	resp := p.exec(writer, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})
	require.Empty(t, resp.Errors)
	var got struct {
		AssignArtifact struct {
			Token, DeviceToken, Component string
			Current                       bool
			SupersededAt                  *string
			Artifact                      struct{ Token, Version, State string }
			Attempt                       struct {
				Token, State, TargetVersion, TargetDigest string
				RequiresReboot, CancelRequested           bool
				Progress                                  *struct{ Bytes, Total string }
				Failure                                   *struct{ Code string }
			}
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &got))
	a := got.AssignArtifact
	require.Equal(t, "dev-1", a.DeviceToken)
	require.Equal(t, "firmware", a.Component)
	require.True(t, a.Current)
	require.Nil(t, a.SupersededAt)
	require.Equal(t, "AVAILABLE", a.Artifact.State)
	require.Equal(t, "QUEUED", a.Attempt.State)
	require.Equal(t, digest(content), a.Attempt.TargetDigest)
	require.True(t, a.Attempt.RequiresReboot)
	require.Nil(t, a.Attempt.Progress)
	require.Nil(t, a.Attempt.Failure)
	require.Equal(t, 1, out.n, "the device was told")

	resp = p.exec(reader, `query($t: String!) { updateAssignment(token: $t) { token attempt { state } }
	  updateAssignments(criteria:{pageNumber:1,pageSize:5,currentOnly:true,deviceToken:"dev-1"}) {
	    results { token } pagination { totalRecords } } }`, map[string]any{"t": a.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignment":{"token":"`+a.Token+`","attempt":{"state":"QUEUED"}},
	  "updateAssignments":{"results":[{"token":"`+a.Token+`"}],"pagination":{"totalRecords":1}}}`, string(resp.Data))

	resp = p.exec(writer, `mutation($t: String!) { cancelUpdate(assignmentToken: $t) { attempt { state } } }`, map[string]any{"t": a.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"cancelUpdate":{"attempt":{"state":"CANCELLED"}}}`, string(resp.Data))
	require.Equal(t, 2, out.n, "the device was told of the cancel")
}

func TestAssignmentOperationsAreGatedByTheirAuthority(t *testing.T) {
	p, _ := updatePlane(t, true)
	p.available("fw-1", []byte("image"))
	reader, writer := p.ctx("acme", auth.ArtifactRead), p.ctx("acme", auth.ArtifactWrite)
	bare := context.Background()

	vars := map[string]any{"d": "dev-1", "a": "fw-1"}
	require.Equal(t, auth.ErrUnauthenticated.Error(), code(p.exec(bare, assignMutation, vars)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, assignMutation, vars)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, `mutation { cancelUpdate(assignmentToken:"x"){ token } }`, nil)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer, `{ updateAssignment(token:"x"){ token } }`, nil)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer,
		`{ updateAssignments(criteria:{pageNumber:1,pageSize:5}){ results { token } } }`, nil)))
}

func TestUpdateManagementInfoSaysWhetherDownloadsCanWork(t *testing.T) {
	p, _ := updatePlane(t, false)
	const q = `{ updateManagementInfo { downloadsConfigured } }`
	resp := p.exec(p.ctx("acme"), q, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"downloadsConfigured":false}}`, string(resp.Data),
		"a plane with no download base must say so, not default to working")

	p.schema = gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", DownloadsConfigured: true})
	resp = p.exec(p.ctx("acme"), q, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"downloadsConfigured":true}}`, string(resp.Data))
}

func TestAssignRefusalsCarryTheirCodes(t *testing.T) {
	p, _ := updatePlane(t, false)
	p.available("fw-1", []byte("image"))
	writer := p.ctx("acme", auth.ArtifactWrite)

	// The instance has not opted into unverified artifacts, and every artifact is unverified.
	require.Equal(t, model.CodeUnverifiedRefused, code(p.exec(writer, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})))
	resp := p.exec(p.ctx("acme"), `{ updateManagementInfo { allowUnverifiedArtifacts } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"allowUnverifiedArtifacts":false}}`, string(resp.Data))

	// Opting in flips only the instance setting; the artifact is the same one.
	p.api.AllowUnverified = true
	p.schema = gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", AllowUnverifiedArtifacts: true})
	resp = p.exec(p.ctx("acme"), `{ updateManagementInfo { allowUnverifiedArtifacts } }`, nil)
	require.JSONEq(t, `{"updateManagementInfo":{"allowUnverifiedArtifacts":true}}`, string(resp.Data))
	require.Equal(t, model.CodeIncompatibleProfile, code(p.exec(writer, assignMutation, map[string]any{"d": "dev-2", "a": "fw-1"})))
	require.Equal(t, model.CodeDeviceNotFound, code(p.exec(writer, assignMutation, map[string]any{"d": "ghost", "a": "fw-1"})))
	require.Equal(t, model.CodeDeviceDisabled, code(p.exec(writer, assignMutation, map[string]any{"d": "dev-off", "a": "fw-1"})),
		"assigning to a disabled device is refused with the typed DEVICE_DISABLED code")
	require.Equal(t, model.CodeArtifactNotFound, code(p.exec(writer, assignMutation, map[string]any{"d": "dev-1", "a": "nope"})))
	require.Equal(t, model.CodeAssignmentNotFound, code(p.exec(writer, `mutation { cancelUpdate(assignmentToken:"nope"){ token } }`, nil)))
}

func TestOtherTenantsCannotSeeOrCancelAnAssignment(t *testing.T) {
	p, _ := updatePlane(t, true)
	p.available("fw-1", []byte("image"))
	resp := p.exec(p.ctx("acme", auth.ArtifactWrite), assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})
	require.Empty(t, resp.Errors)
	var got struct{ AssignArtifact struct{ Token string } }
	require.NoError(t, json.Unmarshal(resp.Data, &got))

	other := p.ctx("globex", auth.ArtifactRead, auth.ArtifactWrite)
	resp = p.exec(other, `query($t: String!) { updateAssignment(token: $t){ token }
	  updateAssignments(criteria:{pageNumber:1,pageSize:5}){ results { token } } }`, map[string]any{"t": got.AssignArtifact.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignment":null,"updateAssignments":{"results":[]}}`, string(resp.Data))
	require.Equal(t, model.CodeAssignmentNotFound, code(p.exec(other,
		`mutation($t: String!) { cancelUpdate(assignmentToken: $t){ token } }`, map[string]any{"t": got.AssignArtifact.Token})))
	require.Equal(t, model.CodeArtifactNotFound, code(p.exec(other, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})))
}

func TestAbandonUnknownThroughTheSchema(t *testing.T) {
	p, out := updatePlane(t, true)
	p.available("fw-1", []byte("image"))
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	p.api.Clock = func() time.Time { return now }
	p.api.Policy = ota.Policy{Acknowledge: time.Hour, Download: time.Hour, Verify: time.Hour, Install: time.Minute, Confirm: time.Hour}
	writer, reader := p.ctx("acme", auth.ArtifactWrite), p.ctx("acme", auth.ArtifactRead)

	resp := p.exec(writer, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})
	require.Empty(t, resp.Errors)
	var got struct {
		AssignArtifact struct {
			Token   string
			Attempt struct{ Token string }
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &got))
	a := got.AssignArtifact
	digestOfImage := digest([]byte("image"))
	_, err := p.api.ApplyReport(writer, "dev-1", ota.Report{V: 1, Kind: ota.KindProgress, AttemptID: a.Attempt.Token,
		AssignmentID: a.Token, Component: "firmware", ArtifactDigest: digestOfImage, Seq: 1, Stage: ota.StageInstalling, BootID: "b"})
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)
	sweep, err := p.api.SweepDeadlines(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, sweep.Unknown)

	const abandon = `mutation($t: String!, $r: String!) { abandonUnknownUpdate(assignmentToken: $t, reason: $r) {
	  attempt { state failure { code detail platform } } } }`
	vars := map[string]any{"t": a.Token, "r": "device was scrapped"}
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, abandon, vars)))
	require.Equal(t, model.CodeInvalidReason, code(p.exec(writer, abandon, map[string]any{"t": a.Token, "r": " "})))
	require.Equal(t, model.CodeAssignmentNotFound, code(p.exec(p.ctx("globex", auth.ArtifactWrite), abandon, vars)))

	before := out.n
	resp = p.exec(writer, abandon, vars)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"abandonUnknownUpdate":{"attempt":{"state":"CANCELLED",
	  "failure":{"code":"OPERATOR_ABANDONED","detail":"device was scrapped","platform":true}}}}`, string(resp.Data))
	require.Equal(t, before+1, out.n, "the device was sent a cancel")
	require.Equal(t, model.CodeAttemptTerminal, code(p.exec(writer, abandon, vars)))
	require.Equal(t, model.CodeAssignmentNotFound, code(p.exec(writer,
		`mutation { abandonUnknownUpdate(assignmentToken:"asg-none", reason:"x"){ token } }`, nil)))
}

func TestSignedByNamesThePinnedSignerAndIsNullWhenNothingIsPinned(t *testing.T) {
	p, _ := updatePlane(t, true)
	content := []byte("image")
	p.available("fw-1", content)
	p.available("fw-2", []byte("image two"))
	writer, reader := p.ctx("acme", auth.ArtifactWrite), p.ctx("acme", auth.ArtifactRead)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	signer, err := p.api.RegisterSigner(writer, "release key", pemText)
	require.NoError(t, err)
	sum := sha256.Sum256(content)
	_, err = p.api.AddSignature(writer, "fw-1", signer.Token, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:])))
	require.NoError(t, err)

	const q = `mutation($d: String!, $a: String!) { assignArtifact(deviceToken: $d, artifactToken: $a) {
	  signedBy { token name keyId algorithm state } } }`
	resp := p.exec(writer, q, map[string]any{"d": "dev-1", "a": "fw-1"})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"assignArtifact":{"signedBy":{"token":"`+signer.Token+`","name":"release key","keyId":"`+
		signer.KeyId+`","algorithm":"ED25519","state":"ACTIVE"}}}`, string(resp.Data))

	// The flag admits an unsigned artifact; nothing is pinned.
	resp = p.exec(writer, q, map[string]any{"d": "dev-off2", "a": "fw-2"})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"assignArtifact":{"signedBy":null}}`, string(resp.Data))

	resp = p.exec(reader, `{ updateAssignments(criteria:{pageNumber:1,pageSize:5,deviceToken:"dev-1"}) { results { signedBy { token } } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignments":{"results":[{"signedBy":{"token":"`+signer.Token+`"}}]}}`, string(resp.Data))

	// A revoke does not move the pin, and signedBy reports the signer's state as it is now.
	_, err = p.api.RevokeSigner(writer, signer.Token)
	require.NoError(t, err)
	resp = p.exec(reader, `{ updateAssignments(criteria:{pageNumber:1,pageSize:5,deviceToken:"dev-1"}) { results { signedBy { token state } } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignments":{"results":[{"signedBy":{"token":"`+signer.Token+`","state":"REVOKED"}}]}}`, string(resp.Data))

	// A pin that cannot be read is an error, never null.
	require.NoError(t, p.api.RDB.DB(writer).Where("id = ?", signer.ID).Delete(&model.ArtifactSigner{}).Error)
	resp = p.exec(reader, `{ updateAssignments(criteria:{pageNumber:1,pageSize:5,deviceToken:"dev-1"}) { results { signedBy { token } } } }`, nil)
	require.NotEmpty(t, resp.Errors)
}

// updateManagementInfo.objectStoreConfigured is read from the catalogue API's store, so an
// instance that runs without one (the area is in the default profile and a store cannot always be
// shared with it) says so instead of looking like a working artifact plane.
func TestUpdateManagementInfoSaysWhetherAnObjectStoreIsConfigured(t *testing.T) {
	p, _ := updatePlane(t, false)
	const q = `{ updateManagementInfo { objectStoreConfigured } }`
	resp := p.exec(p.ctx("acme"), q, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"objectStoreConfigured":true}}`, string(resp.Data))

	p.api.Blobs = nil
	resp = p.exec(p.ctx("acme"), q, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateManagementInfo":{"objectStoreConfigured":false}}`, string(resp.Data),
		"a plane with no store must say so, not default to working")
}
