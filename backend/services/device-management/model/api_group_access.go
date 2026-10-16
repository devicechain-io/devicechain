// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// GROUP ACCESS SCOPES (ADR-081): a published dynamic group of devices that a member's access
// can be limited to, as the anchor "group:<token>".
//
// # Why the group is marked, rather than enrolled because a member names it
//
// A member's scope lives in user-management; group materialization lives here. Enrolling a
// group because some member's scope names it would need user-management to tell this service
// which groups its memberships name, and keep telling it — a reference count kept across two
// services by messages, where a lost one is either a member who silently sees nothing or a
// read-model torn down under a member who still needs it. So the distinction is made where the
// group lives instead: a group is an ACCESS SCOPE (EntityGroup.AccessScopeAt), set by whoever
// may write devices, and that is the second reason — beside a live detection rule pinning it —
// to materialize it. A member may be scoped to any group token (a scope names a customer
// token unchecked in the same way); a group that is not an access scope matches nothing.
//
// # What follows the group
//
// The group is materialized at its PUBLISHED version (ActiveVersion), not a pinned one: a
// rule must not drift under a later edit, but a region's members are meant to follow the
// group as it is published. Publishing or rolling back an access scope therefore re-enrolls
// it (the old version is torn down unless a rule pins it). A device in the group at that
// version:
//
//   - is visible, while it is in the group, to a member whose scope names the group
//     (Device's scope rule, rdb.GroupMembers);
//   - carries the anchor "group:<token>" on the events, alarms, commands and update
//     assignments written about it while it is in the group, and on its live state
//     (accessGroupAnchors, every place a device's anchors are read);
//   - is announced with its new anchors (the ownership-changed fact) whenever it enters or
//     leaves the group, so device-state moves its live view without waiting for an event.
//
// # The cost, and its bound
//
// Each access group a device is in adds one anchor to every event it sends — one more
// event_anchors row, one more element on the resolved wire and in device_states — so the
// number of access scopes per tenant is capped at MaxAccessScopeGroups, which bounds the
// per-event growth. A tenant with none pays nothing: no anchor is added and no read is made
// beyond what rule scoping already makes.

// MaxAccessScopeGroups is the most groups one tenant may have marked as access scopes. It
// bounds how many group anchors a single event can carry (one per access group its device is
// in). A further one is refused with LIMIT_EXCEEDED, never accepted and silently ignored.
const MaxAccessScopeGroups = 32

// ErrAccessScopeIneligible refuses making a group an access scope when it cannot be one: it
// collects something other than devices, its members are a static list, or it has never been
// published. The message says which.
var ErrAccessScopeIneligible = integrity.NewRefusal(integrity.ClassInvalid,
	"only a published dynamic group of devices can be an access scope")

// SetEntityGroupAccessScope makes a group an access scope (enabled) or stops it being one, and
// returns the group as it now is. Setting what is already set changes nothing. Enabling needs
// a published dynamic group of devices and a free place under MaxAccessScopeGroups.
//
// The flag and the read-model it implies change in ONE transaction, under the family lock the
// rest of membership maintenance takes, so a member's visibility never reads a flag whose
// members are not yet materialized. After it commits, the members' cached memberships are
// dropped (their access marker changed) and each member device is announced with its anchors.
func (api *Api) SetEntityGroupAccessScope(ctx context.Context, token string, enabled bool) (*EntityGroup, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if enabled {
		if err := accessScopeEligible(group); err != nil {
			return nil, err
		}
	}
	var evictions []membershipEviction
	var changed bool
	var members []uint
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := api.lockMembershipFamily(ctx, tx, group.MemberType); err != nil {
			return err
		}
		var current EntityGroup
		if err := tx.Where("id = ?", group.ID).First(&current).Error; err != nil {
			return err
		}
		if current.IsAccessScope() == enabled {
			return nil
		}
		if enabled {
			var n int64
			if err := tx.Model(&EntityGroup{}).Where("access_scope_at IS NOT NULL").Count(&n).Error; err != nil {
				return err
			}
			if n >= MaxAccessScopeGroups {
				return limit.Exceeded("access-scope groups", int(n)+1, MaxAccessScopeGroups)
			}
		} else {
			// The members losing the anchor, read before the teardown below removes them.
			if members, err = api.membersAtOnTx(tx, current.ID, current.ActiveVersion); err != nil {
				return err
			}
		}
		value := sql.NullTime{}
		if enabled {
			value = sql.NullTime{Time: time.Now().UTC(), Valid: true}
		}
		res := tx.Model(&current).Where("id = ?", current.ID).Update("access_scope_at", value)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: entity group %q", gorm.ErrRecordNotFound, token)
		}
		evictions, changed, err = api.reconcileEnrollmentOnTx(ctx, tx,
			[]ruleScope{{Token: current.Token, Version: current.ActiveVersion.Int32}})
		if err != nil {
			return err
		}
		if enabled {
			members, err = api.membersAtOnTx(tx, current.ID, current.ActiveVersion)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	api.fireScopingEvictions(ctx, evictions, changed)
	api.evictMemberships(ctx, string(entity.TypeDevice), members)
	api.emitOwnershipChanged(ctx, members)
	return api.entityGroupByToken(ctx, token)
}

// accessScopeEligible says why a group cannot be an access scope, or nil when it can.
func accessScopeEligible(g *EntityGroup) error {
	switch {
	case g.MemberType != string(entity.TypeDevice):
		return fmt.Errorf("%w: group %q collects %ss, not devices", ErrAccessScopeIneligible, g.Token, g.MemberType)
	case MembershipMode(g.MembershipMode) != MembershipDynamic:
		return fmt.Errorf("%w: group %q has a static member list", ErrAccessScopeIneligible, g.Token)
	case !g.ActiveVersion.Valid:
		return fmt.Errorf("%w: group %q has never been published", ErrAccessScopeIneligible, g.Token)
	}
	return nil
}

// moveAccessScopeOnTx re-enrolls an access scope after its published pointer moved from
// `from` (a publish or a rollback, on the caller's transaction, after the pointer update). The
// new version is materialized and the old one torn down unless a live rule still pins it,
// atomically with the pointer, so a member never reads a pointer whose members are not there.
// It returns the evictions to fire and the member devices to announce (members of either
// version: a device in only one of them has changed anchors). A group that is not an access
// scope is untouched.
func (api *Api) moveAccessScopeOnTx(ctx context.Context, tx *gorm.DB, groupId uint, from sql.NullInt32) ([]membershipEviction, bool, []uint, error) {
	var g EntityGroup
	if err := tx.Where("id = ?", groupId).First(&g).Error; err != nil {
		return nil, false, nil, err
	}
	if !g.IsAccessScope() || !g.ActiveVersion.Valid {
		return nil, false, nil, nil
	}
	if from.Valid && from.Int32 == g.ActiveVersion.Int32 {
		return nil, false, nil, nil
	}
	if err := api.lockMembershipFamily(ctx, tx, g.MemberType); err != nil {
		return nil, false, nil, err
	}
	before, err := api.membersAtOnTx(tx, g.ID, from)
	if err != nil {
		return nil, false, nil, err
	}
	affected := []ruleScope{{Token: g.Token, Version: g.ActiveVersion.Int32}}
	if from.Valid {
		affected = append(affected, ruleScope{Token: g.Token, Version: from.Int32})
	}
	evictions, changed, err := api.reconcileEnrollmentOnTx(ctx, tx, affected)
	if err != nil {
		return nil, false, nil, err
	}
	after, err := api.membersAtOnTx(tx, g.ID, g.ActiveVersion)
	if err != nil {
		return nil, false, nil, err
	}
	members := before
	for _, id := range after {
		if !slices.Contains(members, id) {
			members = append(members, id)
		}
	}
	return evictions, changed, members, nil
}

// accessReferenceOnTx reports whether groupToken@version is an access scope's published
// version — the access half of the enrollment refcount (scopeRefcountOnTx).
func accessReferenceOnTx(tx *gorm.DB, groupToken string, version int32) (bool, error) {
	var n int64
	err := tx.Model(&EntityGroup{}).
		Where("token = ? AND access_scope_at IS NOT NULL AND active_version = ?", groupToken, version).
		Count(&n).Error
	return n > 0, err
}

// membersAtOnTx returns the member devices of groupId at version, from the read-model. An
// unset version has none.
func (api *Api) membersAtOnTx(tx *gorm.DB, groupId uint, version sql.NullInt32) ([]uint, error) {
	if !version.Valid {
		return nil, nil
	}
	ids := make([]uint, 0)
	err := tx.Model(&EntityGroupMembership{}).
		Where("group_id = ? AND selector_version = ? AND entity_type = ?", groupId, version.Int32, string(entity.TypeDevice)).
		Order("entity_id").Pluck("entity_id", &ids).Error
	return ids, err
}

// accessMembersOnTx returns the member devices of groupId when it is an access scope (at its
// published version), and none otherwise.
func (api *Api) accessMembersOnTx(tx *gorm.DB, groupId uint) ([]uint, error) {
	var g EntityGroup
	if err := tx.Where("id = ?", groupId).First(&g).Error; err != nil {
		return nil, err
	}
	if !g.IsAccessScope() {
		return nil, nil
	}
	return api.membersAtOnTx(tx, g.ID, g.ActiveVersion)
}

// accessGroup is an access scope as its members' anchors need it.
type accessGroup struct {
	Token   string
	Version int32
}

// accessGroupsOnDB reads the tenant's access scopes, by group id. There are at most
// MaxAccessScopeGroups.
func accessGroupsOnDB(db *gorm.DB) (map[uint]accessGroup, error) {
	groups := make([]EntityGroup, 0)
	if err := db.Model(&EntityGroup{}).Select("id", "token", "active_version").
		Where("access_scope_at IS NOT NULL AND active_version IS NOT NULL").
		Find(&groups).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]accessGroup, len(groups))
	for _, g := range groups {
		out[g.ID] = accessGroup{Token: g.Token, Version: g.ActiveVersion.Int32}
	}
	return out, nil
}

// groupAnchor renders an access scope as the anchor a member's scope names.
func groupAnchor(token string) string {
	return auth.AnchorKindGroup + ":" + token
}

// accessGroupAnchors returns, for each given device in at least one access scope, the
// "group:<token>" anchors of those scopes. A device in none is absent. At most
// rdb.MaxLookupKeys devices per call.
func accessGroupAnchors(db *gorm.DB, deviceIds []uint) (map[uint][]string, error) {
	out := make(map[uint][]string)
	if len(deviceIds) == 0 {
		return out, nil
	}
	groups, err := accessGroupsOnDB(db)
	if err != nil || len(groups) == 0 {
		return out, err
	}
	gids := make([]uint, 0, len(groups))
	for id := range groups {
		gids = append(gids, id)
	}
	rows := make([]EntityGroupMembership, 0)
	if err := rdb.FindByKeys(db.Where("entity_type = ?", string(entity.TypeDevice)).
		Where("group_id IN ?", gids), &rows, "entity_id", deviceIds); err != nil {
		return nil, err
	}
	for _, r := range rows {
		g, ok := groups[r.GroupId]
		if !ok || g.Version != r.SelectorVersion {
			continue
		}
		if a := groupAnchor(g.Token); !slices.Contains(out[r.EntityId], a) {
			out[r.EntityId] = append(out[r.EntityId], a)
		}
	}
	return out, nil
}

// withGroupAnchors adds group anchors to a device's anchor set, sorted and de-duplicated.
func withGroupAnchors(anchors rdb.Anchors, groups []string) rdb.Anchors {
	for _, a := range groups {
		if !slices.Contains(anchors, a) {
			anchors = append(anchors, a)
		}
	}
	slices.Sort(anchors)
	return anchors
}
