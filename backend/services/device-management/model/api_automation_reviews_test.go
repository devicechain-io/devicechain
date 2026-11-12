// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type fakeRuleComparer struct {
	requests []CompareRequest
	response *CompareResponse
	err      error
}

func (f *fakeRuleComparer) CompareRuleVersions(_ context.Context, req CompareRequest) (*CompareResponse, error) {
	f.requests = append(f.requests, req)
	return f.response, f.err
}

type automationReviewRig struct {
	api      *Api
	ctx      context.Context
	profile  *DeviceProfile
	rule     *DetectionRule
	comparer *fakeRuleComparer
}

func reviewIdentity(name string) (context.Context, actiongate.Approver) {
	now := time.Now().UTC().Truncate(time.Second)
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: name,
		Authorities: []string{string(auth.DeviceWrite), string(auth.DeviceRead), string(auth.EventRead)}, Amr: auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now)}
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "acme"), claims)
	return ctx, actiongate.Approver{Principal: name, Amr: claims.Amr, MfaTime: &now, Claims: claims}
}

func newAutomationReviewRig(t *testing.T, baseline bool) *automationReviewRig {
	t.Helper()
	api, _ := configTestApi(t)
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&AutomationReview{}, &rdb.AuditEvent{}))
	require.NoError(t, api.RDB.Database.Exec("CREATE UNIQUE INDEX review_test_request ON automation_reviews(tenant_id, request_id)").Error)
	require.NoError(t, api.RDB.Database.Exec("CREATE UNIQUE INDEX review_test_token ON automation_reviews(tenant_id, token)").Error)
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	ctx, _ := reviewIdentity("before")
	profile := seedProfileWithRule(t, api, ctx, "profile", "r1", true)
	var rule DetectionRule
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "r1").First(&rule).Error)
	rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":26}`)
	require.NoError(t, api.RDB.DB(ctx).Save(&rule).Error)
	if baseline {
		_, err := api.PublishDeviceProfile(ctx, "profile", nil, nil, "before")
		require.NoError(t, err)
	}
	comparer := &fakeRuleComparer{response: &CompareResponse{Ok: true, Outcome: ReviewOutcomePassed,
		Result: `{"outcome":"PASSED"}`, ResultDigest: strings.TrimPrefix(reviewHash([]byte(`{"outcome":"PASSED"}`)), "sha256:"),
		InputDigest: "inputs", EngineBuild: "test-engine", Fidelity: ReviewFidelity{Status: "SUPPORTED"},
		Windows: []ReviewWindowResult{{Role: "INCIDENT", Stream: "resolved", FirstSeq: "12", LastSeq: "19"}, {Role: "NORMAL", Stream: "resolved", FirstSeq: "20", LastSeq: "25"}}}}
	api.RuleComparer = comparer
	ctx, _ = reviewIdentity("creator")
	return &automationReviewRig{api: api, ctx: ctx, profile: profile, rule: &rule, comparer: comparer}
}

func (r *automationReviewRig) changeRule(t *testing.T, actor string) {
	t.Helper()
	ctx, _ := reviewIdentity(actor)
	r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":25.5}`)
	require.NoError(t, r.api.RDB.DB(ctx).Save(r.rule).Error)
}

func reviewRunInput() RunAutomationReviewInput {
	at := time.Date(2026, 11, 10, 8, 0, 0, 123, time.FixedZone("east", -5*3600))
	return RunAutomationReviewInput{ProfileToken: "profile", RequestId: "request-1", DeviceTokens: []string{"z", "a", "z"},
		Windows:      []ReviewWindow{{Role: "NORMAL", Start: at, End: at.Add(time.Hour)}, {Role: "INCIDENT", Start: at.Add(-24 * time.Hour), End: at.Add(-23 * time.Hour)}},
		Expectations: []ReviewExpectation{{Window: "INCIDENT", Name: "catch", Expect: `{"raised":{"rule":"r1"}}`}}}
}

func (r *automationReviewRig) run(t *testing.T) *AutomationReview {
	t.Helper()
	_, person := reviewIdentity("creator")
	row, err := r.api.RunAutomationReview(r.ctx, person, reviewRunInput())
	require.NoError(t, err)
	return row
}

func reviewErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var gate *actiongate.Error
	require.ErrorAs(t, err, &gate)
	require.Equal(t, code, gate.Code)
}

func (r *automationReviewRig) count(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AutomationReview{}).Count(&n).Error)
	return n
}

func (r *automationReviewRig) journal(t *testing.T, row *AutomationReview, operation, actor string) {
	t.Helper()
	var events []rdb.AuditEvent
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("table_name = ? AND entity_pk = ? AND operation = ?", "automation_reviews", strconv.FormatUint(uint64(row.ID), 10), operation).Order("id DESC").Find(&events).Error)
	require.NotEmpty(t, events)
	require.Equal(t, actor, events[0].Actor)
	require.Equal(t, "acme", events[0].TenantId)
	require.Equal(t, rdb.AuditCategoryMutation, events[0].Category)
	require.EqualValues(t, 1, events[0].RowsAffected)
}

func TestRunAutomationReviewCapturesEvidenceAndAuthors(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	// A legacy baseline can contain a broken formula; only the rule changes in replay.
	m := &MetricDefinition{DeviceProfileId: r.profile.ID, MetricKey: "calc", DataType: "DOUBLE", Expression: sql.NullString{String: "!!!", Valid: true}, TokenReference: rdb.TokenReference{Token: "calc"}}
	require.NoError(t, r.api.RDB.DB(r.ctx).Create(m).Error)
	snapshot, err := r.api.buildProfileSnapshot(r.ctx, r.profile.ID)
	require.NoError(t, err)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).
		Where("device_profile_id = ? AND version = ?", r.profile.ID, 1).Update("snapshot", snapshot).Error)
	r.changeRule(t, "author")
	row := r.run(t)
	require.Equal(t, ReviewKindRuleChange, row.Kind)
	require.Equal(t, ReviewOutcomePassed, row.Outcome)
	require.Equal(t, ReviewStateRan, row.State)
	require.Equal(t, "r1", *row.RuleToken)
	require.Equal(t, 1, row.RequiredApprovals)
	require.Equal(t, 0, row.Revision)
	require.Equal(t, "[]", string(row.Approvals))
	require.Equal(t, "creator", row.CreatedBy)
	require.Equal(t, "test-engine", row.EngineBuild)
	require.Equal(t, "inputs", row.InputDigest)
	require.Equal(t, `{"outcome":"PASSED"}`, row.Result)
	require.True(t, strings.HasPrefix(row.Token, "acr_"))
	require.Len(t, row.Token, 36)
	require.Len(t, r.comparer.requests, 1)
	req := r.comparer.requests[0]
	require.Equal(t, "profile", req.ProfileToken)
	require.Equal(t, []string{"a", "z"}, req.DeviceTokens)
	require.Equal(t, []string{"calc"}, req.ConstantMetrics)
	require.Equal(t, &RuleEnvelope{Token: "r1", Enabled: true, Definition: `{"metric":"temp_c","threshold":26}`}, req.Base)
	require.Equal(t, &RuleEnvelope{Token: "r1", Enabled: true, Definition: `{"metric":"temp_c","threshold":25.5}`}, req.Draft)
	require.Equal(t, "INCIDENT", req.Windows[0].Role)
	require.Equal(t, time.UTC, req.Windows[0].Start.Location())
	require.Equal(t, reviewRunInput().Expectations, req.Expectations)
	require.JSONEq(t, `["author","creator"]`, string(row.DraftAuthors))
	require.JSONEq(t, `[{"role":"INCIDENT","stream":"resolved","firstSeq":"12","lastSeq":"19"},{"role":"NORMAL","stream":"resolved","firstSeq":"20","lastSeq":"25"}]`, string(row.StreamBounds))
	r.journal(t, row, "create", "creator")
	computed, err := reviewDigestOf(row, "profile")
	require.NoError(t, err)
	require.Equal(t, computed, row.ReviewDigest)
	require.Equal(t, reviewHash(row.Scope), row.ScopeDigest)
	require.Equal(t, reviewHash(row.Expectations), row.ExpectationsDigest)
	_, person := reviewIdentity("creator")
	again, err := r.api.RunAutomationReview(r.ctx, person, reviewRunInput())
	require.NoError(t, err)
	require.Equal(t, row.Token, again.Token)
	require.EqualValues(t, 1, r.count(t))
	require.Len(t, r.comparer.requests, 1)
	in := reviewRunInput()
	in.DeviceTokens = []string{"other"}
	_, err = r.api.RunAutomationReview(r.ctx, person, in)
	reviewErrorCode(t, err, "CONFLICT")
	ctx, other := reviewIdentity("other")
	_, err = r.api.RunAutomationReview(ctx, other, reviewRunInput())
	reviewErrorCode(t, err, "CONFLICT")
}

func TestRunAutomationReviewUnavailableWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		configure  func(*automationReviewRig)
	}{
		{"transport", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) { r.comparer.err = errors.New("down") }},
		{"refusal", "REVIEW_REFUSED", func(r *automationReviewRig) {
			r.comparer.response = &CompareResponse{RefusalReason: "BUSY", RefusalMessage: "try later"}
		}},
		{"digest", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) { r.comparer.response.ResultDigest = "wrong" }},
		{"missing outcome", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) {
			r.comparer.response.Result = `{}`
			r.comparer.response.ResultDigest = reviewHash([]byte(r.comparer.response.Result))
		}},
		{"mismatched outcome", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) {
			r.comparer.response.Result = `{"outcome":"FAILED"}`
			r.comparer.response.ResultDigest = reviewHash([]byte(r.comparer.response.Result))
		}},
		{"nil", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) { r.api.RuleComparer = nil }},
		{"empty", "REVIEW_UNAVAILABLE", func(r *automationReviewRig) { r.comparer.response = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			r.changeRule(t, "author")
			tc.configure(r)
			_, person := reviewIdentity("creator")
			_, err := r.api.RunAutomationReview(r.ctx, person, reviewRunInput())
			reviewErrorCode(t, err, tc.code)
			if tc.name == "refusal" {
				require.Contains(t, err.Error(), "BUSY: try later")
			}
			require.EqualValues(t, 0, r.count(t))
			var n int64
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(&rdb.AuditEvent{}).Where("table_name = ?", "automation_reviews").Count(&n).Error)
			require.EqualValues(t, 0, n)
		})
	}
	r := newAutomationReviewRig(t, false)
	_, person := reviewIdentity("creator")
	_, err := r.api.RunAutomationReview(r.ctx, person, reviewRunInput())
	reviewErrorCode(t, err, "NO_BASELINE")
	require.EqualValues(t, 0, r.count(t))
}

func TestRunAutomationReviewLocalOutcomesAndStoredPublishDigest(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	r.api.RuleComparer = nil
	row := r.run(t)
	require.Equal(t, ReviewKindNoAutomationChange, row.Kind)
	require.Equal(t, ReviewOutcomeNoAutomationChange, row.Outcome)
	require.Equal(t, "{}", row.Result)
	require.Equal(t, reviewHash([]byte("{}")), row.ResultDigest)
	require.Equal(t, "", row.InputDigest)
	require.Equal(t, "", row.EngineBuild)
	require.Equal(t, "[]", string(row.StreamBounds))
	// A no-op ORM save must not stale the evidence, and publishing must freeze the same digest.
	r.rule.UpdatedAt = time.Now().Add(time.Minute)
	require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
	digest, err := r.api.CurrentDraftDigest(r.ctx, r.profile.ID)
	require.NoError(t, err)
	require.Equal(t, row.DraftDigest, digest)
	v, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "creator")
	require.NoError(t, err)
	digest, err = SnapshotDigest(v.Snapshot)
	require.NoError(t, err)
	require.Equal(t, row.DraftDigest, digest)
	var stored DeviceProfileVersion
	require.NoError(t, r.api.RDB.DB(r.ctx).First(&stored, v.ID).Error)
	digest, err = SnapshotDigest(stored.Snapshot)
	require.NoError(t, err)
	require.Equal(t, row.DraftDigest, digest)

	r = newAutomationReviewRig(t, true)
	r.changeRule(t, "author")
	second := &DetectionRule{DeviceProfileId: r.profile.ID, TokenReference: rdb.TokenReference{Token: "r2"}, Definition: datatypes.JSON(`{}`)}
	require.NoError(t, r.api.RDB.DB(r.ctx).Create(second).Error)
	row = r.run(t)
	require.Equal(t, ReviewKindUnreviewable, row.Kind)
	require.Equal(t, ReviewOutcomeInconclusive, row.Outcome)
	require.JSONEq(t, `{"reasons":["rules r1 and r2 both change"]}`, row.Result)
	require.Equal(t, reviewHash([]byte(row.Result)), row.ResultDigest)
	require.Len(t, r.comparer.requests, 0)
}

func TestApproveAutomationReviewRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*testing.T, *automationReviewRig, *AutomationReview, *actiongate.Approver)
		digest     string
	}{
		{name: "digest", code: "DIGEST_MISMATCH", digest: "wrong"},
		{name: "failed", code: "NOT_APPROVABLE", mutate: func(t *testing.T, r *automationReviewRig, row *AutomationReview, _ *actiongate.Approver) {
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("outcome", ReviewOutcomeFailed).Error)
		}},
		{name: "inconclusive", code: "NOT_APPROVABLE", mutate: func(t *testing.T, r *automationReviewRig, row *AutomationReview, _ *actiongate.Approver) {
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("outcome", ReviewOutcomeInconclusive).Error)
		}},
		{name: "no factor", code: "SECOND_FACTOR_REQUIRED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.Amr = []string{"pwd"}
			a.Claims.MfaTime = nil
		}},
		{name: "old factor", code: "STEP_UP_REQUIRED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.MfaTime = jwt.NewNumericDate(time.Now().Add(-time.Hour))
		}},
		{name: "creator", code: "SELF_APPROVAL_REFUSED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Principal, a.Claims.Username = "creator", "creator"
		}},
		{name: "author", code: "SELF_APPROVAL_REFUSED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Principal, a.Claims.Username = "author", "author"
		}},
		{name: "agent", code: "APPROVER_IS_AN_AGENT", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.ClientId = "agent"
		}},
		{name: "service", code: "SERVICE_TOKEN_REFUSED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.TokenType = auth.TokenTypeService
		}},
		{name: "service account", code: "NOT_A_PERSON", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Principal = auth.ServiceAccountSubjectPrefix + "robot"
			a.Claims.Username = a.Principal
		}},
		{name: "service account subject", code: "NOT_A_PERSON", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.Subject = auth.ServiceAccountSubjectPrefix + "robot"
		}},
		{name: "scoped", code: "SCOPED_GRANT_NOT_SUPPORTED", mutate: func(_ *testing.T, _ *automationReviewRig, _ *AutomationReview, a *actiongate.Approver) {
			a.Claims.ScopeAnchors = []string{"customer:x"}
		}},
		{name: "draft", code: "REVIEW_STALE_DRAFT_CHANGED", mutate: func(t *testing.T, r *automationReviewRig, _ *AutomationReview, _ *actiongate.Approver) {
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(r.rule).Update("enabled", false).Error)
		}},
		{name: "baseline", code: "REVIEW_STALE_BASELINE_CHANGED", mutate: func(t *testing.T, r *automationReviewRig, _ *AutomationReview, _ *actiongate.Approver) {
			require.NoError(t, recordActivation(r.api.RDB.DB(r.ctx), r.profile.ID, 1, ActivationKindRollback, time.Now(), "other"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			r.changeRule(t, "author")
			row := r.run(t)
			ctx, a := reviewIdentity("third")
			if tc.mutate != nil {
				tc.mutate(t, r, row, &a)
			}
			ctx = auth.WithClaims(ctx, a.Claims)
			digest := tc.digest
			if digest == "" {
				digest = row.ReviewDigest
			}
			_, err := r.api.ApproveAutomationReview(ctx, a, row.Token, digest)
			reviewErrorCode(t, err, tc.code)
			stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
			require.NoError(t, err)
			require.Equal(t, ReviewStateRan, stored.State)
			require.Equal(t, "[]", string(stored.Approvals))
			require.Nil(t, stored.ApprovedAt)
		})
	}
}

func TestApproveAndRejectAutomationReviewJournal(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(strconv.FormatBool(change), func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			if change {
				r.changeRule(t, "author")
			}
			row := r.run(t)
			ctx, third := reviewIdentity("third")
			approved, err := r.api.ApproveAutomationReview(ctx, third, row.Token, row.ReviewDigest)
			require.NoError(t, err)
			require.Equal(t, ReviewStateApproved, approved.State)
			require.Equal(t, 1, approved.Revision)
			require.NotNil(t, approved.ApprovedAt)
			require.NotNil(t, approved.ExpiresAt)
			require.Equal(t, DefaultReviewApprovalTTL, approved.ExpiresAt.Sub(*approved.ApprovedAt))
			require.EqualValues(t, 0, *approved.ApprovedPolicyRevision)
			var approvals []ReviewApproval
			require.NoError(t, json.Unmarshal(approved.Approvals, &approvals))
			require.Len(t, approvals, 1)
			require.Equal(t, "third", approvals[0].Sub)
			require.Equal(t, auth.AmrPasswordOTP(), approvals[0].Amr)
			require.NotNil(t, approvals[0].MfaTime)
			require.True(t, third.MfaTime.Equal(*approvals[0].MfaTime))
			require.True(t, approved.ApprovedAt.Equal(approvals[0].At))
			require.Equal(t, 0, approvals[0].PolicyRevision)
			r.journal(t, approved, "update", "third")
			note := "stop"
			ctx, author := reviewIdentity("author")
			author.Claims.Amr, author.Claims.MfaTime = nil, nil
			rejected, err := r.api.RejectAutomationReview(ctx, author, row.Token, &note)
			require.NoError(t, err)
			require.Equal(t, ReviewStateRejected, rejected.State)
			require.Equal(t, "author", *rejected.DecidedBy)
			require.Equal(t, note, *rejected.DecidedNote)
			require.NotNil(t, rejected.DecidedAt)
			require.Equal(t, 2, rejected.Revision)
			r.journal(t, rejected, "update", "author")
			_, err = r.api.RejectAutomationReview(ctx, author, row.Token, nil)
			reviewErrorCode(t, err, "REVIEW_NOT_PENDING")
		})
	}
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	ctx, creator := reviewIdentity("creator")
	long := strings.Repeat("a", 513)
	_, err := r.api.RejectAutomationReview(ctx, creator, row.Token, &long)
	var invalid *InvalidArgumentError
	require.ErrorAs(t, err, &invalid)
	rejected, err := r.api.RejectAutomationReview(ctx, creator, row.Token, nil)
	require.NoError(t, err)
	require.Equal(t, ReviewStateRejected, rejected.State)
	require.Equal(t, 1, rejected.Revision)
}

func TestAutomationReviewApprovalCAS(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	// Both readers start at revision zero: only one conditional write may win.
	stale := *row
	ctx, _ := reviewIdentity("third")
	now := time.Now().UTC()
	a := []ReviewApproval{{Approval: actiongate.Approval{Sub: "third", At: now}}}
	require.NoError(t, r.api.recordReviewApproval(ctx, row, a, now))
	err := r.api.recordReviewApproval(ctx, &stale, []ReviewApproval{{Approval: actiongate.Approval{Sub: "fourth", At: now}}}, now)
	reviewErrorCode(t, err, "CONCURRENT_APPROVAL")
	stored, err := r.api.AutomationReviewByToken(ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateApproved, stored.State)
	require.Equal(t, 1, stored.Revision)
	var approvals []ReviewApproval
	require.NoError(t, json.Unmarshal(stored.Approvals, &approvals))
	require.Equal(t, a, approvals)

	// Force both CAS attempts to lose, exercising the public API's one-retry bound.
	r = newAutomationReviewRig(t, true)
	row = r.run(t)
	updates := 0
	db := r.api.RDB.Database
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:review_cas_race", func(tx *gorm.DB) {
		if tx.Statement.Table != "automation_reviews" {
			return
		}
		updates++
		// Test-only competing writer in the same connection; raw SQL avoids recursively firing this hook.
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE automation_reviews SET revision = revision + 1 WHERE id = ?", row.ID).Error)
	}))
	defer func() { require.NoError(t, db.Callback().Update().Remove("test:review_cas_race")) }()
	ctx, third := reviewIdentity("third")
	_, err = r.api.ApproveAutomationReview(ctx, third, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, "CONCURRENT_APPROVAL")
	require.Equal(t, 2, updates)
	stored, err = r.api.AutomationReviewByToken(ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, 2, stored.Revision)
	require.Equal(t, ReviewStateRan, stored.State)
	require.Equal(t, "[]", string(stored.Approvals))
}

func TestDeleteProfileRemovesAutomationReviews(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	deleted, err := r.api.DeleteDeviceProfile(r.ctx, "profile")
	require.NoError(t, err)
	require.True(t, deleted)
	require.EqualValues(t, 0, r.count(t))
	_, err = r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestReviewInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RunAutomationReviewInput)
	}{
		{"missing request", func(in *RunAutomationReviewInput) { in.RequestId = "" }},
		{"long request", func(in *RunAutomationReviewInput) { in.RequestId = strings.Repeat("a", 129) }},
		{"control request", func(in *RunAutomationReviewInput) { in.RequestId = "bad\n" }},
		{"no devices", func(in *RunAutomationReviewInput) { in.DeviceTokens = nil }},
		{"too many devices", func(in *RunAutomationReviewInput) {
			for i := 0; i < 51; i++ {
				in.DeviceTokens = append(in.DeviceTokens, strconv.Itoa(i))
			}
		}},
		{"missing window", func(in *RunAutomationReviewInput) { in.Windows = in.Windows[:1] }},
		{"duplicate role", func(in *RunAutomationReviewInput) { in.Windows[0].Role = "INCIDENT" }},
		{"reversed window", func(in *RunAutomationReviewInput) { in.Windows[0].Start = in.Windows[0].End }},
		{"empty expectation", func(in *RunAutomationReviewInput) { in.Expectations[0].Expect = "" }},
		{"large expectation", func(in *RunAutomationReviewInput) { in.Expectations[0].Expect = strings.Repeat("x", 8193) }},
		{"expectation role", func(in *RunAutomationReviewInput) { in.Expectations[0].Window = "OTHER" }},
		{"too many expectations", func(in *RunAutomationReviewInput) {
			for len(in.Expectations) < 11 {
				in.Expectations = append(in.Expectations, in.Expectations[0])
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewRunInput()
			tc.change(&in)
			_, _, _, err := normalizeReviewInput(in)
			var invalid *InvalidArgumentError
			require.ErrorAs(t, err, &invalid)
			require.NotEqual(t, "", invalid.Msg)
		})
	}
}

func TestReviewRuleTokenValidationAndEnvelopes(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(strconv.FormatBool(change), func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			if change {
				r.changeRule(t, "author")
			}
			token := "other"
			in := reviewRunInput()
			in.RuleToken = &token
			_, person := reviewIdentity("creator")
			_, err := r.api.RunAutomationReview(r.ctx, person, in)
			var invalid *InvalidArgumentError
			require.ErrorAs(t, err, &invalid)
			require.EqualValues(t, 0, r.count(t))
		})
	}
	for _, removed := range []bool{false, true} {
		t.Run("absent side "+strconv.FormatBool(removed), func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			if removed {
				require.NoError(t, r.api.RDB.DB(r.ctx).Delete(r.rule).Error)
			} else {
				added := &DetectionRule{DeviceProfileId: r.profile.ID, TokenReference: rdb.TokenReference{Token: "r2"}, Enabled: true, Definition: datatypes.JSON(`{}`)}
				require.NoError(t, r.api.RDB.DB(r.ctx).Create(added).Error)
			}
			row := r.run(t)
			require.Equal(t, ReviewKindRuleChange, row.Kind)
			require.Len(t, r.comparer.requests, 1)
			req := r.comparer.requests[0]
			if removed {
				require.Nil(t, req.Draft)
				require.Nil(t, row.DraftEnvelope)
				require.Equal(t, "r1", req.Base.Token)
			} else {
				require.Nil(t, req.Base)
				require.Nil(t, row.BaseEnvelope)
				require.Equal(t, "r2", req.Draft.Token)
			}
		})
	}
}

func TestDraftAuthorsDeletedRowsAndMissingBoundary(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	ctx, _ := reviewIdentity("deleted-author")
	require.NoError(t, r.api.RDB.DB(ctx).Delete(r.rule).Error)
	activation, err := r.api.latestReviewActivation(r.ctx, r.profile.ID)
	require.NoError(t, err)
	var version DeviceProfileVersion
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("device_profile_id = ?", r.profile.ID).First(&version).Error)
	base, err := parseProfileSnapshot(version.Snapshot)
	require.NoError(t, err)
	draftRaw, err := r.api.buildProfileSnapshot(r.ctx, r.profile.ID)
	require.NoError(t, err)
	draft, err := parseProfileSnapshot(draftRaw)
	require.NoError(t, err)
	authors, err := r.api.draftAuthors(r.ctx, r.profile, activation.Version, base, draft)
	require.NoError(t, err)
	require.Equal(t, []string{"deleted-author"}, authors)
	// A missing baseline publish has no safe lower bound, so include the earlier writer.
	authors, err = r.api.draftAuthors(r.ctx, r.profile, activation.Version+100, base, draft)
	require.NoError(t, err)
	require.Equal(t, []string{"before", "deleted-author"}, authors)
}

func TestAutomationReviewReadsAreTenantScopedAndFilterDraft(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	first := r.run(t)
	r.changeRule(t, "author")
	in := reviewRunInput()
	in.RequestId = "second"
	_, person := reviewIdentity("creator")
	second, err := r.api.RunAutomationReview(r.ctx, person, in)
	require.NoError(t, err)
	matching, state := true, ReviewStateRan
	page, err := r.api.AutomationReviews(r.ctx, AutomationReviewSearchCriteria{ProfileToken: "profile", MatchingCurrentDraft: &matching, State: &state, Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, second.Token, page.Results[0].Token)
	matching = false
	page, err = r.api.AutomationReviews(r.ctx, AutomationReviewSearchCriteria{ProfileToken: "profile", MatchingCurrentDraft: &matching, Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, first.Token, page.Results[0].Token)
	_, err = r.api.AutomationReviewByToken(core.WithTenant(context.Background(), "other"), first.Token)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = r.api.AutomationReviewByToken(context.Background(), first.Token)
	require.Error(t, err)
}

func TestAutomationReviewRateLimit(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	for i := 1; i < MaxReviewsPerHour; i++ {
		copy := *row
		copy.ID, copy.Token, copy.RequestId = 0, "acr_test_"+strconv.Itoa(i), "rate-"+strconv.Itoa(i)
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(&copy).Error)
	}
	in := reviewRunInput()
	in.RequestId = "over-limit"
	_, person := reviewIdentity("creator")
	_, err := r.api.RunAutomationReview(r.ctx, person, in)
	reviewErrorCode(t, err, "RATE_LIMITED")
	again, err := r.api.RunAutomationReview(r.ctx, person, reviewRunInput())
	require.NoError(t, err)
	require.Equal(t, row.Token, again.Token)
	require.Equal(t, row.ReviewDigest, again.ReviewDigest)
	require.EqualValues(t, MaxReviewsPerHour, r.count(t))
}

func TestReviewApprovalIsDistinctAndIdempotentWhileRan(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("required_approvals", 2).Error)
	ctx, third := reviewIdentity("third")
	first, err := r.api.ApproveAutomationReview(ctx, third, row.Token, row.ReviewDigest)
	require.NoError(t, err)
	require.Equal(t, ReviewStateRan, first.State)
	require.Equal(t, 1, first.Revision)
	again, err := r.api.ApproveAutomationReview(ctx, third, row.Token, row.ReviewDigest)
	require.NoError(t, err)
	require.Equal(t, first.Revision, again.Revision)
	require.JSONEq(t, string(first.Approvals), string(again.Approvals))
	ctx, fourth := reviewIdentity("fourth")
	approved, err := r.api.ApproveAutomationReview(ctx, fourth, row.Token, row.ReviewDigest)
	require.NoError(t, err)
	require.Equal(t, ReviewStateApproved, approved.State)
	require.Equal(t, 2, approved.Revision)
	var approvals []ReviewApproval
	require.NoError(t, json.Unmarshal(approved.Approvals, &approvals))
	require.Len(t, approvals, 2)
	require.Equal(t, "third", approvals[0].Sub)
	require.Equal(t, "fourth", approvals[1].Sub)
}

// A decided review is not approvable, and the refusal says so before any write is tried: the
// compare-and-set would also refuse it, but as a concurrent approval, which tells the person to
// retry something that can never succeed.
func TestApproveDecidedReviewIsNotPending(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	r.changeRule(t, "author")
	row := r.run(t)
	_, err := r.api.RejectAutomationReview(r.ctx, mustReviewPerson("creator"), row.Token, nil)
	require.NoError(t, err)
	ctx, third := reviewIdentity("third")
	_, err = r.api.ApproveAutomationReview(ctx, third, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, "REVIEW_NOT_PENDING")
}

func mustReviewPerson(name string) actiongate.Approver {
	_, a := reviewIdentity(name)
	return a
}

// A person who edited a baseline rule after the activation is an author even when the rule has
// since been hard-deleted from the draft: only the BASELINE snapshot still carries its row id.
// A system-context write is never an author.
func TestDraftAuthorsFollowBaselineRowsAndSkipSystem(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	r.changeRule(t, "editor")
	sys := core.WithSystemContext(core.WithTenant(context.Background(), "acme"))
	require.NoError(t, r.api.RDB.DB(sys).Model(&MetricDefinition{}).Where("device_profile_id = ?", r.profile.ID).
		Update("description", "machine note").Error)
	require.NoError(t, r.api.RDB.DB(sys).Model(&DeviceProfile{}).Where("id = ?", r.profile.ID).
		Update("description", "machine note").Error)
	require.NoError(t, r.api.RDB.DB(sys).Exec("DELETE FROM detection_rules WHERE id = ?", r.rule.ID).Error)
	row := r.run(t)
	var authors []string
	require.NoError(t, json.Unmarshal(row.DraftAuthors, &authors))
	require.Equal(t, []string{"editor"}, authors)
}

func TestDraftAuthorsAfterRollbackUseBaselinePublish(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	r.changeRule(t, "alice")
	ctx, _ := reviewIdentity("before")
	_, err := r.api.PublishDeviceProfile(ctx, "profile", nil, nil, "before")
	require.NoError(t, err)
	ctx, _ = reviewIdentity("carol")
	_, err = r.api.RollbackDeviceProfile(ctx, "profile", 1)
	require.NoError(t, err)
	activation, err := r.api.latestReviewActivation(r.ctx, r.profile.ID)
	require.NoError(t, err)
	row := r.run(t)
	require.Equal(t, activation.ID, row.BaselineActivationId)
	require.Equal(t, int32(1), row.BaselineVersion)
	var authors []string
	require.NoError(t, json.Unmarshal(row.DraftAuthors, &authors))
	require.Contains(t, authors, "alice")
	ctx, alice := reviewIdentity("alice")
	_, err = r.api.ApproveAutomationReview(ctx, alice, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, "SELF_APPROVAL_REFUSED")
}

func TestApproveAutomationReviewVerifiesStoredEvidence(t *testing.T) {
	for _, tc := range []struct {
		column string
		value  any
	}{
		{"result", `{"outcome":"PASSED","tampered":true}`},
		{"scope", datatypes.JSON(`{"devices":["other"]}`)},
		{"expectations", datatypes.JSON(`[]`)},
		{"draft_authors", datatypes.JSON(`[]`)},
	} {
		t.Run(tc.column, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			r.changeRule(t, "author")
			row := r.run(t)
			digest := row.ReviewDigest
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update(tc.column, tc.value).Error)
			ctx, third := reviewIdentity("third")
			_, err := r.api.ApproveAutomationReview(ctx, third, row.Token, digest)
			reviewErrorCode(t, err, "DIGEST_MISMATCH")
			stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
			require.NoError(t, err)
			require.Equal(t, ReviewStateRan, stored.State)
			require.Equal(t, "[]", string(stored.Approvals))
			require.Equal(t, 0, stored.Revision)
		})
	}
}

func TestRunAutomationReviewRequestBindsProfileAndRule(t *testing.T) {
	for _, field := range []string{"profile", "rule", "absent rule"} {
		t.Run(field, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			in := reviewRunInput()
			rule := "r1"
			in.RuleToken = &rule
			_, creator := reviewIdentity("creator")
			row, err := r.api.RunAutomationReview(r.ctx, creator, in)
			require.NoError(t, err)
			switch field {
			case "profile":
				seedProfileWithRule(t, r.api, r.ctx, "other", "r2", true)
				in.ProfileToken = "other"
			case "rule":
				other := "r2"
				in.RuleToken = &other
			case "absent rule":
				in.RuleToken = nil
			}
			_, err = r.api.RunAutomationReview(r.ctx, creator, in)
			reviewErrorCode(t, err, "CONFLICT")
			require.EqualValues(t, 1, r.count(t))
			stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
			require.NoError(t, err)
			require.Equal(t, string(row.Scope), string(stored.Scope))
		})
	}
}

func TestRunAutomationReviewHiddenDependenciesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, definition, key, kind string
		changeRule                  bool
	}{
		{"CEL escape", `{"condition":{"cel":"m[\"te\\u006dp_c\"] > 1"}}`, "temp_c", ReviewKindUnreviewable, false},
		{"CEL variable", `{"conditions":[{"cel":"m[key] > 1"}]}`, "unread", ReviewKindUnreviewable, false},
		{"CEL concatenation", `{"cel":"m['te' + 'mp_c'] > 1"}`, "temp_c", ReviewKindUnreviewable, false},
		{"JSON escape", `{"metric":"te\u006dp_c"}`, "temp_c", ReviewKindUnreviewable, false},
		{"template", `{"metric":"{{key}}"}`, "unread", ReviewKindUnreviewable, false},
		{"backslash", `{"expression":"m[\"temp\"]"}`, "unread", ReviewKindUnreviewable, false},
		{"structured validation with rule change", `{"metric":"temp_c","threshold":26}`, "unread", ReviewKindUnreviewable, true},
		{"structured unrelated validation", `{"metric":"temp_c","threshold":26}`, "unread", ReviewKindUnreviewable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, false)
			ctx, _ := reviewIdentity("before")
			r.rule.Definition = datatypes.JSON(tc.definition)
			require.NoError(t, r.api.RDB.DB(ctx).Save(r.rule).Error)
			metric := &MetricDefinition{DeviceProfileId: r.profile.ID, MetricKey: tc.key, DataType: "DOUBLE",
				TokenReference: rdb.TokenReference{Token: "metric"}}
			require.NoError(t, r.api.RDB.DB(ctx).Create(metric).Error)
			_, err := r.api.PublishDeviceProfile(ctx, "profile", nil, nil, "before")
			require.NoError(t, err)
			ctx, _ = reviewIdentity("alice")
			metric.Expression = sql.NullString{String: "2.0", Valid: true}
			require.NoError(t, r.api.RDB.DB(ctx).Save(metric).Error)
			if tc.changeRule {
				r.changeRule(t, "alice")
			}
			row := r.run(t)
			require.Equal(t, tc.kind, row.Kind)
			var changes []ProfileChange
			require.NoError(t, json.Unmarshal(row.ProfileChanges, &changes))
			want := []ProfileChange{{Area: "METRIC", Key: tc.key, Change: "MODIFIED", Automation: tc.kind == ReviewKindUnreviewable}}
			if tc.changeRule {
				want = append(want, ProfileChange{Area: "RULE", Key: "r1", Change: "MODIFIED", Automation: true})
			}
			require.Equal(t, want, changes)
		})
	}
}

func TestReviewPersonRequiresEventRead(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	row := r.run(t)
	ctx, person := reviewIdentity("third")
	person.Claims.Authorities = []string{string(auth.DeviceRead), string(auth.DeviceWrite)}
	got, err := r.api.RunAutomationReview(ctx, person, reviewRunInput())
	require.ErrorIs(t, err, auth.ErrForbidden)
	require.Nil(t, got)
	got, err = r.api.ApproveAutomationReview(ctx, person, row.Token, row.ReviewDigest)
	require.ErrorIs(t, err, auth.ErrForbidden)
	require.Nil(t, got)
	got, err = r.api.RejectAutomationReview(ctx, person, row.Token, nil)
	require.ErrorIs(t, err, auth.ErrForbidden)
	require.Nil(t, got)
	person.Claims.Authorities = append(person.Claims.Authorities, string(auth.EventRead))
	in := reviewRunInput()
	in.RequestId = "third-run"
	got, err = r.api.RunAutomationReview(ctx, person, in)
	require.NoError(t, err)
	require.Equal(t, "third", got.CreatedBy)
	got, err = r.api.ApproveAutomationReview(ctx, person, row.Token, row.ReviewDigest)
	require.NoError(t, err)
	require.Equal(t, ReviewStateApproved, got.State)
}
