// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type fakePrincipalStanding struct {
	denied  bool
	err     error
	calls   int
	missing bool
}

func (f *fakePrincipalStanding) PrincipalStanding(ctx context.Context, principals, authorities []string) (map[string]bool, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]bool{}
	for _, p := range principals {
		if !f.missing {
			out[p] = !f.denied
		}
	}
	return out, nil
}

func protectReviewRig(t *testing.T, r *automationReviewRig, want ReviewPolicySettings) *AutomationReviewPolicyView {
	t.Helper()
	want.Required = true
	view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.True(t, applied)
	r.api.PrincipalStanding = &fakePrincipalStanding{}
	return view
}
func approveRigReview(t *testing.T, r *automationReviewRig, row *AutomationReview, name string) *AutomationReview {
	t.Helper()
	ctx, a := reviewIdentity(name)
	out, err := r.api.ApproveAutomationReview(ctx, a, row.Token, row.ReviewDigest)
	require.NoError(t, err)
	return out
}
func publishRigReview(r *automationReviewRig, row *AutomationReview) (*DeviceProfileVersion, error) {
	return r.api.PublishDeviceProfileReviewed(r.ctx, "profile", nil, nil, "publisher", &row.Token)
}
func withoutReviewMFA(name string) context.Context {
	ctx, _ := reviewIdentity(name)
	claims, _ := auth.ClaimsFromContext(ctx)
	claims.Amr = []string{"pwd"}
	claims.MfaTime = nil
	return auth.WithClaims(ctx, claims)
}

func TestPublishUnprotectedIgnoresReviewToken(t *testing.T) {
	r := newAutomationReviewRig(t, false)
	_, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	bogus := "missing"
	version, err := r.api.PublishDeviceProfileReviewed(r.ctx, "profile", nil, nil, "publisher", &bogus)
	require.NoError(t, err)
	require.Nil(t, version.AutomationReviewToken)
}

func TestProtectedPublishConsumesReview(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	_, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	reviewErrorCode(t, err, "REVIEW_REQUIRED")
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	version, err := publishRigReview(r, row)
	require.NoError(t, err)
	require.NotNil(t, version.AutomationReviewToken)
	require.Equal(t, row.Token, *version.AutomationReviewToken)
	stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateConsumed, stored.State)
	require.Equal(t, version.Version, *stored.ConsumedVersion)
	require.Nil(t, stored.ConsumedTxid)
	_, err = publishRigReview(r, row)
	reviewErrorCode(t, err, "REVIEW_ALREADY_USED")
}

func TestProtectedPublishRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		prepare    func(*testing.T, *automationReviewRig, *AutomationReview)
	}{
		{"draft", "REVIEW_STALE_DRAFT_CHANGED", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":24}`)
			require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
		}},
		{"policy count", "REVIEW_STALE_POLICY_CHANGED", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			want := DefaultReviewPolicySettings()
			want.Required = true
			want.RequiredApprovals = 2
			_, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			require.NoError(t, err)
			require.True(t, applied)
		}},
		{"policy ttl", "REVIEW_EXPIRED", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			at := reviewNow().Add(-1000 * time.Second)
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("approved_at", at).Error)
			want := DefaultReviewPolicySettings()
			want.Required = true
			want.ApprovalTtlSeconds = 900
			_, _, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			require.NoError(t, err)
		}},
		{"standing false", "APPROVER_NO_LONGER_QUALIFIES", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			r.api.PrincipalStanding = &fakePrincipalStanding{denied: true}
		}},
		{"standing error", "REVIEW_UNAVAILABLE", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			r.api.PrincipalStanding = &fakePrincipalStanding{err: errors.New("down")}
		}},
		{"standing nil", "REVIEW_UNAVAILABLE", func(t *testing.T, r *automationReviewRig, row *AutomationReview) { r.api.PrincipalStanding = nil }},
		{"standing missing", "REVIEW_UNAVAILABLE", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			r.api.PrincipalStanding = &fakePrincipalStanding{missing: true}
		}},
		{"wrong profile", "REVIEW_WRONG_PROFILE", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("device_profile_id", r.profile.ID+999).Error)
		}},
		{"missing", "REVIEW_NOT_FOUND", func(t *testing.T, r *automationReviewRig, row *AutomationReview) { row.Token = "absent" }},
		{"digest", "DIGEST_MISMATCH", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("review_digest", "corrupt").Error)
		}},
		{"rollback baseline", "REVIEW_STALE_BASELINE_CHANGED", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			reason := "restore known settings"
			_, err := r.api.RollbackDeviceProfileWithReason(r.ctx, "profile", 1, &reason)
			require.NoError(t, err)
		}},
		{"publish baseline", "REVIEW_STALE_BASELINE_CHANGED", func(t *testing.T, r *automationReviewRig, row *AutomationReview) {
			_, creator := reviewIdentity("creator")
			input := reviewRunInput()
			input.RequestId = "second"
			second, err := r.api.RunAutomationReview(r.ctx, creator, input)
			require.NoError(t, err)
			second = approveRigReview(t, r, second, "reviewer")
			_, err = publishRigReview(r, second)
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			protectReviewRig(t, r, DefaultReviewPolicySettings())
			r.changeRule(t, "author")
			row := approveRigReview(t, r, r.run(t), "reviewer")
			tc.prepare(t, r, row)
			_, err := publishRigReview(r, row)
			reviewErrorCode(t, err, tc.code)
		})
	}
}

func TestProtectedPublishRequiresApprovedOutcome(t *testing.T) {
	for _, outcome := range []string{ReviewOutcomeFailed, ReviewOutcomeInconclusive, ReviewOutcomePassed} {
		t.Run(outcome, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			protectReviewRig(t, r, DefaultReviewPolicySettings())
			r.changeRule(t, "author")
			row := r.run(t)
			state := ReviewStateApproved
			if outcome == ReviewOutcomePassed {
				state = ReviewStateRan
			}
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Updates(map[string]any{"outcome": outcome, "state": state}).Error)
			_, err := publishRigReview(r, row)
			reviewErrorCode(t, err, "REVIEW_NOT_APPROVED")
		})
	}
}

func TestProtectionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *automationReviewRig)
	}{
		{"attribute", func(t *testing.T, r *automationReviewRig) {
			r.rule.Definition = datatypes.JSON(`{"thresholdAttr":"limit"}`)
		}},
		{"nested cel attribute", func(t *testing.T, r *automationReviewRig) {
			r.rule.Definition = datatypes.JSON(`{"actions":[{"cel":"attr.limit > 3"}]}`)
		}},
		{"group", func(t *testing.T, r *automationReviewRig) {
			token := "group"
			version := int32(1)
			r.rule.EntityGroupToken = &token
			r.rule.EntityGroupVersion = &version
		}},
		{"constant metric", func(t *testing.T, r *automationReviewRig) {
			metric := &MetricDefinition{DeviceProfileId: r.profile.ID, MetricKey: "calc", DataType: "DOUBLE", Expression: sql.NullString{String: "device.gain * 2.0", Valid: true}, TokenReference: rdb.TokenReference{Token: "calc"}}
			require.NoError(t, r.api.RDB.DB(r.ctx).Create(metric).Error)
			r.rule.Definition = datatypes.JSON(`{"metric":"calc"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			tc.prepare(t, r)
			// Install a historical active snapshot directly: eligibility is checked on that
			// version, even when the current publisher would reject an invalid formula.
			require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
			raw, err := r.api.buildProfileSnapshot(r.ctx, r.profile.ID)
			require.NoError(t, err)
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ? AND version = ?", r.profile.ID, 1).Update("snapshot", raw).Error)
			want := DefaultReviewPolicySettings()
			want.Required = true
			_, _, err = r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
			reviewErrorCode(t, err, "PROTECTION_INELIGIBLE")
			require.Contains(t, err.Error(), "r1")
		})
	}
	r := newAutomationReviewRig(t, false)
	want := DefaultReviewPolicySettings()
	want.Required = true
	_, _, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	reviewErrorCode(t, err, "PROFILE_NOT_PUBLISHED")
	want.RequiredApprovals = 3
	_, _, err = r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	reviewErrorCode(t, err, "INVALID_POLICY")
}

func TestReviewPolicyTighteningAndLoosening(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	view := protectReviewRig(t, r, DefaultReviewPolicySettings())
	require.EqualValues(t, 1, view.Stored.Revision)
	want := view.Effective
	want.RequiredApprovals = 2
	view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.True(t, applied)
	require.EqualValues(t, 2, view.Stored.Revision)
	want.Required = false
	view, applied, err = r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.False(t, applied)
	require.True(t, view.Effective.Required)
	require.NotNil(t, view.Pending)
	require.False(t, view.Pending.EffectiveAt.Valid)
	token := view.Pending.Token
	_, err = r.api.ApproveAutomationReviewPolicyChange(r.ctx, token)
	reviewErrorCode(t, err, actiongate.CodeSelfApproval)
	_, err = r.api.ApproveAutomationReviewPolicyChange(withoutReviewMFA("second"), token)
	reviewErrorCode(t, err, actiongate.CodeMfaRequired)
	ctx, _ := reviewIdentity("second")
	view, err = r.api.ApproveAutomationReviewPolicyChange(ctx, token)
	require.NoError(t, err)
	require.False(t, view.Effective.Required)
	require.Nil(t, view.Pending)
	require.EqualValues(t, 3, view.Stored.Revision)
	var change AutomationReviewPolicyChange
	require.NoError(t, r.api.RDB.DB(ctx).Where("token = ?", token).First(&change).Error)
	require.Equal(t, "APPLIED", change.State)
	require.Equal(t, "second", change.DecidedBy.String)
	_, err = r.api.CancelAutomationReviewPolicyChange(ctx, token)
	reviewErrorCode(t, err, "POLICY_CHANGE_NOT_PENDING")
}

func TestReviewPolicyCancelSupersedeAndDelay(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	settings := DefaultReviewPolicySettings()
	settings.AllowSelfApproval = true
	protectReviewRig(t, r, settings)
	want := settings
	want.Required = false
	before := reviewNow().UTC()
	view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.False(t, applied)
	pending := view.Pending
	require.True(t, pending.EffectiveAt.Valid)
	require.WithinDuration(t, before.Add(24*time.Hour), pending.EffectiveAt.Time, time.Second)
	require.True(t, effectivePolicy(view.Stored, pending, pending.EffectiveAt.Time.Add(-time.Nanosecond)).Required)
	require.False(t, effectivePolicy(view.Stored, pending, pending.EffectiveAt.Time).Required)
	view, err = r.api.CancelAutomationReviewPolicyChange(withoutReviewMFA("creator"), pending.Token)
	require.NoError(t, err)
	require.Nil(t, view.Pending)
	require.True(t, view.Effective.Required)
	view, _, err = r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	first := view.Pending.Token
	view, _, err = r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.NotEqual(t, first, view.Pending.Token)
	var superseded AutomationReviewPolicyChange
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("token = ?", first).First(&superseded).Error)
	require.Equal(t, "SUPERSEDED", superseded.State)
	now := reviewNow
	reviewNow = func() time.Time { return view.Pending.EffectiveAt.Time }
	t.Cleanup(func() { reviewNow = now })
	effective, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
	require.NoError(t, err)
	require.False(t, effective.Effective.Required)
	// A locked path materializes the delay, attributing it to the proposer.
	_, err = r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	stored, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
	require.NoError(t, err)
	require.Nil(t, stored.Pending)
	require.False(t, stored.Stored.Required)
	var change AutomationReviewPolicyChange
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("token = ?", view.Pending.Token).First(&change).Error)
	require.Equal(t, "delay", change.DecidedBy.String)
}

func TestReviewApprovalUsesPolicy(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	settings := DefaultReviewPolicySettings()
	settings.RequiredApprovals = 2
	protectReviewRig(t, r, settings)
	r.changeRule(t, "author")
	row := r.run(t)
	first := approveRigReview(t, r, row, "first")
	require.Equal(t, ReviewStateRan, first.State)
	require.Equal(t, 2, first.RequiredApprovals)
	second := approveRigReview(t, r, first, "second")
	require.Equal(t, ReviewStateApproved, second.State)
	require.EqualValues(t, 1, *second.ApprovedPolicyRevision)
	require.WithinDuration(t, second.ApprovedAt.Add(24*time.Hour), *second.ExpiresAt, time.Millisecond)
}
func TestReviewApprovalAllowsSelfAndUsesStepUpAge(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	settings := DefaultReviewPolicySettings()
	settings.AllowSelfApproval = true
	settings.StepUpMaxAgeSeconds = 60
	protectReviewRig(t, r, settings)
	row := r.run(t)
	ctx, a := reviewIdentity("creator")
	claims, _ := auth.ClaimsFromContext(ctx)
	claims.MfaTime.Time = time.Now().Add(-2 * time.Minute)
	_, err := r.api.ApproveAutomationReview(ctx, a, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, actiongate.CodeStepUpRequired)
	approved := approveRigReview(t, r, row, "creator")
	require.Equal(t, ReviewStateApproved, approved.State)
}

func TestProtectedRollbackBreakGlass(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	_, err := r.api.RollbackDeviceProfile(r.ctx, "profile", 1)
	reviewErrorCode(t, err, "BREAK_GLASS_REASON_REQUIRED")
	reason := "restore safe settings"
	_, err = r.api.RollbackDeviceProfileWithReason(withoutReviewMFA("operator"), "profile", 1, &reason)
	reviewErrorCode(t, err, actiongate.CodeMfaRequired)
	ctx, _ := reviewIdentity("operator")
	profile, err := r.api.RollbackDeviceProfileWithReason(ctx, "profile", 1, &reason)
	require.NoError(t, err)
	require.EqualValues(t, 1, profile.ActiveVersion.Int32)
	var rows []AutomationReviewBreakGlass
	require.NoError(t, r.api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "operator", rows[0].Actor)
	require.Equal(t, reason, rows[0].Reason)
	require.Equal(t, "ROLLBACK", rows[0].Action)
	require.Equal(t, "1", rows[0].Target)
}

func TestBindingBreakGlass(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unprotected", true: "protected"}[protected], func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			_, err := r.api.CreateDeviceType(r.ctx, &DeviceTypeCreateRequest{Token: "bound", ProfileToken: &r.profile.Token})
			require.NoError(t, err)
			_, err = r.api.CreateDeviceType(r.ctx, &DeviceTypeCreateRequest{Token: "other"})
			require.NoError(t, err)
			_, err = r.api.CreateDevice(r.ctx, &DeviceCreateRequest{Token: "device", DeviceTypeToken: "bound"})
			require.NoError(t, err)
			if protected {
				protectReviewRig(t, r, DefaultReviewPolicySettings())
			}
			move := &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("other")}
			repoint := &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalString{Set: true}}
			var reason *string
			if protected {
				_, err = r.api.UpdateDevice(r.ctx, "device", move)
				reviewErrorCode(t, err, "BREAK_GLASS_REASON_REQUIRED")
				_, err = r.api.UpdateDeviceType(r.ctx, "bound", repoint)
				reviewErrorCode(t, err, "BREAK_GLASS_REASON_REQUIRED")
				text := "emergency maintenance"
				reason = &text
				_, err = r.api.UpdateDeviceWithBreakGlass(withoutReviewMFA("operator"), "device", move, reason)
				reviewErrorCode(t, err, actiongate.CodeMfaRequired)
				_, err = r.api.UpdateDeviceTypeWithBreakGlass(withoutReviewMFA("operator"), "bound", repoint, reason)
				reviewErrorCode(t, err, actiongate.CodeMfaRequired)
			}
			_, err = r.api.UpdateDeviceWithBreakGlass(r.ctx, "device", move, reason)
			require.NoError(t, err)
			_, err = r.api.UpdateDeviceTypeWithBreakGlass(r.ctx, "bound", repoint, reason)
			require.NoError(t, err)

			if protected {
				attach := &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("profile")}
				_, err = r.api.UpdateDeviceType(r.ctx, "bound", attach)
				reviewErrorCode(t, err, "BREAK_GLASS_REASON_REQUIRED")
				text := "restore reviewed binding"
				_, err = r.api.UpdateDeviceTypeWithBreakGlass(r.ctx, "bound", attach, &text)
				require.NoError(t, err)
				// Provisioning and moving into a protected type do not need break-glass.
				_, err = r.api.CreateDevice(r.ctx, &DeviceCreateRequest{Token: "new-device", DeviceTypeToken: "bound"})
				require.NoError(t, err)
				_, err = r.api.UpdateDevice(r.ctx, "device", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("bound")})
				require.NoError(t, err)
			}
			var rows []AutomationReviewBreakGlass
			require.NoError(t, r.api.RDB.DB(r.ctx).Order("id").Find(&rows).Error)
			if protected {
				require.Len(t, rows, 3)
				require.Equal(t, "MOVE_DEVICE", rows[0].Action)
				require.Equal(t, "REPOINT_TYPE", rows[1].Action)
			} else {
				require.Empty(t, rows)
			}
		})
	}
}

func TestDeleteReviewProtectionAndRetention(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	_, err := r.api.DeleteDeviceProfile(r.ctx, "profile")
	reviewErrorCode(t, err, "PROFILE_PROTECTED")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	_, err = publishRigReview(r, row)
	require.NoError(t, err)
	input := reviewRunInput()
	input.RequestId = "pending-review"
	_, creator := reviewIdentity("creator")
	pending, err := r.api.RunAutomationReview(r.ctx, creator, input)
	require.NoError(t, err)

	reason := "restore baseline"
	_, err = r.api.RollbackDeviceProfileWithReason(r.ctx, "profile", 1, &reason)
	require.NoError(t, err)
	want := DefaultReviewPolicySettings()
	view, _, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	ctx, _ := reviewIdentity("second")
	_, err = r.api.ApproveAutomationReviewPolicyChange(ctx, view.Pending.Token)
	require.NoError(t, err)
	deleted, err := r.api.DeleteDeviceProfile(r.ctx, "profile")
	require.NoError(t, err)
	require.True(t, deleted)
	retained, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateConsumed, retained.State)
	var history int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AutomationReviewBreakGlass{}).Count(&history).Error)
	require.EqualValues(t, 1, history)
	_, err = r.api.AutomationReviewByToken(r.ctx, pending.Token)
	require.Error(t, err)
	for _, model := range []any{&AutomationReviewPolicy{}, &AutomationReviewPolicyChange{}} {
		var count int64
		require.NoError(t, r.api.RDB.DB(r.ctx).Model(model).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestMapAutomationReviewGuard(t *testing.T) {
	for _, tc := range []struct{ state, code string }{{"DCR01", "REVIEW_REQUIRED"}, {"DCR02", "PROFILE_PROTECTED"}} {
		err := mapAutomationReviewGuard(&pgconn.PgError{Code: tc.state, Message: tc.code + ": plain message"})
		reviewErrorCode(t, err, tc.code)
		require.Contains(t, err.Error(), "plain message")
		require.False(t, strings.Contains(err.Error(), "ADR-"))
	}
}

func TestUnprotectedRollbackNeedsNoReason(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	_, err := r.api.RollbackDeviceProfile(r.ctx, "profile", 1)
	require.NoError(t, err)
	var count int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AutomationReviewBreakGlass{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestProtectedPublishRejectsIneligibleDraft(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.rule.Definition = datatypes.JSON(`{"thresholdAttr":"limit","metric":"temp_c"}`)
	require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
	row := approveRigReview(t, r, r.run(t), "reviewer")
	_, err := publishRigReview(r, row)
	reviewErrorCode(t, err, "PROTECTION_INELIGIBLE")
	require.Zero(t, r.api.PrincipalStanding.(*fakePrincipalStanding).calls)
}

func TestPolicyDoesNotRequireEventRead(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	ctx, _ := reviewIdentity("operator")
	claims, _ := auth.ClaimsFromContext(ctx)
	claims.Authorities = []string{string(auth.DeviceWrite)}
	ctx = auth.WithClaims(ctx, claims)
	want := DefaultReviewPolicySettings()
	want.Required = true
	_, applied, err := r.api.SetAutomationReviewPolicy(ctx, "profile", want)
	require.NoError(t, err)
	require.True(t, applied)
	reason := "restore safe settings"
	_, err = r.api.RollbackDeviceProfileWithReason(ctx, "profile", 1, &reason)
	require.NoError(t, err)
}

func TestProtectedPublishStoredExpiryWinsOverLaterLoosening(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	settings := DefaultReviewPolicySettings()
	settings.ApprovalTtlSeconds = 3600
	view := protectReviewRig(t, r, settings)
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	require.NotNil(t, row.ApprovedAt)
	require.NotNil(t, row.ExpiresAt)
	require.WithinDuration(t, row.ApprovedAt.Add(time.Hour), *row.ExpiresAt, time.Millisecond)

	want := view.Effective
	want.ApprovalTtlSeconds = 7 * 24 * 3600
	view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", want)
	require.NoError(t, err)
	require.False(t, applied)
	require.NotNil(t, view.Pending)
	ctx, _ := reviewIdentity("second")
	view, err = r.api.ApproveAutomationReviewPolicyChange(ctx, view.Pending.Token)
	require.NoError(t, err)
	require.Nil(t, view.Pending)
	require.Equal(t, want, view.Effective)

	now := row.ApprovedAt.Add(2 * time.Hour)
	require.True(t, now.Before(row.ApprovedAt.Add(time.Duration(view.Effective.ApprovalTtlSeconds)*time.Second)))
	oldNow := reviewNow
	reviewNow = func() time.Time { return now }
	t.Cleanup(func() { reviewNow = oldNow })
	var before, after int64
	db := r.api.RDB.DB(r.ctx)
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&before).Error)
	_, err = publishRigReview(r, row)
	reviewErrorCode(t, err, "REVIEW_EXPIRED")
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestProtectedPublishRefusesWhenValidatedBytesDifferFromApproved(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	approvedDefinition := string(r.rule.Definition)
	r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":24}`)
	require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)

	var before, after int64
	db := r.api.RDB.Database
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&before).Error)
	armed, restored := false, false
	// The detection_rules read is the last child read in the pre-transaction
	// snapshot build. Arm only after it completes, so neither the initial token
	// lookup nor that snapshot's device_profiles read can trigger the restore.
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:review_snapshot_read", func(tx *gorm.DB) {
		if tx.Statement.Table == "detection_rules" && !restored {
			armed = true
		}
	}))
	defer func() { require.NoError(t, db.Callback().Query().Remove("test:review_snapshot_read")) }()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:review_restore_draft", func(tx *gorm.DB) {
		if !armed || restored || tx.Statement.Table != "device_profiles" {
			return
		}
		// This is the in-transaction profile lock, after the gates saw draft E.
		// Restore D without changing bookkeeping, so the rebuilt digest matches
		// the approval and only the first frozen-draft check can refuse E.
		restored = true
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE detection_rules SET definition = ? WHERE id = ?", approvedDefinition, r.rule.ID).Error)
	}))
	defer func() { require.NoError(t, db.Callback().Query().Remove("test:review_restore_draft")) }()

	_, err := publishRigReview(r, row)
	require.True(t, restored, "the draft must be restored after the pre-transaction snapshot was read")
	reviewErrorCode(t, err, "REVIEW_STALE_DRAFT_CHANGED")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestProtectedPublishLosesToAConcurrentReject(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	var before, after int64
	db := r.api.RDB.Database
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&before).Error)
	rejected := false
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:review_consume_reject", func(tx *gorm.DB) {
		if rejected || tx.Statement.Table != "automation_reviews" {
			return
		}
		set, ok := tx.Statement.Dest.(map[string]any)
		if !ok || set["state"] != ReviewStateConsumed {
			return
		}
		rejected = true
		// SQLite cannot run a second writer while this transaction holds its
		// read locks. Simulate the committed reject on this connection, then
		// start the losing publish's transaction again: its rollback must not
		// undo the reject. No publish writes have happened at this point.
		// Raw SQL bypasses Update callbacks, avoiding recursion.
		raw := tx.Session(&gorm.Session{NewDB: true})
		if err := raw.Exec("UPDATE automation_reviews SET state = ?, revision = revision + 1 WHERE id = ?", ReviewStateRejected, row.ID).Error; err != nil {
			tx.AddError(err)
			return
		}
		if err := raw.Exec("COMMIT").Error; err != nil {
			tx.AddError(err)
			return
		}
		tx.AddError(raw.Exec("BEGIN").Error)
	}))
	defer func() { require.NoError(t, db.Callback().Update().Remove("test:review_consume_reject")) }()

	_, err := publishRigReview(r, row)
	require.True(t, rejected, "the competing reject must run at the consume CAS")
	reviewErrorCode(t, err, "REVIEW_ALREADY_USED")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&after).Error)
	require.Equal(t, before, after)
	stored, err := r.api.AutomationReviewByToken(r.ctx, row.Token)
	require.NoError(t, err)
	require.Equal(t, ReviewStateRejected, stored.State)
	require.Equal(t, row.Revision+1, stored.Revision)
	require.Nil(t, stored.ConsumedVersion)
}

func TestBreakGlassRollbackRefusesIneligibleVersion(t *testing.T) {
	r := newAutomationReviewRig(t, false)
	r.rule.Enabled = true
	r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","thresholdAttr":"limit"}`)
	require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
	bad, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":26}`)
	require.NoError(t, r.api.RDB.DB(r.ctx).Save(r.rule).Error)
	good, err := r.api.PublishDeviceProfile(r.ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	var before, after int64
	db := r.api.RDB.DB(r.ctx)
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&before).Error)
	reason := "restore previous settings during incident"
	ctx, _ := reviewIdentity("operator")
	_, err = r.api.RollbackDeviceProfileWithReason(ctx, "profile", bad.Version, &reason)
	reviewErrorCode(t, err, "PROTECTION_INELIGIBLE")
	var profile DeviceProfile
	require.NoError(t, db.First(&profile, r.profile.ID).Error)
	require.True(t, profile.ActiveVersion.Valid)
	require.Equal(t, good.Version, profile.ActiveVersion.Int32)
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", r.profile.ID).Count(&after).Error)
	require.Equal(t, before, after)
	var breakGlassCount int64
	require.NoError(t, db.Model(&AutomationReviewBreakGlass{}).Where("device_profile_id = ?", r.profile.ID).Count(&breakGlassCount).Error)
	require.Zero(t, breakGlassCount)
}

func TestEveryLooseningDimensionIsPending(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stricter func(*ReviewPolicySettings)
		looser   func(*ReviewPolicySettings)
	}{
		{"approvals", func(p *ReviewPolicySettings) { p.RequiredApprovals = 2 }, func(p *ReviewPolicySettings) { p.RequiredApprovals = 1 }},
		{"self approval", func(p *ReviewPolicySettings) { p.AllowSelfApproval = false }, func(p *ReviewPolicySettings) { p.AllowSelfApproval = true }},
		{"approval TTL", func(p *ReviewPolicySettings) { p.ApprovalTtlSeconds = 3600 }, func(p *ReviewPolicySettings) { p.ApprovalTtlSeconds = 604800 }},
		{"step-up age", func(p *ReviewPolicySettings) { p.StepUpMaxAgeSeconds = 60 }, func(p *ReviewPolicySettings) { p.StepUpMaxAgeSeconds = 3600 }},
		{"unprotect", func(p *ReviewPolicySettings) { p.Required = true }, func(p *ReviewPolicySettings) { p.Required = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			strict := DefaultReviewPolicySettings()
			strict.Required = true
			tc.stricter(&strict)
			loose := strict
			tc.looser(&loose)
			t.Run("loosening", func(t *testing.T) {
				r := newAutomationReviewRig(t, true)
				original := protectReviewRig(t, r, strict)
				view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", loose)
				require.NoError(t, err)
				require.False(t, applied)
				require.NotNil(t, view.Pending)
				require.Equal(t, "PENDING", view.Pending.State)
				require.Equal(t, strict, view.Effective)
				require.Equal(t, strict, view.Stored.Settings())
				require.Equal(t, original.Stored.Revision, view.Stored.Revision)
				stored, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
				require.NoError(t, err)
				require.NotNil(t, stored.Pending)
				require.Equal(t, view.Pending.Token, stored.Pending.Token)
				require.Equal(t, "PENDING", stored.Pending.State)
				require.Equal(t, strict, stored.Effective)
				require.Equal(t, strict, stored.Stored.Settings())
			})
			if tc.name == "unprotect" {
				return // Enabling protection is already exercised by protectReviewRig.
			}
			t.Run("tightening", func(t *testing.T) {
				r := newAutomationReviewRig(t, true)
				original := protectReviewRig(t, r, loose)
				view, applied, err := r.api.SetAutomationReviewPolicy(r.ctx, "profile", strict)
				require.NoError(t, err)
				require.True(t, applied)
				require.Nil(t, view.Pending)
				require.Equal(t, strict, view.Effective)
				require.Equal(t, strict, view.Stored.Settings())
				require.Equal(t, original.Stored.Revision+1, view.Stored.Revision)
				stored, err := r.api.AutomationReviewPolicyFor(r.ctx, "profile")
				require.NoError(t, err)
				require.Nil(t, stored.Pending)
				require.Equal(t, strict, stored.Effective)
				require.Equal(t, strict, stored.Stored.Settings())
			})
		})
	}
}

// A self-approval counted while the policy allowed it stops counting once the policy forbids
// it: the publish re-tallies the approvals under the policy in force, not the one at approval.
func TestProtectedPublishRetalliesSelfApprovalUnderTheCurrentPolicy(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	settings := DefaultReviewPolicySettings()
	settings.AllowSelfApproval = true
	protectReviewRig(t, r, settings)
	row := r.run(t)
	approved := approveRigReview(t, r, row, "creator")
	require.Equal(t, ReviewStateApproved, approved.State)
	settings.AllowSelfApproval = false
	view := protectReviewRig(t, r, settings)
	require.False(t, view.Effective.AllowSelfApproval)
	var before, after int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Count(&before).Error)
	_, err := publishRigReview(r, approved)
	reviewErrorCode(t, err, "REVIEW_STALE_POLICY_CHANGED")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&DeviceProfileVersion{}).Count(&after).Error)
	require.Equal(t, before, after)
}
