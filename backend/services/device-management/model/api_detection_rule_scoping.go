// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

// This file is the ADR-062 S4 rule-side glue between a DetectionRule's optional group scope
// and the S2 membership read-model. A rule pins {EntityGroupToken}@{EntityGroupVersion}; the
// resolver stamps an event's memberships from the read-model, and the engine fires the rule
// only for events whose ScopeMemberships include the pinned group@v. For that stamp to exist,
// the pinned group@v must be ENROLLED for membership maintenance (registered) whenever an
// ENABLED rule references it, and torn down (deregistered) once the last enabled rule stops.
// reconcileRuleScopingOnTx maintains that invariant transactionally alongside the rule write.

// ruleScope is a rule's optional pin to one published dynamic group@version. A nil *ruleScope
// means "unscoped" (a profile-wide rule).
type ruleScope struct {
	Token   string
	Version int32
}

// ruleScopeOf extracts the (token, version) pair a rule's columns pin, or nil when unscoped.
// The two columns are always both-set or both-nil (validateDetectionRuleScope enforces it),
// so a half-set pair is treated as unscoped defensively rather than panicking.
func ruleScopeOf(token *string, version *int32) *ruleScope {
	if token == nil || *token == "" || version == nil {
		return nil
	}
	return &ruleScope{Token: *token, Version: *version}
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
	if _, _, _, err := api.loadGroupVersionForScoping(ctx, *request.EntityGroupToken, *request.EntityGroupVersion); err != nil {
		return fmt.Errorf("invalid detection rule scope: %w", err)
	}
	return nil
}

// membershipEviction is one family's set of entity ids whose membership cache must be evicted
// post-commit after a reconcile touched the read-model.
type membershipEviction struct {
	Type string
	Ids  []uint
}

// reconcileRuleScopingOnTx brings the membership read-model into line with a rule's scope
// after the rule row has been written on tx (ADR-062 S4). For each group@v the change could
// affect — the old scope, the new scope, or both — it recomputes how many ENABLED rules now
// reference that exact group@v and either enrolls it (>0, if not already materialized) or GCs
// it (0). Running on the SAME tx as the rule write makes arming atomic: a rule and the
// read-model it depends on commit together, so a rule can never go live against a read-model
// that a failed enrollment left half-built. It returns the per-family evictions the caller
// fires post-commit and whether any (de)registration ran (so the caller can evict the
// AnyScopedGroups gate, whose truth may have flipped).
func (api *Api) reconcileRuleScopingOnTx(ctx context.Context, tx *gorm.DB, old, new *ruleScope) ([]membershipEviction, bool, error) {
	// The affected group@versions, de-duplicated: an unchanged scope (old == new) is one
	// entry re-evaluated once; a scope change is both the vacated and the adopted group@v.
	affected := make([]ruleScope, 0, 2)
	add := func(s *ruleScope) {
		if s == nil {
			return
		}
		for _, e := range affected {
			if e == *s {
				return
			}
		}
		affected = append(affected, *s)
	}
	add(old)
	add(new)

	// Acquire the per-family membership locks UP FRONT in a canonical (sorted) order. A
	// re-scope that moves a rule across families (e.g. a device group to an area group)
	// touches two families; without a fixed acquisition order two such reconciles could lock
	// them in opposite orders and deadlock. The locks are re-entrant within the txn, so the
	// per-branch lockMembershipFamily calls below (which also serve the standalone
	// Register/Deregister paths) are no-ops here. Same-family or single-family reconciles —
	// the overwhelming common case — acquire exactly one lock.
	if err := api.lockAffectedFamiliesOnTx(ctx, tx, affected); err != nil {
		return nil, false, err
	}

	var evictions []membershipEviction
	changed := false
	for _, s := range affected {
		n, err := api.enabledRulesReferencingScopeOnTx(tx, s.Token, s.Version)
		if err != nil {
			return nil, false, err
		}
		if n > 0 {
			// An enabled rule references this group@v — ensure the read-model is enrolled.
			// The frozen version is immutable, so an already-materialized group@v is already
			// correct: skip the (atomic but wasteful) rebuild when its rows already exist.
			group, frozen, keys, err := api.loadGroupVersionForScoping(ctx, s.Token, s.Version)
			if err != nil {
				return nil, false, err
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
			// No enabled rule references this group@v any longer — GC its read-model. Safe
			// (and a no-op) if it was never registered. The group itself still exists (a
			// referencing rule row kept it alive), so its id resolves.
			group, err := api.entityGroupByToken(ctx, s.Token)
			if err != nil {
				// The group vanished (its delete-guard let it go because no rule referenced
				// it): nothing to tear down for a group that is already gone.
				if err == gorm.ErrRecordNotFound {
					continue
				}
				return nil, false, err
			}
			etype, ids, err := api.deregisterGroupVersionOnTx(ctx, tx, group.ID, s.Version)
			if err != nil {
				return nil, false, err
			}
			if len(ids) > 0 {
				evictions = append(evictions, membershipEviction{Type: etype, Ids: ids})
			}
			changed = true
		}
	}
	return evictions, changed, nil
}

// lockAffectedFamiliesOnTx takes the per-family advisory locks for every family the affected
// scopes belong to, in sorted member-type order, so concurrent multi-family reconciles cannot
// deadlock. A scope whose group has vanished contributes no family (nothing to lock). Postgres
// advisory locks are re-entrant within a transaction, so re-locking a family later is free.
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

// fireScopingEvictions applies the post-commit cache evictions a reconcile produced: each
// family's membership rows, plus the AnyScopedGroups gate when a (de)registration ran (its
// truth — does this tenant use rule scoping at all — may have flipped).
func (api *Api) fireScopingEvictions(ctx context.Context, evictions []membershipEviction, changed bool) {
	for _, ev := range evictions {
		api.evictMemberships(ctx, ev.Type, ev.Ids)
	}
	if changed {
		api.evictScopedGroupsExist(ctx)
	}
}

// enabledRulesReferencingScopeOnTx counts the ENABLED detection rules that pin exactly this
// group@v (tenant-scoped by the rdb callback). It is the reference count that decides whether
// the read-model for group@v must stay enrolled: a disabled rule does not need a live
// read-model (it is inert until re-enabled, which re-runs reconcile).
func (api *Api) enabledRulesReferencingScopeOnTx(tx *gorm.DB, groupToken string, version int32) (int64, error) {
	var n int64
	err := tx.Model(&DetectionRule{}).
		Where("enabled = ? AND entity_group_token = ? AND entity_group_version = ?", true, groupToken, version).
		Count(&n).Error
	return n, err
}

// scopeMaterializedOnTx reports whether a group@v already has read-model rows — either
// reverse-index (facet-ref) or membership rows. It lets reconcile skip a redundant rebuild of
// an immutable frozen version that is already enrolled. Checking BOTH tables covers the two
// edge shapes: a selector referencing no facets (no facet-refs but real members) and a
// selector currently matching no entities (facet-refs but no members).
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
