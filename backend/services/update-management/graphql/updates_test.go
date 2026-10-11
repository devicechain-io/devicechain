// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

type devices map[string]string

func (d devices) ProfileOf(_ context.Context, tenant, device string) (string, bool, error) {
	p, ok := d[tenant+"/"+device]
	return p, ok, nil
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
	p.api.Devices = devices{"acme/dev-1": "gateway", "acme/dev-2": "other", "globex/dev-1": "gateway"}
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
	  updateAssignments(criteria:{pageNumber:0,pageSize:5,currentOnly:true,deviceToken:"dev-1"}) {
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
		`{ updateAssignments(criteria:{pageNumber:0,pageSize:5}){ results { token } } }`, nil)))
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
	  updateAssignments(criteria:{pageNumber:0,pageSize:5}){ results { token } } }`, map[string]any{"t": got.AssignArtifact.Token})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignment":null,"updateAssignments":{"results":[]}}`, string(resp.Data))
	require.Equal(t, model.CodeAssignmentNotFound, code(p.exec(other,
		`mutation($t: String!) { cancelUpdate(assignmentToken: $t){ token } }`, map[string]any{"t": got.AssignArtifact.Token})))
	require.Equal(t, model.CodeArtifactNotFound, code(p.exec(other, assignMutation, map[string]any{"d": "dev-1", "a": "fw-1"})))
}
