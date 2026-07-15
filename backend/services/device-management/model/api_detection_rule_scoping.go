// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

// This file is the ADR-062 S4 glue between a DetectionRule's optional group scope and the
// S2 membership read-model. A rule pins {EntityGroupToken}@{EntityGroupVersion}; the resolver
// stamps an event's memberships from the read-model, and the engine fires the rule only for
// events whose ScopeMemberships include the pinned group@v. For that stamp to exist, the
// pinned group@v must be ENROLLED for membership maintenance (registered) whenever a LIVE
// rule references it, and torn down (deregistered) once the last live rule stops.
//
// Enrollment follows PUBLISHED state, not the mutable draft. The live DETECT engine runs a
// profile's ACTIVE published version, so a rule "goes live" at publish, not at draft-save;
// keying enrollment on drafts would tear the read-model out from under a still-live published
// rule the moment its draft was disabled/re-scoped (and would strand a rolled-back version's
// rule with no enrollment). So the reference set is the DetectionRuleScopeRef table — one row
// per scoped enabled rule in each profile's active version — re-synced whenever the active
// version changes (publish / rollback) and on profile delete. Draft rule save only validates
// and stores the scope columns; it does NOT touch enrollment.

// ruleScope is a rule's pin to one published dynamic group@version. A nil *ruleScope means
// "unscoped" (a profile-wide rule).
type ruleScope struct {
	Token   string
	Version int32
}

// scopeRefRow is one live scoped rule: its authoring token plus the group@v it pins. It is
// the projection of an enabled scoped rule in a profile's active-version snapshot into a
// DetectionRuleScopeRef row.
type scopeRefRow struct {
	RuleToken    string
	GroupToken   string
	GroupVersion int32
}

// normalizedRuleScope maps a request's raw scope fields to the stored form: a nil-or-empty
// token collapses BOTH columns to nil (unscoped). Validation has already rejected a half-set
// pair, so this only strips an empty-string token to a clean NULL/NULL.
func normalizedRuleScope(token *string, version *int32) (*string, *int32) {
	if token == nil || *token == "" {
		return nil, nil
	}
	return token, version
}

// validateDetectionRuleScope fails closed on an invalid rule scope: the two columns must be
// set together (a token requires a version and vice-versa), and when set they must resolve to
// a PUBLISHED DYNAMIC entity-group version — loadGroupVersionForScoping rejects a static group,
// a missing group, or a missing version (and re-compiles the frozen selector, so a scope
// pinning an un-lowerable version is rejected too). An unscoped rule (both absent) is valid.
func (api *Api) validateDetectionRuleScope(ctx context.Context, request *DetectionRuleCreateRequest) error {
	hasToken := request.EntityGroupToken != nil && *request.EntityGroupToken != ""
	hasVersion := request.EntityGroupVersion != nil
	if !hasToken && !hasVersion {
		return nil
	}
	if hasToken != hasVersion {
		return fmt.Errorf("a detection rule scope requires both an entity group token and a version")
	}
	if _, _, _, err := api.loadGroupVersionForScoping(ctx, api.RDB.DB(ctx), *request.EntityGroupToken, *request.EntityGroupVersion); err != nil {
		return fmt.Errorf("invalid detection rule scope: %w", err)
	}
	return nil
}

// membershipEviction is one family's set of entity ids whose membership cache must be evicted
// post-commit after enrollment changed.
type membershipEviction struct {
	Type string
	Ids  []uint
}

// scopedRulesInSnapshot projects a profile snapshot's ENABLED scoped rules to scopeRefRows —
// exactly the set of live rules that reference a group@v under that version. Disabled rules
// are inert (never fed to the engine) and un-scoped rules reference nothing, so both are
// omitted. It is the desired DetectionRuleScopeRef set for a profile whose active version is
// this snapshot.
func scopedRulesInSnapshot(snap *ProfileSnapshot) []scopeRefRow {
	if snap == nil {
		return nil
	}
	out := make([]scopeRefRow, 0, len(snap.Rules))
	for _, r := range snap.Rules {
		if r == nil || !r.Enabled || !r.GroupScoped() {
			continue
		}
		out = append(out, scopeRefRow{
			RuleToken:    r.Token,
			GroupToken:   *r.EntityGroupToken,
			GroupVersion: *r.EntityGroupVersion,
		})
	}
	return out
}

// syncProfileScopeRefsAndEnroll re-syncs a profile's DetectionRuleScopeRef rows to `desired`
// (its active version's scoped enabled rules) and reconciles the S2 read-model enrollment for
// every group@v the change touched, all on the caller's tx (so it is atomic with the
// active-version change that triggered it — publish / rollback / profile delete). It returns
// the per-family cache evictions to fire post-commit and whether enrollment changed (so the
// caller can evict the AnyScopedGroups gate). The whole re-sync is delete-all-then-insert for
// this profile: a profile has one active version, so its live scoped set is fully replaced.
func (api *Api) syncProfileScopeRefsAndEnroll(ctx context.Context, tx *gorm.DB, profileId uint, desired []scopeRefRow) ([]membershipEviction, bool, error) {
	// The group@versions this profile referenced BEFORE the change (to detect ones it is
	// dropping) and AFTER (ones it is adding) — their union is what enrollment must re-check.
	var existing []DetectionRuleScopeRef
	if err := tx.Where("device_profile_id = ?", profileId).Find(&existing).Error; err != nil {
		return nil, false, err
	}
	affected := make([]ruleScope, 0, len(existing)+len(desired))
	seen := make(map[ruleScope]bool)
	addAffected := func(s ruleScope) {
		if !seen[s] {
			seen[s] = true
			affected = append(affected, s)
		}
	}
	for _, e := range existing {
		addAffected(ruleScope{Token: e.GroupToken, Version: e.GroupVersion})
	}
	// Replace the profile's rows: hard-delete the old set (derived rows; a soft-delete
	// tombstone would collide with the re-insert on the unique index), then insert the new.
	if err := tx.Unscoped().Where("device_profile_id = ?", profileId).Delete(&DetectionRuleScopeRef{}).Error; err != nil {
		return nil, false, err
	}
	for _, d := range desired {
		row := &DetectionRuleScopeRef{
			DeviceProfileId: profileId,
			RuleToken:       d.RuleToken,
			GroupToken:      d.GroupToken,
			GroupVersion:    d.GroupVersion,
		}
		if err := tx.Create(row).Error; err != nil {
			return nil, false, err
		}
		addAffected(ruleScope{Token: d.GroupToken, Version: d.GroupVersion})
	}
	return api.reconcileEnrollmentOnTx(ctx, tx, affected)
}

// reconcileEnrollmentOnTx brings the read-model enrollment into line with the current
// DetectionRuleScopeRef refcount for each affected group@v: > 0 live references ⇒ materialize
// (register, skipping an already-materialized immutable version); 0 ⇒ GC (deregister). The
// refcount is global across profiles, so a group@v two profiles reference stays enrolled until
// the last un-references it. Family locks are taken up front in canonical order (deadlock-free
// against concurrent reconciles and attribute-write recomputes).
func (api *Api) reconcileEnrollmentOnTx(ctx context.Context, tx *gorm.DB, affected []ruleScope) ([]membershipEviction, bool, error) {
	if len(affected) == 0 {
		return nil, false, nil
	}
	if err := api.lockAffectedFamiliesOnTx(ctx, tx, affected); err != nil {
		return nil, false, err
	}
	var evictions []membershipEviction
	changed := false
	for _, s := range affected {
		n, err := api.scopeRefcountOnTx(tx, s.Token, s.Version)
		if err != nil {
			return nil, false, err
		}
		if n > 0 {
			group, frozen, keys, err := api.loadGroupVersionForScoping(ctx, tx, s.Token, s.Version)
			if err != nil {
				// A live scoped rule references this group@v but the group/version is gone.
				// Fail closed (the caller's publish/rollback rolls back atomically), but wrap
				// the bare not-found so the operator can tell WHY the version won't activate —
				// this is the retained-version-whose-group-was-deleted corner (the delete-guard
				// intentionally does not scan retained non-active snapshots, so a deleted group
				// can strand a rollback target; the failure surfaces here rather than silently).
				return nil, false, fmt.Errorf("cannot activate profile version: scoped entity group %q@%d no longer exists: %w", s.Token, s.Version, err)
			}
			materialized, err := api.scopeMaterializedOnTx(tx, group.ID, s.Version)
			if err != nil {
				return nil, false, err
			}
			if materialized {
				continue
			}
			ids, err := api.registerGroupVersionOnTx(ctx, tx, group, frozen, keys)
			if err != nil {
				return nil, false, err
			}
			evictions = append(evictions, membershipEviction{Type: frozen.MemberType, Ids: ids})
			changed = true
		} else {
			// Resolve the group id on the tx (same connection + lock; no second pool conn).
			var group EntityGroup
			if err := tx.Where("token = ?", s.Token).First(&group).Error; err != nil {
				// The group is already gone (its delete-guard let it go because no live rule
				// referenced it): nothing to tear down.
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return nil, false, err
			}
			etype, ids, err := api.deregisterGroupVersionOnTx(ctx, tx, group.ID, s.Version)
			if err != nil {
				return nil, false, err
			}
			// Only mark changed when rows were actually torn down: a no-op deregister of a
			// never-enrolled scope must not spuriously evict the AnyScopedGroups gate.
			if len(ids) > 0 {
				evictions = append(evictions, membershipEviction{Type: etype, Ids: ids})
				changed = true
			}
		}
	}
	return evictions, changed, nil
}

// lockAffectedFamiliesOnTx takes the per-family advisory locks for every family the affected
// scopes belong to, in sorted member-type order, so concurrent multi-family reconciles cannot
// deadlock. A scope whose group has vanished contributes no family. Postgres advisory locks are
// re-entrant within a transaction, so re-locking a family later (in register/deregister) is free.
func (api *Api) lockAffectedFamiliesOnTx(ctx context.Context, tx *gorm.DB, affected []ruleScope) error {
	seen := make(map[string]bool)
	families := make([]string, 0, len(affected))
	for _, s := range affected {
		var mt string
		if err := tx.Model(&EntityGroup{}).Where("token = ?", s.Token).
			Select("member_type").Scan(&mt).Error; err != nil {
			return err
		}
		if mt == "" || seen[mt] {
			continue
		}
		seen[mt] = true
		families = append(families, mt)
	}
	sort.Strings(families)
	for _, mt := range families {
		if err := api.lockMembershipFamily(ctx, tx, mt); err != nil {
			return err
		}
	}
	return nil
}

// fireScopingEvictions applies the post-commit cache evictions an enrollment reconcile
// produced: each family's membership rows, plus the AnyScopedGroups gate when enrollment
// changed (its truth — does this tenant use rule scoping at all — may have flipped).
func (api *Api) fireScopingEvictions(ctx context.Context, evictions []membershipEviction, changed bool) {
	for _, ev := range evictions {
		api.evictMemberships(ctx, ev.Type, ev.Ids)
	}
	if changed {
		api.evictScopedGroupsExist(ctx)
	}
}

// scopeRefcountOnTx counts the LIVE (published, enabled) rules that pin exactly this group@v,
// across all profiles (tenant-scoped by the rdb callback). It is the enrollment refcount: the
// read-model for group@v stays materialized while this is > 0.
func (api *Api) scopeRefcountOnTx(tx *gorm.DB, groupToken string, version int32) (int64, error) {
	var n int64
	err := tx.Model(&DetectionRuleScopeRef{}).
		Where("group_token = ? AND group_version = ?", groupToken, version).
		Count(&n).Error
	return n, err
}

// scopeMaterializedOnTx reports whether a group@v already has read-model rows — either
// reverse-index (facet-ref) or membership rows. It lets reconcile skip a redundant rebuild of
// an immutable frozen version that is already enrolled (e.g. a second profile publishes a rule
// scoped to a group@v another profile already enrolled). Checking BOTH tables is defensive
// belt-and-suspenders: in practice a compiled selector always references at least one facet
// (the lowering rejects any non-facet leaf), so facet-refs alone would suffice, but the extra
// membership check costs one indexed count and cannot be wrong.
func (api *Api) scopeMaterializedOnTx(tx *gorm.DB, groupId uint, version int32) (bool, error) {
	var refs int64
	if err := tx.Model(&EntityGroupFacetRef{}).
		Where("group_id = ? AND selector_version = ?", groupId, version).
		Limit(1).Count(&refs).Error; err != nil {
		return false, err
	}
	if refs > 0 {
		return true, nil
	}
	var members int64
	if err := tx.Model(&EntityGroupMembership{}).
		Where("group_id = ? AND selector_version = ?", groupId, version).
		Limit(1).Count(&members).Error; err != nil {
		return false, err
	}
	return members > 0, nil
}
