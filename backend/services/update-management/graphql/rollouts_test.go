// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

// groups answers every group of a tenant with a fixed member list.
type groups map[string][]string

func (g groups) ResolveGroupTargets(ctx context.Context, group string, _ *int32, _ string, _ int) (*model.GroupTargetPage, error) {
	tenant, _ := core.TenantFromContext(ctx)
	members, ok := g[tenant+"/"+group]
	if !ok {
		return &model.GroupTargetPage{Rejected: true, Code: "GROUP_NOT_FOUND", Reason: "no such group"}, nil
	}
	return &model.GroupTargetPage{DeviceTokens: members}, nil
}

const rolloutFields = `token name groupToken groupVersion maxInFlight failureThreshold state pauseReason targetCount
  createdBy stateChangedBy startedAt finishedAt artifact { token }
  progress { total pending inFlight succeeded failed unknown cancelled skipped }`

func rolloutPlane(t *testing.T) (*plane, *sink) {
	t.Helper()
	p, out := updatePlane(t, true)
	p.api.Groups = groups{"acme/fleet": {"dev-2", "dev-off", "dev-1", "dev-off2"}}
	p.available("fw-1", []byte("image"))
	return p, out
}

func TestRolloutOperationsAreGatedByTheirAuthority(t *testing.T) {
	p, _ := rolloutPlane(t)
	reader, writer := p.ctx("acme", auth.ArtifactRead), p.ctx("acme", auth.ArtifactWrite)
	create := `mutation { createUpdateRollout(request: {token: "r1", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1}) { token } }`
	for _, q := range []string{create,
		`mutation { startUpdateRollout(token: "r1") { token } }`,
		`mutation { pauseUpdateRollout(token: "r1") { token } }`,
		`mutation { resumeUpdateRollout(token: "r1") { token } }`,
		`mutation { abortUpdateRollout(token: "r1") { token } }`,
	} {
		require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, q, nil)), "read does not grant %s", q)
	}
	for _, q := range []string{
		`{ updateRollout(token: "r1") { token } }`,
		`{ updateRollouts(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`,
		`{ updateRolloutTargets(token: "r1", criteria: {pageNumber: 1, pageSize: 5}) { results { deviceToken } } }`,
	} {
		require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer, q, nil)), "write does not grant %s", q)
	}
	require.Empty(t, p.exec(writer, create, nil).Errors)
}

func TestARolloutRoundTripsThroughTheSchema(t *testing.T) {
	p, out := rolloutPlane(t)
	writer, reader := p.ctx("acme", auth.ArtifactWrite), p.ctx("acme", auth.ArtifactRead)
	writer = auth.WithClaims(writer, &auth.Claims{Tenant: "acme", Username: "alice", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.ArtifactWrite)}})

	resp := p.exec(writer, `mutation($r: UpdateRolloutCreateRequest!) { createUpdateRollout(request: $r) { `+rolloutFields+` } }`,
		map[string]any{"r": map[string]any{"token": "r1", "name": "spring wave", "artifactToken": "fw-1",
			"groupToken": "fleet", "maxInFlight": 1, "failureThreshold": 2}})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"createUpdateRollout":{"token":"r1","name":"spring wave","groupToken":"fleet","groupVersion":null,
	  "maxInFlight":1,"failureThreshold":2,"state":"DRAFT","pauseReason":null,"targetCount":0,"createdBy":"alice",
	  "stateChangedBy":null,"startedAt":null,"finishedAt":null,"artifact":{"token":"fw-1"},
	  "progress":{"total":0,"pending":0,"inFlight":0,"succeeded":0,"failed":0,"unknown":0,"cancelled":0,"skipped":0}}}`,
		string(resp.Data))

	resp = p.exec(writer, `mutation { startUpdateRollout(token: "r1") { state targetCount stateChangedBy
	  progress { total pending inFlight skipped } } }`, nil)
	require.Empty(t, resp.Errors)
	// dev-2's profile is incompatible and dev-off is disabled, so both are skipped without holding
	// the slot, which goes to dev-1 (the cap is 1); dev-off2 waits.
	require.JSONEq(t, `{"startUpdateRollout":{"state":"RUNNING","targetCount":4,"stateChangedBy":"alice",
	  "progress":{"total":4,"pending":1,"inFlight":1,"skipped":2}}}`, string(resp.Data))
	require.Equal(t, 1, out.n, "one assignment went down the device channel")

	resp = p.exec(reader, `{ updateRolloutTargets(token: "r1", criteria: {pageNumber: 1, pageSize: 10, outcome: SKIPPED}) {
	  results { deviceToken outcome skipCode assignmentToken attemptState } pagination { totalRecords } } }`, nil)
	require.Empty(t, resp.Errors)
	var targets struct {
		UpdateRolloutTargets struct {
			Results []struct {
				DeviceToken, Outcome string
				SkipCode             *string
				AssignmentToken      *string
				AttemptState         *string
			}
			Pagination struct{ TotalRecords int }
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &targets))
	require.Equal(t, 2, targets.UpdateRolloutTargets.Pagination.TotalRecords)
	codes := map[string]string{}
	for _, r := range targets.UpdateRolloutTargets.Results {
		require.Equal(t, "SKIPPED", r.Outcome)
		require.Nil(t, r.AssignmentToken)
		require.Nil(t, r.AttemptState)
		codes[r.DeviceToken] = *r.SkipCode
	}
	require.Equal(t, map[string]string{"dev-2": model.CodeIncompatibleProfile, "dev-off": model.CodeDeviceDisabled}, codes)

	resp = p.exec(reader, `{ updateRolloutTargets(token: "r1", criteria: {pageNumber: 1, pageSize: 10, outcome: IN_FLIGHT}) {
	  results { deviceToken assignmentToken attemptState } } }`, nil)
	require.Empty(t, resp.Errors)
	require.Contains(t, string(resp.Data), `"deviceToken":"dev-1"`)
	require.Contains(t, string(resp.Data), `"attemptState":"QUEUED"`)

	resp = p.exec(writer, `mutation { pauseUpdateRollout(token: "r1") { state pauseReason } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"pauseUpdateRollout":{"state":"PAUSED","pauseReason":"OPERATOR"}}`, string(resp.Data))
	resp = p.exec(writer, `mutation { resumeUpdateRollout(token: "r1", failureThreshold: 5) { state pauseReason failureThreshold } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"resumeUpdateRollout":{"state":"RUNNING","pauseReason":null,"failureThreshold":5}}`, string(resp.Data))
	resp = p.exec(writer, `mutation { abortUpdateRollout(token: "r1") { state finishedAt progress { inFlight cancelled } } }`, nil)
	require.Empty(t, resp.Errors)
	require.Contains(t, string(resp.Data), `"state":"ABORTED"`)
	require.Contains(t, string(resp.Data), `"progress":{"inFlight":1,"cancelled":1}`)

	resp = p.exec(reader, `{ updateRollouts(criteria: {pageNumber: 1, pageSize: 5, state: ABORTED}) { results { token } pagination { totalRecords } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateRollouts":{"results":[{"token":"r1"}],"pagination":{"totalRecords":1}}}`, string(resp.Data))
}

func TestRolloutRefusalsCarryTheirCodesAndOtherTenantsSeeNothing(t *testing.T) {
	p, _ := rolloutPlane(t)
	writer := p.ctx("acme", auth.ArtifactWrite)
	create := `mutation($r: UpdateRolloutCreateRequest!) { createUpdateRollout(request: $r) { token } }`
	vars := func(maxInFlight int, group string) map[string]any {
		return map[string]any{"r": map[string]any{"token": "r1", "artifactToken": "fw-1", "groupToken": group, "maxInFlight": maxInFlight}}
	}
	require.Equal(t, model.CodeInvalidRollout, code(p.exec(writer, create, vars(0, "fleet"))))
	require.Empty(t, p.exec(writer, create, vars(1, "nowhere")).Errors)
	require.Equal(t, model.CodeRolloutGroupUnusable, code(p.exec(writer, `mutation { startUpdateRollout(token: "r1") { token } }`, nil)))
	require.Equal(t, model.CodeRolloutStateConflict, code(p.exec(writer, `mutation { pauseUpdateRollout(token: "r1") { token } }`, nil)))

	other := p.ctx("globex", auth.ArtifactRead, auth.ArtifactWrite)
	resp := p.exec(other, `{ updateRollout(token: "r1") { token } updateRolloutTargets(token: "r1", criteria: {pageNumber: 1, pageSize: 5}) { results { deviceToken } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateRollout":null,"updateRolloutTargets":null}`, string(resp.Data))
	require.Equal(t, model.CodeRolloutNotFound, code(p.exec(other, `mutation { abortUpdateRollout(token: "r1") { token } }`, nil)))
}
