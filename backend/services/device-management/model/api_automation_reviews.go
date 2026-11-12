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
	"unicode"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// These defaults will be resolved from per-profile policy when enforcement lands.
const (
	DefaultReviewRequiredApprovals = 1
	DefaultReviewAllowSelfApproval = false
	DefaultReviewApprovalTTL       = 24 * time.Hour
	DefaultReviewStepUpMaxAge      = 15 * time.Minute
	MaxReviewsPerHour              = 60
)

// ErrReviewUnavailable distinguishes missing evidence from an engine verdict.
var ErrReviewUnavailable = actiongate.Refusal("REVIEW_UNAVAILABLE", "automation comparison is unavailable", false)

var errReviewConcurrentApproval = actiongate.Refusal(actiongate.CodeConcurrentApproval, "another review decision landed at the same time; reload and try again", false)

func reviewRefusal(code, msg string) error { return actiongate.Refusal(code, msg, false) }

// AuthorizeReviewPerson refuses machine principals even when they hold device:write.
func AuthorizeReviewPerson(ctx context.Context, authority auth.Authority) (actiongate.Approver, error) {
	a, err := actiongate.AuthorizeApprover(ctx, authority)
	if err != nil {
		return actiongate.Approver{}, err
	}
	if strings.HasPrefix(a.Claims.Username, auth.ServiceAccountSubjectPrefix) || strings.HasPrefix(a.Claims.Subject, auth.ServiceAccountSubjectPrefix) {
		return actiongate.Approver{}, actiongate.Refusal("NOT_A_PERSON", "automation reviews require a person's own session", true)
	}
	return a, nil
}

// The context is the authority for identity and audit attribution. A supplied
// principal must agree with it, so a model caller cannot relabel another writer.
func reviewPerson(ctx context.Context, supplied actiongate.Approver) (actiongate.Approver, error) {
	a, err := AuthorizeReviewPerson(ctx, auth.DeviceWrite)
	if err != nil {
		return a, err
	}
	if supplied.Principal != a.Principal {
		return actiongate.Approver{}, auth.ErrForbidden
	}
	return a, nil
}

type RunAutomationReviewInput struct {
	ProfileToken       string
	RuleToken          *string
	Windows            []ReviewWindow
	DeviceTokens       []string
	Expectations       []ReviewExpectation
	ShiftWindowSeconds *int32
	RequestId          string
}

func normalizeReviewInput(in RunAutomationReviewInput) (RunAutomationReviewInput, datatypes.JSON, datatypes.JSON, error) {
	invalid := func(msg string) (RunAutomationReviewInput, datatypes.JSON, datatypes.JSON, error) {
		return in, nil, nil, &InvalidArgumentError{Msg: msg}
	}
	if len(in.RequestId) < 1 || len(in.RequestId) > 128 || !utf8.ValidString(in.RequestId) {
		return invalid("requestId must be printable text of 1..128 bytes")
	}
	for _, r := range in.RequestId {
		if !unicode.IsPrint(r) {
			return invalid("requestId must be printable")
		}
	}
	in.DeviceTokens = slices.Clone(in.DeviceTokens)
	sort.Strings(in.DeviceTokens)
	in.DeviceTokens = slices.Compact(in.DeviceTokens)
	if len(in.DeviceTokens) < 1 || len(in.DeviceTokens) > 50 || in.DeviceTokens[0] == "" {
		return invalid("review requires 1..50 distinct non-empty device tokens")
	}
	if len(in.Windows) != 2 {
		return invalid("review requires one INCIDENT and one NORMAL window")
	}
	in.Windows = slices.Clone(in.Windows)
	sort.Slice(in.Windows, func(i, j int) bool { return in.Windows[i].Role < in.Windows[j].Role })
	if in.Windows[0].Role != "INCIDENT" || in.Windows[1].Role != "NORMAL" {
		return invalid("review requires one INCIDENT and one NORMAL window")
	}
	for i := range in.Windows {
		w := &in.Windows[i]
		if !w.Start.Before(w.End) {
			return invalid("window start must precede end")
		}
		w.Start, w.End = w.Start.UTC(), w.End.UTC()
	}
	if len(in.Expectations) > 10 {
		return invalid("review accepts at most ten expectations")
	}
	in.Expectations = append([]ReviewExpectation{}, in.Expectations...)
	for _, e := range in.Expectations {
		if e.Expect == "" || len(e.Expect) > 8192 || (e.Window != "INCIDENT" && e.Window != "NORMAL") {
			return invalid("expectations require a valid window and 1..8192 bytes of expectation text")
		}
	}
	scope, err := actiongate.Canonical(map[string]any{"devices": in.DeviceTokens, "windows": in.Windows, "shiftWindowSeconds": in.ShiftWindowSeconds})
	if err != nil {
		return in, nil, nil, err
	}
	expectations, err := actiongate.Canonical(in.Expectations)
	return in, datatypes.JSON(scope), datatypes.JSON(expectations), err
}

func (api *Api) reviewByRequest(ctx context.Context, request string) (*AutomationReview, error) {
	var rows []AutomationReview
	if err := api.RDB.DB(ctx).Where("request_id = ?", request).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func existingReview(r *AutomationReview, person, scope, expectations string) (*AutomationReview, error) {
	if r.CreatedBy != person || !actiongate.DigestEqual(r.ScopeDigest, scope) || !actiongate.DigestEqual(r.ExpectationsDigest, expectations) {
		return nil, reviewRefusal(actiongate.CodeRequestConflict, "this requestId already names a different review")
	}
	return r, nil
}

func (api *Api) latestReviewActivation(ctx context.Context, profileId uint) (*DeviceProfileActivation, error) {
	var rows []DeviceProfileActivation
	if err := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).Order("id DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// RunAutomationReview persists only completed comparisons, never an EP outage or refusal.
func (api *Api) RunAutomationReview(ctx context.Context, person actiongate.Approver, in RunAutomationReviewInput) (*AutomationReview, error) {
	person, err := reviewPerson(ctx, person)
	if err != nil {
		return nil, err
	}
	in, scope, expectations, err := normalizeReviewInput(in)
	if err != nil {
		return nil, err
	}
	// The bound is in the same zone gorm stamps created_at with, so a store that compares
	// timestamps as text (sqlite) orders them the same way PostgreSQL does.
	var count int64
	if err := api.RDB.DB(ctx).Model(&AutomationReview{}).Where("created_at >= ?", time.Now().Add(-time.Hour)).Count(&count).Error; err != nil {
		return nil, err
	}
	if count >= MaxReviewsPerHour {
		return nil, reviewRefusal("RATE_LIMITED", "at most 60 automation reviews may run per tenant per hour")
	}
	scopeDigest, expectationsDigest := reviewHash(scope), reviewHash(expectations)
	prior, err := api.reviewByRequest(ctx, in.RequestId)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return existingReview(prior, person.Principal, scopeDigest, expectationsDigest)
	}
	profile, err := api.deviceProfileByToken(ctx, in.ProfileToken)
	if err != nil {
		return nil, err
	}
	activation, err := api.latestReviewActivation(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	if activation == nil || !profile.ActiveVersion.Valid {
		return nil, reviewRefusal("NO_BASELINE", "the profile has no active baseline activation")
	}
	var version DeviceProfileVersion
	if err := api.RDB.DB(ctx).Where("device_profile_id = ? AND version = ?", profile.ID, activation.Version).First(&version).Error; err != nil {
		return nil, err
	}
	base, err := parseProfileSnapshot(version.Snapshot)
	if err != nil {
		return nil, err
	}
	raw, err := api.buildProfileSnapshot(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	draft, err := parseProfileSnapshot(raw)
	if err != nil {
		return nil, err
	}
	baselineDigest, err := SnapshotDigest(version.Snapshot)
	if err != nil {
		return nil, err
	}
	draftDigest, err := SnapshotDigest(raw)
	if err != nil {
		return nil, err
	}
	classification, err := classifyAutomationReview(base, draft)
	if err != nil {
		return nil, err
	}
	if in.RuleToken != nil {
		if classification.Kind == ReviewKindRuleChange && classification.RuleToken != *in.RuleToken {
			return nil, &InvalidArgumentError{Msg: "ruleToken does not name the changed rule"}
		}
		if classification.Kind == ReviewKindNoAutomationChange && snapshotRules(base)[*in.RuleToken] == nil && snapshotRules(draft)[*in.RuleToken] == nil {
			return nil, &InvalidArgumentError{Msg: "ruleToken names no baseline or draft rule"}
		}
	}
	authors, err := api.draftAuthors(ctx, profile, activation.ID, base, draft)
	if err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	r := &AutomationReview{TenantScoped: rdb.TenantScoped{TenantId: tenant}, DeviceProfileId: profile.ID,
		Kind: classification.Kind, RequestId: in.RequestId, BaselineActivationId: activation.ID, BaselineVersion: activation.Version,
		BaselineDigest: baselineDigest, DraftDigest: draftDigest, Scope: scope, ScopeDigest: scopeDigest,
		Expectations: expectations, ExpectationsDigest: expectationsDigest, Result: "{}", Outcome: ReviewOutcomeNoAutomationChange,
		StreamBounds: datatypes.JSON("[]"), Approvals: datatypes.JSON("[]"), RequiredApprovals: DefaultReviewRequiredApprovals,
		State: ReviewStateRan, CreatedBy: person.Principal}
	r.DraftAuthors, err = actiongate.Canonical(authors)
	if err != nil {
		return nil, err
	}
	r.ProfileChanges, err = actiongate.Canonical(classification.Changes)
	if err != nil {
		return nil, err
	}
	if err := api.fillReviewEvidence(ctx, r, in, classification, base, draft); err != nil {
		return nil, err
	}
	r.ReviewDigest, err = reviewDigestOf(r, profile.Token)
	if err != nil {
		return nil, err
	}
	token, err := actiongate.NewToken()
	if err != nil {
		return nil, err
	}
	r.Token = "acr_" + strings.TrimPrefix(token, actiongate.TokenPrefix)
	if err := api.RDB.DB(ctx).Create(r).Error; err != nil {
		if conflict.Is(err) {
			prior, readErr := api.reviewByRequest(ctx, in.RequestId)
			if readErr != nil {
				return nil, readErr
			}
			if prior != nil {
				return existingReview(prior, person.Principal, scopeDigest, expectationsDigest)
			}
		}
		return nil, err
	}
	return r, nil
}

// fillReviewEvidence has no storage effects: a refusal or unavailable engine
// leaves neither a review row nor an audit entry for an attempted review.
func (api *Api) fillReviewEvidence(ctx context.Context, r *AutomationReview, in RunAutomationReviewInput, classification *reviewClassification, base, draft *ProfileSnapshot) error {
	if classification.Kind == ReviewKindRuleChange {
		if api.RuleComparer == nil {
			return ErrReviewUnavailable
		}
		res, err := api.RuleComparer.CompareRuleVersions(ctx, CompareRequest{ProfileToken: in.ProfileToken,
			Base: classification.Base, Draft: classification.Draft, DeviceTokens: in.DeviceTokens, Windows: in.Windows,
			Expectations: in.Expectations, ShiftWindowSeconds: in.ShiftWindowSeconds, ConstantMetrics: constantMetrics(base, draft)})
		if err != nil || res == nil {
			return ErrReviewUnavailable
		}
		if !res.Ok {
			return reviewRefusal("REVIEW_REFUSED", strings.TrimSpace(res.RefusalReason+": "+res.RefusalMessage))
		}
		if !json.Valid([]byte(res.Result)) || !reviewResultDigestEqual(reviewHash([]byte(res.Result)), res.ResultDigest) ||
			!slices.Contains([]string{ReviewOutcomePassed, ReviewOutcomeFailed, ReviewOutcomeInconclusive}, res.Outcome) {
			return ErrReviewUnavailable
		}
		r.RuleToken = &classification.RuleToken
		r.BaseEnvelope, err = reviewEnvelopeJSON(classification.Base)
		if err != nil {
			return err
		}
		r.DraftEnvelope, err = reviewEnvelopeJSON(classification.Draft)
		if err != nil {
			return err
		}
		r.Result, r.ResultDigest, r.Outcome, r.InputDigest, r.EngineBuild = res.Result, res.ResultDigest, res.Outcome, res.InputDigest, res.EngineBuild
		bounds := make([]ReviewStreamBounds, 0, len(res.Windows))
		for _, w := range res.Windows {
			bounds = append(bounds, ReviewStreamBounds{Role: w.Role, Stream: w.Stream, FirstSeq: w.FirstSeq, LastSeq: w.LastSeq})
		}
		r.StreamBounds, err = actiongate.Canonical(bounds)
		if err != nil {
			return err
		}
	} else if classification.Kind == ReviewKindUnreviewable {
		b, err := actiongate.Canonical(map[string]any{"reasons": classification.Reasons})
		if err != nil {
			return err
		}
		r.Result, r.Outcome = string(b), ReviewOutcomeInconclusive
	}
	if r.ResultDigest == "" {
		r.ResultDigest = reviewHash([]byte(r.Result))
	}
	return nil
}

func reviewResultDigestEqual(a, b string) bool {
	return strings.TrimPrefix(a, "sha256:") == strings.TrimPrefix(b, "sha256:") && b != ""
}

func reviewEnvelopeJSON(e *RuleEnvelope) (*datatypes.JSON, error) {
	if e == nil {
		return nil, nil
	}
	b, err := actiongate.Canonical(e)
	if err != nil {
		return nil, err
	}
	j := datatypes.JSON(b)
	return &j, nil
}

// AutomationReviewByToken keeps missing and inaccessible rows indistinguishable.
func (api *Api) AutomationReviewByToken(ctx context.Context, token string) (*AutomationReview, error) {
	var rows []AutomationReview
	if err := api.RDB.DB(ctx).Where("token = ?", token).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &rows[0], nil
}

// CurrentDraftDigest shares the exact snapshot builder used by publication.
func (api *Api) CurrentDraftDigest(ctx context.Context, profileId uint) (string, error) {
	raw, err := api.buildProfileSnapshot(ctx, profileId)
	if err != nil {
		return "", err
	}
	return SnapshotDigest(raw)
}

type ReviewApproval struct {
	actiongate.Approval
	PolicyRevision int `json:"policyRevision"`
}

// checkReviewCurrent runs before step-up, so an approver is never asked to verify
// a second factor for evidence that already describes an obsolete draft/baseline.
func (api *Api) checkReviewCurrent(ctx context.Context, r *AutomationReview) error {
	digest, err := api.CurrentDraftDigest(ctx, r.DeviceProfileId)
	if err != nil {
		return err
	}
	if !actiongate.DigestEqual(digest, r.DraftDigest) {
		return reviewRefusal("REVIEW_STALE_DRAFT_CHANGED", "DRAFT_CHANGED: the draft changed after this review")
	}
	activation, err := api.latestReviewActivation(ctx, r.DeviceProfileId)
	if err != nil {
		return err
	}
	if activation == nil || activation.ID != r.BaselineActivationId {
		return reviewRefusal("REVIEW_STALE_BASELINE_CHANGED", "BASELINE_CHANGED: the active baseline changed after this review")
	}
	return nil
}

// recordReviewApproval is a single journaled compare-and-set; a stale revision
// cannot overwrite another person's approval or a rejection.
func (api *Api) recordReviewApproval(ctx context.Context, r *AutomationReview, approvals []ReviewApproval, now time.Time) error {
	raw, err := actiongate.Canonical(approvals)
	if err != nil {
		return err
	}
	set := map[string]any{"approvals": datatypes.JSON(raw), "revision": r.Revision + 1}
	principals := map[string]bool{}
	for _, a := range approvals {
		if a.Sub != "" {
			principals[a.Sub] = true
		}
	}
	if len(principals) >= r.RequiredApprovals {
		set["state"], set["approved_at"], set["expires_at"], set["approved_policy_revision"] = ReviewStateApproved, now, now.Add(DefaultReviewApprovalTTL), 0
	}
	res := api.RDB.DB(ctx).Model(r).Where("token = ? AND state = ? AND revision = ?", r.Token, ReviewStateRan, r.Revision).Updates(set)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errReviewConcurrentApproval
	}
	return nil
}

// ApproveAutomationReview binds a fresh, independent person to the frozen evidence.
func (api *Api) ApproveAutomationReview(ctx context.Context, approver actiongate.Approver, token, digest string) (*AutomationReview, error) {
	approver, err := reviewPerson(ctx, approver)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		r, err := api.AutomationReviewByToken(ctx, token)
		if err != nil {
			return nil, err
		}
		if r.State != ReviewStateRan {
			return nil, reviewRefusal("REVIEW_NOT_PENDING", "the review is no longer pending")
		}
		if r.Outcome != ReviewOutcomePassed && r.Outcome != ReviewOutcomeNoAutomationChange {
			return nil, reviewRefusal("NOT_APPROVABLE", "only passed or non-automation reviews may be approved")
		}
		if !actiongate.DigestEqual(r.ReviewDigest, digest) {
			return nil, reviewRefusal(actiongate.CodeDigestMismatch, "the digest does not match the stored review")
		}
		var profile DeviceProfile
		if err := api.RDB.DB(ctx).First(&profile, r.DeviceProfileId).Error; err != nil {
			return nil, err
		}
		computed, err := reviewDigestOf(r, profile.Token)
		if err != nil {
			return nil, err
		}
		if !actiongate.DigestEqual(computed, r.ReviewDigest) {
			return nil, reviewRefusal(actiongate.CodeDigestMismatch, "the stored review digest does not verify")
		}
		if err := api.checkReviewCurrent(ctx, r); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if err := actiongate.CheckStepUp(approver, auth.StepUp{MaxAge: DefaultReviewStepUpMaxAge}, now); err != nil {
			return nil, err
		}
		var authors []string
		if err := json.Unmarshal(r.DraftAuthors, &authors); err != nil {
			return nil, err
		}
		if authors == nil {
			return nil, fmt.Errorf("stored review draft authors are missing")
		}
		if !DefaultReviewAllowSelfApproval && (approver.Principal == r.CreatedBy || slices.Contains(authors, approver.Principal)) {
			return nil, actiongate.Refusal(actiongate.CodeSelfApproval, "the review creator and draft authors cannot approve this review", true)
		}
		var approvals []ReviewApproval
		if err := json.Unmarshal(r.Approvals, &approvals); err != nil {
			return nil, err
		}
		if approvals == nil {
			return nil, fmt.Errorf("stored review approvals are missing")
		}
		for _, a := range approvals {
			if a.Sub == approver.Principal {
				return r, nil
			}
		}
		approvals = append(approvals, ReviewApproval{Approval: actiongate.Approval{Sub: approver.Principal, Amr: approver.Amr, MfaTime: approver.MfaTime, At: now}, PolicyRevision: 0})
		err = api.recordReviewApproval(ctx, r, approvals, now)
		if errors.Is(err, errReviewConcurrentApproval) && attempt == 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		return api.AutomationReviewByToken(ctx, token)
	}
	return nil, errReviewConcurrentApproval
}

// RejectAutomationReview only stops a review, so needs neither step-up nor author exclusion.
func (api *Api) RejectAutomationReview(ctx context.Context, person actiongate.Approver, token string, note *string) (*AutomationReview, error) {
	person, err := reviewPerson(ctx, person)
	if err != nil {
		return nil, err
	}
	if note != nil && (len(*note) > 512 || !utf8.ValidString(*note) || strings.ContainsRune(*note, 0)) {
		return nil, &InvalidArgumentError{Msg: "note must be valid text of at most 512 bytes"}
	}
	r, err := api.AutomationReviewByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if r.State != ReviewStateRan && r.State != ReviewStateApproved {
		return nil, reviewRefusal("REVIEW_NOT_PENDING", "the review is no longer pending")
	}
	res := api.RDB.DB(ctx).Model(r).Where("token = ? AND state IN ? AND revision = ?", token, []string{ReviewStateRan, ReviewStateApproved}, r.Revision).
		Updates(map[string]any{"state": ReviewStateRejected, "decided_by": person.Principal, "decided_at": time.Now().UTC(), "decided_note": note, "revision": r.Revision + 1})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, reviewRefusal("REVIEW_NOT_PENDING", "the review is no longer pending")
	}
	return api.AutomationReviewByToken(ctx, token)
}

type AutomationReviewSearchCriteria struct {
	rdb.Pagination
	ProfileToken         string
	State                *string
	MatchingCurrentDraft *bool
}

type AutomationReviewSearchResults struct {
	Results    []AutomationReview
	Pagination rdb.SearchResultsPagination
}

// AutomationReviews pages the tenant ledger in its total newest-first order.
func (api *Api) AutomationReviews(ctx context.Context, criteria AutomationReviewSearchCriteria) (*AutomationReviewSearchResults, error) {
	if criteria.State != nil && !slices.Contains([]string{ReviewStateRan, ReviewStateApproved, ReviewStateRejected, ReviewStateConsumed, ReviewStateExpired}, *criteria.State) {
		return nil, &InvalidArgumentError{Msg: "unknown review state"}
	}
	profile, err := api.deviceProfileByToken(ctx, criteria.ProfileToken)
	if err != nil {
		return nil, err
	}
	var digest string
	if criteria.MatchingCurrentDraft != nil {
		digest, err = api.CurrentDraftDigest(ctx, profile.ID)
		if err != nil {
			return nil, err
		}
	}
	rows := make([]AutomationReview, 0)
	db, pag := api.RDB.ListOf(ctx, &AutomationReview{}, func(q *gorm.DB) *gorm.DB {
		q = q.Where("automation_reviews.device_profile_id = ?", profile.ID)
		if criteria.State != nil {
			q = q.Where("automation_reviews.state = ?", *criteria.State)
		}
		if criteria.MatchingCurrentDraft != nil {
			if *criteria.MatchingCurrentDraft {
				q = q.Where("automation_reviews.draft_digest = ?", digest)
			} else {
				q = q.Where("automation_reviews.draft_digest <> ?", digest)
			}
		}
		return q
	}, criteria.Pagination)
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	return &AutomationReviewSearchResults{Results: rows, Pagination: pag}, nil
}
