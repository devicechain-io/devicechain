// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

const policyWireFields = `profileToken revision updatedBy updatedAt
 effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds }
 pendingChange { token proposed { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds }
 proposedBy proposedAt effectiveAt state decidedBy decidedAt }`
const policyWireSet = `mutation($settings:AutomationReviewPolicyInput!) {
 setAutomationReviewPolicy(profileToken:"profile",settings:$settings) { applied policy { ` + policyWireFields + ` } } }`

func policyWireRig(t *testing.T) (context.Context, *model.Api, *gqlcore.Schema) {
	t.Helper()
	ctx, api, schema := automationWireRig(t)
	ctx = context.WithValue(ctx, ContextCachedApiKey, model.NewCachedApi(api, &model.Caches{}))
	return ctx, api, schema
}

func TestAutomationReviewPolicyWireDecisions(t *testing.T) {
	ctx, api, schema := policyWireRig(t)
	set := func(required bool) map[string]any {
		t.Helper()
		res := schema.Exec(ctx, policyWireSet, "", map[string]any{"settings": map[string]any{"required": required}})
		require.Empty(t, res.Errors)
		return automationWireData(t, res.Data, "setAutomationReviewPolicy")
	}
	res := schema.Exec(ctx, `{ automationReviewPolicy(profileToken:"profile") { `+policyWireFields+` } }`, "", nil)
	require.Empty(t, res.Errors)
	initial := automationWireData(t, res.Data, "automationReviewPolicy")
	require.Equal(t, float64(0), initial["revision"])
	require.Nil(t, initial["updatedBy"])
	require.Nil(t, initial["updatedAt"])
	require.Nil(t, initial["pendingChange"])
	require.Equal(t, map[string]any{"required": false, "requiredApprovals": float64(1), "allowSelfApproval": false,
		"approvalTtlSeconds": float64(86400), "stepUpMaxAgeSeconds": float64(900)}, initial["effective"])

	tight := set(true)
	require.Equal(t, true, tight["applied"])
	policy := tight["policy"].(map[string]any)
	require.Equal(t, "profile", policy["profileToken"])
	require.Equal(t, float64(1), policy["revision"])
	require.Equal(t, "creator", policy["updatedBy"])
	require.Nil(t, policy["pendingChange"])
	require.Equal(t, true, policy["effective"].(map[string]any)["required"])
	_, err := time.Parse(time.RFC3339Nano, policy["updatedAt"].(string))
	require.NoError(t, err)

	loose := set(false)
	require.Equal(t, false, loose["applied"])
	policy = loose["policy"].(map[string]any)
	require.Equal(t, true, policy["effective"].(map[string]any)["required"])
	pending := policy["pendingChange"].(map[string]any)
	require.Equal(t, "PENDING", pending["state"])
	require.Equal(t, "creator", pending["proposedBy"])
	require.Equal(t, false, pending["proposed"].(map[string]any)["required"])
	require.Nil(t, pending["effectiveAt"])
	require.Nil(t, pending["decidedBy"])
	require.Nil(t, pending["decidedAt"])
	const approve = `mutation($token:String!) { approveAutomationReviewPolicyChange(token:$token) { ` + policyWireFields + ` } }`
	vars := map[string]any{"token": pending["token"]}
	res = schema.Exec(ctx, approve, "", vars)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "SELF_APPROVAL_REFUSED", res.Errors[0].Extensions["code"])
	second := automationWireClaims("second")
	second.MfaTime = nil
	res = schema.Exec(auth.WithClaims(ctx, second), approve, "", vars)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "STEP_UP_REQUIRED", res.Errors[0].Extensions["code"])
	res = schema.Exec(auth.WithClaims(ctx, automationWireClaims("second")), approve, "", vars)
	require.Empty(t, res.Errors)
	policy = automationWireData(t, res.Data, "approveAutomationReviewPolicyChange")
	require.Equal(t, "profile", policy["profileToken"])
	require.Equal(t, false, policy["effective"].(map[string]any)["required"])
	require.Equal(t, float64(2), policy["revision"])
	require.Nil(t, policy["pendingChange"])
	var stored model.AutomationReviewPolicyChange
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", pending["token"]).First(&stored).Error)
	require.Equal(t, "APPLIED", stored.State)
	require.Equal(t, "second", stored.DecidedBy.String)
	require.True(t, stored.DecidedAt.Valid)

	set(true)
	loose = set(false)
	pending = loose["policy"].(map[string]any)["pendingChange"].(map[string]any)
	res = schema.Exec(ctx, `mutation($token:String!) { cancelAutomationReviewPolicyChange(token:$token) { `+policyWireFields+` } }`, "", map[string]any{"token": pending["token"]})
	require.Empty(t, res.Errors)
	policy = automationWireData(t, res.Data, "cancelAutomationReviewPolicyChange")
	require.Equal(t, "profile", policy["profileToken"])
	require.Equal(t, true, policy["effective"].(map[string]any)["required"])
	require.Nil(t, policy["pendingChange"])
	stored = model.AutomationReviewPolicyChange{}
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", pending["token"]).First(&stored).Error)
	require.Equal(t, "CANCELLED", stored.State)
	require.Equal(t, "creator", stored.DecidedBy.String)
	require.True(t, stored.DecidedAt.Valid)
}

func TestAutomationReviewPolicyWireExplicitSettingsAndDelayedLoosening(t *testing.T) {
	ctx, _, schema := policyWireRig(t)
	settings := map[string]any{"required": true, "requiredApprovals": 2, "allowSelfApproval": true,
		"approvalTtlSeconds": 1800, "stepUpMaxAgeSeconds": 120}
	res := schema.Exec(ctx, policyWireSet, "", map[string]any{"settings": settings})
	require.Empty(t, res.Errors)
	result := automationWireData(t, res.Data, "setAutomationReviewPolicy")
	require.Equal(t, true, result["applied"])
	policy := result["policy"].(map[string]any)
	require.Equal(t, map[string]any{"required": true, "requiredApprovals": float64(2), "allowSelfApproval": true,
		"approvalTtlSeconds": float64(1800), "stepUpMaxAgeSeconds": float64(120)}, policy["effective"])
	settings["requiredApprovals"] = 1
	res = schema.Exec(ctx, policyWireSet, "", map[string]any{"settings": settings})
	require.Empty(t, res.Errors)
	result = automationWireData(t, res.Data, "setAutomationReviewPolicy")
	require.Equal(t, false, result["applied"])
	policy = result["policy"].(map[string]any)
	require.Equal(t, float64(2), policy["effective"].(map[string]any)["requiredApprovals"])
	pending := policy["pendingChange"].(map[string]any)
	require.Equal(t, "PENDING", pending["state"])
	require.Equal(t, float64(1), pending["proposed"].(map[string]any)["requiredApprovals"])
	proposedAt, err := time.Parse(time.RFC3339Nano, pending["proposedAt"].(string))
	require.NoError(t, err)
	effectiveAt, err := time.Parse(time.RFC3339Nano, pending["effectiveAt"].(string))
	require.NoError(t, err)
	require.InDelta(t, (24 * time.Hour).Seconds(), effectiveAt.Sub(proposedAt).Seconds(), 1)
}

type policyWireStanding struct{}

func (policyWireStanding) PrincipalStanding(_ context.Context, principals, _ []string) (map[string]bool, error) {
	out := make(map[string]bool, len(principals))
	for _, p := range principals {
		out[p] = true
	}
	return out, nil
}

func TestAutomationReviewPolicyWireProtectedPublish(t *testing.T) {
	ctx, api, schema := policyWireRig(t)
	res := schema.Exec(ctx, policyWireSet, "", map[string]any{"settings": map[string]any{"required": true}})
	require.Empty(t, res.Errors)
	res = schema.Exec(ctx, `mutation { publishDeviceProfile(token:"profile") { version } }`, "", nil)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "REVIEW_REQUIRED", res.Errors[0].Extensions["code"])
	res = schema.Exec(ctx, `mutation { rollbackDeviceProfile(token:"profile",version:1) { token } }`, "", nil)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "BREAK_GLASS_REASON_REQUIRED", res.Errors[0].Extensions["code"])

	res = schema.Exec(ctx, automationRunDoc, "", map[string]any{"input": automationWireInput()})
	require.Empty(t, res.Errors)
	review := automationWireData(t, res.Data, "runAutomationReview")
	res = schema.Exec(auth.WithClaims(ctx, automationWireClaims("reviewer")), `mutation($token:String!,$digest:String!) {
	 approveAutomationReview(token:$token,digest:$digest) { state consumedVersion }
	}`, "", map[string]any{"token": review["token"], "digest": review["reviewDigest"]})
	require.Empty(t, res.Errors)
	require.Nil(t, automationWireData(t, res.Data, "approveAutomationReview")["consumedVersion"])
	api.PrincipalStanding = policyWireStanding{}
	res = schema.Exec(ctx, `mutation($token:String!) { publishDeviceProfile(token:"profile",label:"reviewed",description:"approved draft",automationReviewToken:$token) {
	 version label description automationReviewToken automationReview { token consumedVersion }
	} }`, "", map[string]any{"token": review["token"]})
	require.Empty(t, res.Errors)
	version := automationWireData(t, res.Data, "publishDeviceProfile")
	require.Equal(t, float64(2), version["version"])
	require.Equal(t, "reviewed", version["label"])
	require.Equal(t, "approved draft", version["description"])
	require.Equal(t, review["token"], version["automationReviewToken"])
	require.Equal(t, map[string]any{"token": review["token"], "consumedVersion": float64(2)}, version["automationReview"])

	reader := automationWireClaims("reader")
	reader.Authorities = []string{string(auth.DeviceRead)}
	res = schema.Exec(auth.WithClaims(ctx, reader), `{ deviceProfileVersions(token:"profile") {
	 version automationReviewToken automationReview { token consumedVersion }
	} }`, "", nil)
	require.Len(t, res.Errors, 1)
	require.ErrorIs(t, res.Errors[0].ResolverError, auth.ErrForbidden)
	require.Equal(t, []any{"deviceProfileVersions", 0, "automationReview"}, res.Errors[0].Path)
	var data map[string]any
	require.NoError(t, json.Unmarshal(res.Data, &data))
	versions := data["deviceProfileVersions"].([]any)
	require.Len(t, versions, 2)
	require.Equal(t, map[string]any{"version": float64(2), "automationReviewToken": review["token"], "automationReview": nil}, versions[0])
	require.Equal(t, map[string]any{"version": float64(1), "automationReviewToken": nil, "automationReview": nil}, versions[1])

	res = schema.Exec(ctx, `mutation { rollbackDeviceProfile(token:"profile",version:1,breakGlassReason:"recover service") { token activeVersion } }`, "", nil)
	require.Empty(t, res.Errors)
	res = schema.Exec(auth.WithClaims(ctx, reader), `{ automationReviewBreakGlass(profileToken:"profile") { action target reason actor at } }`, "", nil)
	require.Empty(t, res.Errors)
	require.NoError(t, json.Unmarshal(res.Data, &data))
	history := data["automationReviewBreakGlass"].([]any)
	require.Len(t, history, 1)
	entry := history[0].(map[string]any)
	require.Equal(t, "ROLLBACK", entry["action"])
	require.Equal(t, "recover service", entry["reason"])
	require.Equal(t, "creator", entry["actor"])
	_, err := time.Parse(time.RFC3339Nano, entry["at"].(string))
	require.NoError(t, err)
}

func TestAutomationReviewPolicyWireReadAuthorizationAndCorruptProposal(t *testing.T) {
	ctx, api, schema := policyWireRig(t)
	reader := automationWireClaims("reader")
	reader.Authorities = []string{string(auth.DeviceRead)}
	for _, op := range []string{"automationReviewPolicy", "automationReviewBreakGlass"} {
		selection := "profileToken"
		if op == "automationReviewBreakGlass" {
			selection = "action"
		}
		doc := `{ ` + op + `(profileToken:"profile") { ` + selection + ` } }`
		res := schema.Exec(auth.WithClaims(ctx, reader), doc, "", nil)
		require.Empty(t, res.Errors)
		scoped := automationWireClaims("scoped")
		scoped.ScopeAnchors = []string{"customer:c1"}
		res = schema.Exec(auth.WithClaims(ctx, scoped), doc, "", nil)
		require.Len(t, res.Errors, 1)
		require.Equal(t, "SCOPED_GRANT_NOT_SUPPORTED", res.Errors[0].Extensions["code"])
		res = schema.Exec(ctx, `{ `+op+`(profileToken:"missing") { `+selection+` } }`, "", nil)
		require.Len(t, res.Errors, 1)
		require.Equal(t, "NOT_FOUND", res.Errors[0].Extensions["code"])
		noRead := automationWireClaims("no-read")
		noRead.Authorities = nil
		res = schema.Exec(auth.WithClaims(ctx, noRead), doc, "", nil)
		require.Len(t, res.Errors, 1)
		require.ErrorIs(t, res.Errors[0].ResolverError, auth.ErrForbidden)
	}
	for _, doc := range []string{
		`mutation { setAutomationReviewPolicy(profileToken:"profile",settings:{required:true}) { applied } }`,
		`mutation { approveAutomationReviewPolicyChange(token:"missing") { revision } }`,
		`mutation { cancelAutomationReviewPolicyChange(token:"missing") { revision } }`,
	} {
		res := schema.Exec(auth.WithClaims(ctx, reader), doc, "", nil)
		require.Len(t, res.Errors, 1)
		require.ErrorIs(t, res.Errors[0].ResolverError, auth.ErrForbidden)
	}
	for _, required := range []bool{true, false} {
		res := schema.Exec(ctx, policyWireSet, "", map[string]any{"settings": map[string]any{"required": required}})
		require.Empty(t, res.Errors)
	}
	for _, raw := range []string{"{broken", "null", `{}`, `{"required":null}`, `{"required":"broken","requiredApprovals":1,"allowSelfApproval":false,"approvalTtlSeconds":86400,"stepUpMaxAgeSeconds":900}`} {
		require.NoError(t, api.RDB.DB(ctx).Model(&model.AutomationReviewPolicyChange{}).Where("state = ?", "PENDING").Update("proposed", raw).Error)
		res := schema.Exec(ctx, `{ automationReviewPolicy(profileToken:"profile") { profileToken pendingChange { token proposed { required } } } }`, "", nil)
		require.Len(t, res.Errors, 1)
		require.Equal(t, []any{"automationReviewPolicy", "pendingChange", "proposed"}, res.Errors[0].Path)
		policy := automationWireData(t, res.Data, "automationReviewPolicy")
		require.Equal(t, "profile", policy["profileToken"])
		require.Nil(t, policy["pendingChange"].(map[string]any)["proposed"])
	}
}

func TestAutomationReviewBreakGlassWireBoundedTenantHistory(t *testing.T) {
	ctx, api, schema := policyWireRig(t)
	var profile model.DeviceProfile
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "profile").First(&profile).Error)
	other := &model.DeviceProfile{TokenReference: rdb.TokenReference{Token: "other"}}
	require.NoError(t, api.RDB.DB(ctx).Create(other).Error)
	foreignCtx := core.WithTenant(ctx, "globex")
	foreign := &model.DeviceProfile{TokenReference: rdb.TokenReference{Token: "profile"}}
	require.NoError(t, api.RDB.DB(foreignCtx).Create(foreign).Error)
	at := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 202; i++ {
		row := &model.AutomationReviewBreakGlass{DeviceProfileId: profile.ID, Action: "ROLLBACK", Target: "profile",
			Reason: fmt.Sprintf("reason-%03d", i), Actor: "person", CreatedAt: at, Amr: datatypes.JSON("[]")}
		if i == 0 {
			row.CreatedAt = at.Add(time.Hour)
		}
		require.NoError(t, api.RDB.DB(ctx).Create(row).Error)
	}
	for _, c := range []struct {
		ctx context.Context
		id  uint
	}{{ctx, other.ID}, {foreignCtx, foreign.ID}} {
		row := &model.AutomationReviewBreakGlass{DeviceProfileId: c.id, Action: "ROLLBACK", Target: "decoy", Reason: "decoy", Actor: "other", CreatedAt: at.Add(time.Hour), Amr: datatypes.JSON("[]")}
		require.NoError(t, api.RDB.DB(c.ctx).Create(row).Error)
	}
	res := schema.Exec(ctx, `{ automationReviewBreakGlass(profileToken:"profile") { reason } }`, "", nil)
	require.Empty(t, res.Errors)
	var data map[string]any
	require.NoError(t, json.Unmarshal(res.Data, &data))
	rows := data["automationReviewBreakGlass"].([]any)
	require.Len(t, rows, 200)
	for i, row := range rows {
		want := 202 - i
		if i == 0 {
			want = 0
		}
		require.Equal(t, fmt.Sprintf("reason-%03d", want), row.(map[string]any)["reason"])
	}
}
