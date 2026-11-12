// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PrincipalStandingChecker interface {
	PrincipalStanding(context.Context, []string, []string) (map[string]bool, error)
}

func mapAutomationReviewGuard(err error) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	code := ""
	switch pg.Code {
	case "DCR01":
		code = "REVIEW_REQUIRED"
	case "DCR02":
		code = "PROFILE_PROTECTED"
	default:
		return err
	}
	message := pg.Message
	if _, after, ok := strings.Cut(message, ": "); ok {
		message = after
	}
	return reviewRefusal(code, message)
}

func qualifyingReviewApprovers(r *AutomationReview, p ReviewPolicySettings) ([]string, error) {
	var authors []string
	var approvals []ReviewApproval
	if err := json.Unmarshal(r.DraftAuthors, &authors); err != nil || authors == nil {
		return nil, reviewRefusal("REVIEW_UNAVAILABLE", "review authors are unreadable")
	}
	if err := json.Unmarshal(r.Approvals, &approvals); err != nil || approvals == nil {
		return nil, reviewRefusal("REVIEW_UNAVAILABLE", "review approvals are unreadable")
	}
	distinct := map[string]bool{}
	for _, a := range approvals {
		if a.Sub == "" || !p.AllowSelfApproval && (a.Sub == r.CreatedBy || slices.Contains(authors, a.Sub)) {
			continue
		}
		if auth.StepUpFreshAt(a.Amr, a.MfaTime, auth.StepUp{MaxAge: time.Duration(p.StepUpMaxAgeSeconds) * time.Second}, a.At) {
			distinct[a.Sub] = true
		}
	}
	out := make([]string, 0, len(distinct))
	for sub := range distinct {
		out = append(out, sub)
	}
	sort.Strings(out)
	return out, nil
}

func (api *Api) checkReviewStanding(ctx context.Context, principals []string) error {
	if api.PrincipalStanding == nil {
		return reviewRefusal("REVIEW_UNAVAILABLE", "approver standing is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	standing, err := api.PrincipalStanding.PrincipalStanding(ctx, principals, []string{"device:write", "event:read"})
	if err != nil || ctx.Err() != nil || len(standing) != len(principals) {
		return reviewRefusal("REVIEW_UNAVAILABLE", "approver standing is unavailable")
	}
	for _, sub := range principals {
		if _, ok := standing[sub]; !ok {
			return reviewRefusal("REVIEW_UNAVAILABLE", "approver standing is incomplete")
		}
	}
	for _, sub := range principals {
		if !standing[sub] {
			return reviewRefusal("APPROVER_NO_LONGER_QUALIFIES", fmt.Sprintf("approver %s no longer qualifies", sub))
		}
	}
	return nil
}

func (api *Api) checkProtectedPublish(ctx context.Context, tx *gorm.DB, profile *DeviceProfile, token *string, snapshot datatypes.JSON, p ReviewPolicySettings, now time.Time) (*AutomationReview, error) {
	if token == nil || *token == "" {
		return nil, reviewRefusal("REVIEW_REQUIRED", "this device profile requires an approved automation review to publish")
	}
	var r AutomationReview
	// Lock the review under the profile lock to serialize consumption and rejection.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", *token).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, reviewRefusal("REVIEW_NOT_FOUND", "the automation review was not found")
		}
		return nil, err
	}
	if r.DeviceProfileId != profile.ID {
		return nil, reviewRefusal("REVIEW_WRONG_PROFILE", "the automation review belongs to another device profile")
	}
	if r.State == ReviewStateConsumed {
		return nil, reviewRefusal("REVIEW_ALREADY_USED", "the automation review has already been published")
	}
	if r.State != ReviewStateApproved || (r.Outcome != ReviewOutcomePassed && r.Outcome != ReviewOutcomeNoAutomationChange) || (r.Kind != ReviewKindRuleChange && r.Kind != ReviewKindNoAutomationChange) {
		return nil, reviewRefusal("REVIEW_NOT_APPROVED", "the automation review is not approved for publish")
	}
	digest, err := reviewDigestOf(&r, profile.Token)
	if err != nil || !reviewEvidenceVerifies(&r) || !actiongate.DigestEqual(digest, r.ReviewDigest) {
		return nil, reviewRefusal(actiongate.CodeDigestMismatch, "the stored review evidence does not verify")
	}
	if r.ExpiresAt == nil || r.ApprovedAt == nil || !now.Before(*r.ExpiresAt) || !now.Before(r.ApprovedAt.Add(time.Duration(p.ApprovalTtlSeconds)*time.Second)) {
		return nil, reviewRefusal("REVIEW_EXPIRED", "the automation review approval has expired")
	}
	if err = checkFrozenReviewDraft(snapshot, &r); err != nil {
		return nil, err
	}
	snap, err := parseProfileSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if err = refuseProtectionIneligible(snap); err != nil {
		return nil, err
	}
	var activations []DeviceProfileActivation
	if err = tx.Where("device_profile_id = ?", profile.ID).Order("id DESC").Limit(1).Find(&activations).Error; err != nil {
		return nil, err
	}
	if len(activations) != 1 || activations[0].ID != r.BaselineActivationId {
		return nil, reviewRefusal("REVIEW_STALE_BASELINE_CHANGED", "the active baseline changed after this review")
	}
	principals, err := qualifyingReviewApprovers(&r, p)
	if err != nil {
		return nil, err
	}
	if len(principals) < p.RequiredApprovals {
		return nil, reviewRefusal("REVIEW_STALE_POLICY_CHANGED", "the approvals no longer satisfy the current protection policy")
	}
	if err = api.checkReviewStanding(ctx, principals); err != nil {
		return nil, err
	}
	return &r, nil
}

func checkFrozenReviewDraft(snapshot datatypes.JSON, r *AutomationReview) error {
	digest, err := SnapshotDigest(snapshot)
	if err != nil {
		return err
	}
	if !actiongate.DigestEqual(digest, r.DraftDigest) {
		return reviewRefusal("REVIEW_STALE_DRAFT_CHANGED", "the draft changed after this review")
	}
	return nil
}

func consumeAutomationReview(tx *gorm.DB, r *AutomationReview, next int32, now time.Time) error {
	set := map[string]any{"state": ReviewStateConsumed, "consumed_version": next, "revision": r.Revision + 1, "updated_at": now}
	if tx.Dialector.Name() == "postgres" {
		set["consumed_txid"] = gorm.Expr("txid_current()")
	}
	res := tx.Model(r).Where("id = ? AND state = ? AND revision = ?", r.ID, ReviewStateApproved, r.Revision).Updates(set)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return reviewRefusal("REVIEW_ALREADY_USED", "the automation review has already been published")
	}
	return nil
}
