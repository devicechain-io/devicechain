// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ReviewPolicyLooseningDelay = 24 * time.Hour

var reviewNow = time.Now

func policyPerson(ctx context.Context) (actiongate.Approver, error) {
	a, err := actiongate.AuthorizeApprover(ctx, auth.DeviceWrite)
	if err != nil {
		return a, err
	}
	if strings.HasPrefix(a.Claims.Username, auth.ServiceAccountSubjectPrefix) || strings.HasPrefix(a.Claims.Subject, auth.ServiceAccountSubjectPrefix) {
		return a, reviewRefusal("NOT_A_PERSON", "automation protection requires a person's own session")
	}
	return a, nil
}

// NO KEY UPDATE serializes policy and activation changes with the guards without
// blocking draft foreign keys' KEY SHARE locks.
func lockProfile(tx *gorm.DB, id uint) (*DeviceProfile, error) {
	var p DeviceProfile
	err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&p, id).Error
	return &p, err
}

func validateReviewPolicy(want ReviewPolicySettings) error {
	if want.RequiredApprovals < 1 || want.RequiredApprovals > 2 || want.ApprovalTtlSeconds < 900 || want.ApprovalTtlSeconds > 604800 || want.StepUpMaxAgeSeconds < 60 || want.StepUpMaxAgeSeconds > 3600 {
		return reviewRefusal("INVALID_POLICY", "approvals must be 1 or 2, approval lifetime 900 to 604800 seconds, and step-up age 60 to 3600 seconds")
	}
	return nil
}

func readReviewPolicy(tx *gorm.DB, id uint) (*AutomationReviewPolicy, *AutomationReviewPolicyChange, error) {
	var policies []AutomationReviewPolicy
	if err := tx.Where("device_profile_id = ?", id).Limit(1).Find(&policies).Error; err != nil {
		return nil, nil, err
	}
	var changes []AutomationReviewPolicyChange
	if err := tx.Where("device_profile_id = ? AND state = ?", id, "PENDING").Order("id DESC").Limit(1).Find(&changes).Error; err != nil {
		return nil, nil, err
	}
	var p *AutomationReviewPolicy
	var c *AutomationReviewPolicyChange
	if len(policies) > 0 {
		p = &policies[0]
	}
	if len(changes) > 0 {
		c = &changes[0]
	}
	return p, c, nil
}

// Invalid stored JSON is never an effective loosening. Writers validate it before applying.
func effectivePolicy(stored *AutomationReviewPolicy, pending *AutomationReviewPolicyChange, now time.Time) ReviewPolicySettings {
	settings := DefaultReviewPolicySettings()
	if stored != nil {
		settings = stored.Settings()
	}
	if pending != nil && pending.State == "PENDING" && pending.EffectiveAt.Valid && !now.Before(pending.EffectiveAt.Time) {
		var proposed ReviewPolicySettings
		if json.Unmarshal(pending.Proposed, &proposed) == nil && validateReviewPolicy(proposed) == nil {
			settings = proposed
		}
	}
	return settings
}

func writeReviewPolicy(tx *gorm.DB, id uint, p *AutomationReviewPolicy, want ReviewPolicySettings, actor string) error {
	if err := validateReviewPolicy(want); err != nil {
		return err
	}
	revision := int32(1)
	if p != nil {
		revision = p.Revision + 1
	}
	if p == nil {
		p = &AutomationReviewPolicy{DeviceProfileId: id}
	}
	p.Required = want.Required
	p.RequiredApprovals = int16(want.RequiredApprovals)
	p.AllowSelfApproval = want.AllowSelfApproval
	p.ApprovalTtlSeconds = int32(want.ApprovalTtlSeconds)
	p.StepUpMaxAgeSeconds = int32(want.StepUpMaxAgeSeconds)
	p.Revision = revision
	p.UpdatedBy = actor
	p.UpdatedAt = reviewNow().UTC()
	if p.ID == 0 {
		return tx.Create(p).Error
	}
	return tx.Model(p).Select("Required", "RequiredApprovals", "AllowSelfApproval", "ApprovalTtlSeconds", "StepUpMaxAgeSeconds", "Revision", "UpdatedBy", "UpdatedAt").Updates(p).Error
}

func decidePolicyChange(tx *gorm.DB, c *AutomationReviewPolicyChange, state, actor string, now time.Time) error {
	res := tx.Model(c).Where("id = ? AND state = ? AND revision = ?", c.ID, "PENDING", c.Revision).
		Updates(map[string]any{"state": state, "decided_by": actor, "decided_at": now, "revision": c.Revision + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return reviewRefusal("POLICY_CHANGE_NOT_PENDING", "the policy change is no longer pending")
	}
	return nil
}

func applyMaturedPolicyChange(tx *gorm.DB, id uint, now time.Time) error {
	p, c, err := readReviewPolicy(tx, id)
	if err != nil {
		return err
	}
	if c == nil || !c.EffectiveAt.Valid || now.Before(c.EffectiveAt.Time) {
		return nil
	}
	if p == nil || p.Revision != c.BaseRevision {
		return decidePolicyChange(tx, c, "SUPERSEDED", "delay", now)
	}
	var want ReviewPolicySettings
	if err := json.Unmarshal(c.Proposed, &want); err != nil {
		return err
	}
	if err := writeReviewPolicy(tx, id, p, want, c.ProposedBy); err != nil {
		return err
	}
	return decidePolicyChange(tx, c, "APPLIED", "delay", now)
}

func reviewPolicyView(tx *gorm.DB, id uint, now time.Time) (*AutomationReviewPolicyView, error) {
	p, c, err := readReviewPolicy(tx, id)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewPolicyView{Stored: p, Pending: c, Effective: effectivePolicy(p, c, now)}, nil
}

func (api *Api) AutomationReviewPolicyFor(ctx context.Context, token string) (*AutomationReviewPolicyView, error) {
	p, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return reviewPolicyView(api.RDB.DB(ctx), p.ID, reviewNow())
}

func policyLoosens(current, want ReviewPolicySettings) bool {
	return current.Required && (!want.Required || want.RequiredApprovals < current.RequiredApprovals || !current.AllowSelfApproval && want.AllowSelfApproval || want.ApprovalTtlSeconds > current.ApprovalTtlSeconds || want.StepUpMaxAgeSeconds > current.StepUpMaxAgeSeconds)
}

func checkProtectionEligible(tx *gorm.DB, p *DeviceProfile) error {
	if !p.ActiveVersion.Valid {
		return reviewRefusal("PROFILE_NOT_PUBLISHED", "publish the profile before enabling protection")
	}
	var v DeviceProfileVersion
	if err := tx.Where("device_profile_id = ? AND version = ?", p.ID, p.ActiveVersion.Int32).First(&v).Error; err != nil {
		return err
	}
	snap, err := parseProfileSnapshot(v.Snapshot)
	if err != nil {
		return err
	}
	return refuseProtectionIneligible(snap)
}

func refuseProtectionIneligible(snap *ProfileSnapshot) error {
	if reasons := protectionIneligibility(snap); len(reasons) > 0 {
		return reviewRefusal("PROTECTION_INELIGIBLE", strings.Join(reasons, "; "))
	}
	return nil
}

func (api *Api) SetAutomationReviewPolicy(ctx context.Context, token string, want ReviewPolicySettings) (*AutomationReviewPolicyView, bool, error) {
	person, err := policyPerson(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = validateReviewPolicy(want); err != nil {
		return nil, false, err
	}
	profile, err := api.deviceProfileByToken(ctx, token)
	if err != nil {
		return nil, false, err
	}
	applied := false
	var view *AutomationReviewPolicyView
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := lockProfile(tx, profile.ID)
		if err != nil {
			return err
		}
		now := reviewNow().UTC()
		if err = applyMaturedPolicyChange(tx, p.ID, now); err != nil {
			return err
		}
		stored, pending, err := readReviewPolicy(tx, p.ID)
		if err != nil {
			return err
		}
		current := effectivePolicy(stored, pending, now)
		loosening := policyLoosens(current, want)
		if !loosening && want.Required {
			if err = checkProtectionEligible(tx, p); err != nil {
				return err
			}
		}
		if pending != nil {
			if err = decidePolicyChange(tx, pending, "SUPERSEDED", person.Principal, now); err != nil {
				return err
			}
		}
		if !loosening {
			if err = writeReviewPolicy(tx, p.ID, stored, want, person.Principal); err != nil {
				return err
			}
			applied = true
		} else {
			token, err := actiongate.NewToken()
			if err != nil {
				return err
			}
			raw, err := json.Marshal(want)
			if err != nil {
				return err
			}
			c := &AutomationReviewPolicyChange{DeviceProfileId: p.ID, Token: "arp_" + strings.TrimPrefix(token, actiongate.TokenPrefix), BaseRevision: stored.Revision, Proposed: datatypes.JSON(raw), ProposedBy: person.Principal, State: "PENDING"}
			if current.AllowSelfApproval {
				c.EffectiveAt = sql.NullTime{Time: now.Add(ReviewPolicyLooseningDelay), Valid: true}
			}
			if err = tx.Create(c).Error; err != nil {
				return err
			}
		}
		view, err = reviewPolicyView(tx, p.ID, now)
		return err
	})
	return view, applied, err
}

func (api *Api) decideAutomationReviewPolicyChange(ctx context.Context, token string, approve bool) (*AutomationReviewPolicyView, error) {
	person, err := policyPerson(ctx)
	if err != nil {
		return nil, err
	}
	var change AutomationReviewPolicyChange
	if err = api.RDB.DB(ctx).Where("token = ?", token).First(&change).Error; err != nil {
		return nil, err
	}
	var view *AutomationReviewPolicyView
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockProfile(tx, change.DeviceProfileId); err != nil {
			return err
		}
		now := reviewNow().UTC()
		if err := applyMaturedPolicyChange(tx, change.DeviceProfileId, now); err != nil {
			return err
		}
		if err := tx.Where("id = ?", change.ID).First(&change).Error; err != nil {
			return err
		}
		if change.State != "PENDING" {
			return reviewRefusal("POLICY_CHANGE_NOT_PENDING", "the policy change is no longer pending")
		}
		p, c, err := readReviewPolicy(tx, change.DeviceProfileId)
		if err != nil {
			return err
		}
		state := "CANCELLED"
		if approve {
			settings := effectivePolicy(p, c, now)
			if err = actiongate.CheckStepUp(person, auth.StepUp{MaxAge: time.Duration(settings.StepUpMaxAgeSeconds) * time.Second}, now); err != nil {
				return err
			}
			if person.Principal == change.ProposedBy {
				return reviewRefusal(actiongate.CodeSelfApproval, "the proposer cannot approve their own policy change")
			}
			if p == nil || p.Revision != change.BaseRevision {
				return reviewRefusal("POLICY_CHANGED", "the protection policy changed after this proposal")
			}
			var want ReviewPolicySettings
			if err = json.Unmarshal(change.Proposed, &want); err != nil {
				return err
			}
			if err = writeReviewPolicy(tx, change.DeviceProfileId, p, want, person.Principal); err != nil {
				return err
			}
			state = "APPLIED"
		}
		if err = decidePolicyChange(tx, &change, state, person.Principal, now); err != nil {
			return err
		}
		view, err = reviewPolicyView(tx, change.DeviceProfileId, now)
		return err
	})
	return view, err
}
func (api *Api) ApproveAutomationReviewPolicyChange(ctx context.Context, token string) (*AutomationReviewPolicyView, error) {
	return api.decideAutomationReviewPolicyChange(ctx, token, true)
}
func (api *Api) CancelAutomationReviewPolicyChange(ctx context.Context, token string) (*AutomationReviewPolicyView, error) {
	return api.decideAutomationReviewPolicyChange(ctx, token, false)
}

// approvalPolicy materializes delayed changes under the same profile lock as policy writers.
func (api *Api) approvalPolicy(ctx context.Context, id uint) (*AutomationReviewPolicyView, error) {
	var view *AutomationReviewPolicyView
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockProfile(tx, id); err != nil {
			return err
		}
		if err := applyMaturedPolicyChange(tx, id, reviewNow()); err != nil {
			return err
		}
		var err error
		view, err = reviewPolicyView(tx, id, reviewNow())
		return err
	})
	return view, err
}
