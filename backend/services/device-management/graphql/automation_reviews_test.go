// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// The stored engine JSON uses nested bounds and nanosecond durations, unlike
// the flattened GraphQL comparison response. Pin those distinctions on the wire.
const automationReviewResultJSON = `{
  "outcome":"PASSED",
  "fidelity":{"status":"SUPPORTED","reasons":[],"label":"Recorded inputs"},
  "expectations":[{"index":0,"name":"catch","window":"INCIDENT","verdict":"PASS","baseline":"FAIL","reason":"caught","baselineReason":"missed","evidence":[{"device":"d1","at":"2026-11-10T08:00:00Z","value":26.5,"hasValue":true,"span":2500000000}]}],
  "windows":[{
    "role":"INCIDENT","start":"2026-11-10T08:00:00Z","end":"2026-11-10T09:00:00Z","leadInStart":"2026-11-10T07:00:00Z",
    "bounds":{"role":"INCIDENT","stream":"resolved","firstSeq":18446744073709551614,"lastSeq":18446744073709551615},
    "inputDigest":"inputs","degraded":[],"counts":{"unchanged":2,"added":0,"removed":0,"shifted":0,"contentChanged":1},"changesTruncated":false,
    "devices":[{"device":"d1","readings":{"z":1,"temp_c":3},"versions":{"profile@1":4},"stateBase":"CLEAR","stateDraft":"RAISED","lateBase":0,"lateDraft":1,"first":"2026-11-10T08:00:00Z","last":"2026-11-10T08:30:00Z","divergence":"2026-11-10T08:15:00Z"}],
    "changes":[{"kind":"CONTENT_CHANGED","device":"d1","subject":"r1","discriminator":"command","base":"2026-11-10T08:00:00Z","draft":"2026-11-10T08:00:01.5Z","shift":1500000000,"baseContent":{"payload":"old","relativeExpiry":3000000000},"draftContent":{"payload":"new"},"contentChanged":true,"reading":{"device":"d1","at":"2026-11-10T08:00:00Z","hasValue":false,"value":0,"span":500000000},"afterDivergence":true}]
  }]
}`

type automationWireComparer struct{}

func (automationWireComparer) CompareRuleVersions(context.Context, model.CompareRequest) (*model.CompareResponse, error) {
	return &model.CompareResponse{Ok: true, Outcome: model.ReviewOutcomePassed,
		Result: automationReviewResultJSON, ResultDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(automationReviewResultJSON))),
		InputDigest: "inputs", EngineBuild: "wire-engine",
		Windows: []model.ReviewWindowResult{{Role: "INCIDENT", Stream: "resolved", FirstSeq: "18446744073709551614", LastSeq: "18446744073709551615"}}}, nil
}

func automationWireClaims(name string) *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: name,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)},
		Amr:         auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(time.Now())}
}

func automationWireRig(t *testing.T) (context.Context, *model.Api, *gqlcore.Schema) {
	t.Helper()
	ctx := profileLocationCtx(t)
	api := (&SchemaResolver{}).GetApi(ctx)
	require.NoError(t, api.RDB.Database.AutoMigrate(&model.AutomationReview{}, &rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	sqlDB, err := api.RDB.Database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	before := auth.WithClaims(ctx, automationWireClaims("before"))
	profile := &model.DeviceProfile{}
	profile.Token = "profile"
	require.NoError(t, api.RDB.DB(before).Create(profile).Error)
	rule := &model.DetectionRule{DeviceProfileId: profile.ID, Enabled: true, Definition: datatypes.JSON(`{"threshold":26}`)}
	rule.Token = "r1"
	require.NoError(t, api.RDB.DB(before).Create(rule).Error)
	_, err = api.PublishDeviceProfile(before, "profile", nil, nil, "before")
	require.NoError(t, err)
	editor := auth.WithClaims(ctx, automationWireClaims("editor"))
	rule.Definition = datatypes.JSON(`{"threshold":25.5}`)
	require.NoError(t, api.RDB.DB(editor).Save(rule).Error)
	api.RuleComparer = automationWireComparer{}
	return auth.WithClaims(ctx, automationWireClaims("creator")), api, gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
}

func automationWireInput() map[string]any {
	return map[string]any{"profileToken": "profile", "ruleToken": "r1", "deviceTokens": []any{"d1"}, "requestId": "wire-1", "shiftWindowSeconds": 15,
		"windows": []any{
			map[string]any{"role": "INCIDENT", "start": "2026-11-10T08:00:00Z", "end": "2026-11-10T09:00:00Z"},
			map[string]any{"role": "NORMAL", "start": "2026-11-09T08:00:00Z", "end": "2026-11-09T09:00:00Z"}},
		"expectations": []any{map[string]any{"window": "INCIDENT", "name": "catch", "expect": `{"raised":{"rule":"r1"}}`}}}
}

const automationRunDoc = `mutation($input:RunAutomationReviewInput!) { runAutomationReview(input:$input) {
  token profileToken kind ruleToken state outcome reviewDigest draftDigest baselineDigest baselineVersion createdBy createdAt requiredApprovals
  approvals { principal approvedAt policyRevision } approvedAt expiresAt decidedBy decidedAt note draftAuthors profileChanges { area key change automation } reasons
  deviceTokens windows { role start end } shiftWindowSeconds expectationInputs { window name expect }
  fidelity { status reasons label } expectations { index name window verdict baseline reason baselineReason evidence { device at value spanSeconds } }
  windowResults { role start end leadInStart stream firstSeq lastSeq inputDigest degraded counts { unchanged added removed shifted contentChanged } changesTruncated
    devices { device readings { key count } versions { key count } stateBase stateDraft divergence lateBase lateDraft first last }
    changes { kind device subject discriminator baseAt draftAt shiftSeconds baseContent { severity payload relativeExpirySeconds expiryLabel configDigest bodyDigest method }
      draftContent { payload } contentChanged reading { device at value spanSeconds } afterDivergence } }
  inputDigest resultDigest engineBuild result matchesCurrentDraft
} }`

func automationWireData(t *testing.T, raw json.RawMessage, field string) map[string]any {
	t.Helper()
	var data map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	v, ok := data[field].(map[string]any)
	require.True(t, ok, "%s: %s", field, raw)
	return v
}

func TestAutomationReviewWireEvidenceAndDecisions(t *testing.T) {
	ctx, api, schema := automationWireRig(t)
	res := schema.Exec(ctx, automationRunDoc, "", map[string]any{"input": automationWireInput()})
	require.Empty(t, res.Errors)
	review := automationWireData(t, res.Data, "runAutomationReview")
	require.Equal(t, "PASSED", review["outcome"])
	require.Equal(t, "profile", review["profileToken"])
	require.Equal(t, true, review["matchesCurrentDraft"])
	require.Equal(t, []any{"editor"}, review["draftAuthors"])
	require.Equal(t, []any{}, review["reasons"])
	require.Equal(t, automationReviewResultJSON, review["result"])
	require.Equal(t, float64(15), review["shiftWindowSeconds"])
	window := review["windowResults"].([]any)[0].(map[string]any)
	require.Equal(t, "18446744073709551615", window["lastSeq"])
	change := window["changes"].([]any)[0].(map[string]any)
	require.Equal(t, "CONTENT_CHANGED", change["kind"])
	require.Equal(t, "old", change["baseContent"].(map[string]any)["payload"])
	require.Equal(t, float64(3), change["baseContent"].(map[string]any)["relativeExpirySeconds"])
	require.Equal(t, 1.5, change["shiftSeconds"])
	require.Nil(t, change["reading"].(map[string]any)["value"])
	require.Equal(t, 0.5, change["reading"].(map[string]any)["spanSeconds"])
	coverage := window["devices"].([]any)[0].(map[string]any)
	require.Equal(t, []any{map[string]any{"key": "temp_c", "count": float64(3)}, map[string]any{"key": "z", "count": float64(1)}}, coverage["readings"])
	require.Equal(t, []any{map[string]any{"key": "profile@1", "count": float64(4)}}, coverage["versions"])
	expectation := review["expectations"].([]any)[0].(map[string]any)
	require.Equal(t, "PASS", expectation["verdict"])
	require.Equal(t, 26.5, expectation["evidence"].([]any)[0].(map[string]any)["value"])
	require.Equal(t, 2.5, expectation["evidence"].([]any)[0].(map[string]any)["spanSeconds"])
	const approve = `mutation($token:String!,$digest:String!) { approveAutomationReview(token:$token,digest:$digest) { state approvals { principal policyRevision } } }`
	vars := map[string]any{"token": review["token"], "digest": review["reviewDigest"]}
	res = schema.Exec(ctx, approve, "", vars)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "SELF_APPROVAL_REFUSED", res.Errors[0].Extensions["code"])
	res = schema.Exec(auth.WithClaims(ctx, automationWireClaims("editor")), approve, "", vars)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "SELF_APPROVAL_REFUSED", res.Errors[0].Extensions["code"])
	res = schema.Exec(auth.WithClaims(ctx, automationWireClaims("third")), approve, "", vars)
	require.Empty(t, res.Errors)
	approved := automationWireData(t, res.Data, "approveAutomationReview")
	require.Equal(t, "APPROVED", approved["state"])
	require.Equal(t, []any{map[string]any{"principal": "third", "policyRevision": float64(0)}}, approved["approvals"])

	// Independent decoys ensure both search predicates are actually applied.
	row, err := api.AutomationReviewByToken(ctx, review["token"].(string))
	require.NoError(t, err)
	stale := *row
	stale.ID, stale.Token, stale.RequestId, stale.DraftDigest = 0, "acr_stale", "stale", "sha256:stale"
	require.NoError(t, api.RDB.DB(ctx).Create(&stale).Error)
	pending := *row
	pending.ID, pending.Token, pending.RequestId, pending.State = 0, "acr_pending", "pending", model.ReviewStateRan
	require.NoError(t, api.RDB.DB(ctx).Create(&pending).Error)
	const search = `query($criteria:AutomationReviewSearchCriteria!) { automationReviews(criteria:$criteria) { results { token } pagination { totalRecords } } }`
	for _, tc := range []struct {
		state string
		match bool
		token string
	}{
		{"APPROVED", true, row.Token}, {"APPROVED", false, stale.Token}, {"RAN", true, pending.Token},
	} {
		res = schema.Exec(ctx, search, "", map[string]any{"criteria": map[string]any{"profileToken": "profile", "state": tc.state, "matchingCurrentDraft": tc.match, "pageNumber": 1, "pageSize": 10}})
		require.Empty(t, res.Errors)
		found := automationWireData(t, res.Data, "automationReviews")
		require.Equal(t, []any{map[string]any{"token": tc.token}}, found["results"])
		require.Equal(t, float64(1), found["pagination"].(map[string]any)["totalRecords"])
	}
	res = schema.Exec(ctx, `query($token:String!) { automationReview(token:$token) { token state matchesCurrentDraft } }`, "", map[string]any{"token": row.Token})
	require.Empty(t, res.Errors)
	require.Equal(t, map[string]any{"token": row.Token, "state": "APPROVED", "matchesCurrentDraft": true}, automationWireData(t, res.Data, "automationReview"))
	res = schema.Exec(auth.WithClaims(ctx, automationWireClaims("third")), `mutation($token:String!) {
	  rejectAutomationReview(token:$token,note:"needs revision") { state note decidedBy decidedAt }
	}`, "", map[string]any{"token": pending.Token})
	require.Empty(t, res.Errors)
	rejected := automationWireData(t, res.Data, "rejectAutomationReview")
	require.Equal(t, "REJECTED", rejected["state"])
	require.Equal(t, "needs revision", rejected["note"])
	require.Equal(t, "third", rejected["decidedBy"])
	_, err = time.Parse(time.RFC3339Nano, rejected["decidedAt"].(string))
	require.NoError(t, err)
	for _, corrupt := range []string{"{broken", "null", `{}`, `{"windows":"broken"}`, `{"windows":null}`} {
		require.NoError(t, api.RDB.DB(ctx).Model(row).Update("result", corrupt).Error)
		res = schema.Exec(ctx, `query($token:String!) { automationReview(token:$token) { token windowResults { role } } }`, "", map[string]any{"token": row.Token})
		require.Len(t, res.Errors, 1, "stored result %s", corrupt)
		require.Equal(t, []any{"automationReview", "windowResults"}, res.Errors[0].Path)
		broken := automationWireData(t, res.Data, "automationReview")
		require.Equal(t, row.Token, broken["token"])
		require.Contains(t, broken, "windowResults")
		require.Nil(t, broken["windowResults"])
	}
}

func TestAutomationReviewWireAuthorizationAndTimestamps(t *testing.T) {
	ctx, _, schema := automationWireRig(t)
	scoped := automationWireClaims("scoped")
	scoped.ScopeAnchors = []string{"customer:c1"}
	for _, doc := range []string{
		`{ automationReview(token:"missing") { token } }`,
		`{ automationReviews(criteria:{profileToken:"profile",pageNumber:1,pageSize:10}) { results { token } } }`,
	} {
		res := schema.Exec(auth.WithClaims(ctx, scoped), doc, "", nil)
		require.Len(t, res.Errors, 1)
		require.Equal(t, "SCOPED_GRANT_NOT_SUPPORTED", res.Errors[0].Extensions["code"])
		noRead := automationWireClaims("no-read")
		noRead.Authorities = nil
		res = schema.Exec(auth.WithClaims(ctx, noRead), doc, "", nil)
		require.Len(t, res.Errors, 1)
		require.ErrorIs(t, res.Errors[0].ResolverError, auth.ErrForbidden)
	}
	reader := automationWireClaims("reader")
	reader.Authorities = []string{string(auth.DeviceRead)}
	for _, doc := range []string{
		`mutation { approveAutomationReview(token:"missing",digest:"digest") { token } }`,
		`mutation { rejectAutomationReview(token:"missing",note:"stop") { token } }`,
		automationRunDoc,
	} {
		res := schema.Exec(auth.WithClaims(ctx, reader), doc, "", map[string]any{"input": automationWireInput()})
		require.Len(t, res.Errors, 1)
		require.ErrorIs(t, res.Errors[0].ResolverError, auth.ErrForbidden)
	}
	input := automationWireInput()
	input["windows"].([]any)[0].(map[string]any)["start"] = "yesterday"
	res := schema.Exec(ctx, automationRunDoc, "", map[string]any{"input": input})
	require.Len(t, res.Errors, 1)
	var invalid *model.InvalidArgumentError
	require.ErrorAs(t, res.Errors[0].ResolverError, &invalid)
	require.Equal(t, "window start must be an RFC3339 timestamp", invalid.Msg)
}
