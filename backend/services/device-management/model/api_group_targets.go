// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
)

// GroupTargetRejectionCode classifies a refusal to resolve a group as a command target.
//
// It is a separate vocabulary from EnqueueRejectionCode because it answers a different
// question: these are reasons the TARGET SET could not be established at all, decided
// before any device is looked at, and every one of them refuses the whole batch. An
// enqueue rejection is about one device's command; these are about whether there is a
// fleet to command.
type GroupTargetRejectionCode string

const (
	// RejectGroupNotFound: the token resolved to no live entity group in this tenant.
	RejectGroupNotFound GroupTargetRejectionCode = "GROUP_NOT_FOUND"
	// RejectGroupNotADeviceGroup: the group collects assets, areas or customers.
	//
	// 🔴 THIS IS A SAFETY CHECK, NOT A TYPO CHECK. Groups collect four entity families
	// and tokens are unique per TABLE, so an asset and a device may both be called
	// "pump-1" in one tenant. Without this, an asset group's member tokens would flow
	// into per-device validation as device tokens: mostly refused as DEVICE_NOT_FOUND
	// (a category error reported as a per-device condition), but wherever a token
	// happens to collide with a real device, ACCEPTED — commanding hardware the
	// operator never targeted.
	RejectGroupNotADeviceGroup GroupTargetRejectionCode = "GROUP_NOT_A_DEVICE_GROUP"
	// RejectGroupNotPublished: a dynamic group that has never been published has only a
	// mutable draft selector, so there is no frozen membership rule to fire at.
	RejectGroupNotPublished GroupTargetRejectionCode = "GROUP_NOT_PUBLISHED"
	// RejectGroupVersionNotFound: the requested version does not exist for this group.
	RejectGroupVersionNotFound GroupTargetRejectionCode = "GROUP_VERSION_NOT_FOUND"
	// RejectGroupNotVersioned: a version was named for a STATIC group. Static groups are
	// never versioned (their members are explicit edges, not a rule), so honouring the
	// request is impossible and ignoring it would silently answer a different question
	// than the one asked.
	RejectGroupNotVersioned GroupTargetRejectionCode = "GROUP_NOT_VERSIONED"
)

// DeviceGroupTargetPage is one keyset page of the device tokens a group resolves to,
// plus the cursor to continue from.
type DeviceGroupTargetPage struct {
	// Rejected reports that the group cannot serve as a command target at all. When
	// set, Code and Reason explain it and every other field is meaningless.
	Rejected bool
	Code     GroupTargetRejectionCode
	Reason   string

	// DeviceTokens are this page's members, ascending by the member's row id.
	DeviceTokens []string
	// NextCursor is the id to pass as afterId for the following page; zero means the
	// group is exhausted.
	//
	// 🔑 It is derived from the LAST ROW RETURNED, not from a count, so it stays correct
	// when rows appear or vanish between pages — which is the whole reason this is a
	// keyset walk (see ResolveDeviceGroupTargets).
	NextCursor uint
	// ResolvedVersion is the frozen group version this page was resolved against, or nil
	// for a static group. The caller stamps it on the batch record so the record can
	// still answer "what did this group MEAN when the batch fired?" after the group is
	// edited.
	ResolvedVersion *int32
}

// rejectedTargets builds a refusal page.
func rejectedTargets(code GroupTargetRejectionCode, format string, args ...any) *DeviceGroupTargetPage {
	return &DeviceGroupTargetPage{Rejected: true, Code: code, Reason: fmt.Sprintf(format, args...)}
}

// ResolveDeviceGroupTargets resolves one KEYSET page of the device tokens an entity group
// currently collects — the target-set primitive a fleet-wide command write pages through.
//
// 🔴 IT IS A KEYSET WALK RATHER THAN ResolveGroupMembers' OFFSET PAGING, AND THAT IS A
// CORRECTNESS DIFFERENCE, NOT A PERFORMANCE ONE. rdb.Pagination is offset-based
// (OFFSET/LIMIT), so when any row LEAVES the result set between two pages — a device
// soft-deleted, or an attribute write moving it out of a dynamic selector — every
// subsequent row shifts down one slot and a device that satisfied the selector the entire
// time slides across the page boundary UNVISITED. It lands in no refusal list, is counted
// nowhere, and nothing records that it was skipped: an unauditable hole in a fleet write.
//
// A keyset walk cannot skip a row that stays in the set, because the cursor is the last id
// actually seen rather than a count of rows presumed seen. What it can still miss is a
// device that JOINS the group below the cursor after the walk has passed — which is the
// honest and documentable semantic: a batch targets the group's membership as of the
// moment it was fired.
//
// Version pinning follows the tree's existing doctrine for firing at a group (a DETECT rule
// scopes to {group}@{version} precisely so its target set cannot drift under a later
// selector edit):
//
//   - dynamic group, no version named → the ACTIVE PUBLISHED version's frozen selector;
//   - dynamic group, version named    → that version's frozen selector;
//   - dynamic group, never published  → REFUSED. It has only a mutable draft, so a batch
//     record could never answer what the group meant when it fired — permanently
//     ambiguous, which is the ambiguity the pinning exists to remove. Publish it first.
//   - static group                    → the explicit member edges; a named version is
//     refused rather than ignored.
//
// A rejection is a decided verdict carried in the page; an error means the resolution
// could not be PERFORMED and the caller must fail closed without relaying it. Same split,
// and for the same reason, as the enqueue gate.
func (api *Api) ResolveDeviceGroupTargets(ctx context.Context, groupToken string,
	version *int32, afterId uint, limit int) (*DeviceGroupTargetPage, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("group target page limit must be positive, got %d", limit)
	}

	var group EntityGroup
	err := api.RDB.DB(ctx).Where("token = ?", groupToken).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rejectedTargets(RejectGroupNotFound, "entity group %q does not exist", groupToken), nil
	}
	if err != nil {
		return nil, err
	}

	// The family check comes before everything else: it is the one refusal whose absence
	// is a safety failure rather than a usability one.
	if entity.Type(group.MemberType) != entity.TypeDevice {
		return rejectedTargets(RejectGroupNotADeviceGroup,
			"entity group %q collects %s, not devices, so it cannot be the target of a command",
			groupToken, group.MemberType), nil
	}

	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		if version != nil {
			return rejectedTargets(RejectGroupNotVersioned,
				"entity group %q is a static group and has no versions; omit the version to "+
					"target its current members", groupToken), nil
		}
		return api.staticDeviceTargets(ctx, &group, afterId, limit)
	}
	return api.dynamicDeviceTargets(ctx, &group, version, afterId, limit)
}

// dynamicDeviceTargets resolves a page against a FROZEN published version's selector,
// never the group's live draft.
func (api *Api) dynamicDeviceTargets(ctx context.Context, group *EntityGroup, version *int32,
	afterId uint, limit int) (*DeviceGroupTargetPage, error) {
	wanted := version
	if wanted == nil {
		if !group.ActiveVersion.Valid {
			return rejectedTargets(RejectGroupNotPublished,
				"entity group %q has never been published, so it has no frozen membership rule "+
					"to command; publish it and retry", group.Token), nil
		}
		active := group.ActiveVersion.Int32
		wanted = &active
	}

	var frozen EntityGroupVersion
	err := api.RDB.DB(ctx).Where("entity_group_id = ? AND version = ?", group.ID, *wanted).
		First(&frozen).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return rejectedTargets(RejectGroupVersionNotFound,
			"entity group %q has no version %d", group.Token, *wanted), nil
	}
	if err != nil {
		return nil, err
	}

	// Lower the FROZEN selector text — exactly what the pinned version matches — rather
	// than the group's mutable Selector column.
	frag, args, table, err := api.lowerSelectorSource(ctx, frozen.MemberType, frozen.Selector)
	if err != nil {
		return nil, err
	}
	memberModel, _, _ := memberModelAndTable(frozen.MemberType)

	members := make([]EntityMember, 0, limit)
	q := api.RDB.DB(ctx).Model(memberModel).Where(frag, args...)
	if afterId > 0 {
		q = q.Where(table+".id > ?", afterId)
	}
	if err := q.Select(table+".id", table+".token").Order(table + ".id").
		Limit(limit).Find(&members).Error; err != nil {
		return nil, err
	}
	return targetPage(members, limit, wanted), nil
}

// staticDeviceTargets pages a static group's explicit "member" edges by keyset on the
// member's own row id, so the cursor means the same thing on both paths.
func (api *Api) staticDeviceTargets(ctx context.Context, group *EntityGroup,
	afterId uint, limit int) (*DeviceGroupTargetPage, error) {
	edges := make([]EntityRelationship, 0, limit)
	q := api.RDB.DB(ctx).Model(&EntityRelationship{}).
		Joins("JOIN entity_relationship_types rt ON rt.id = entity_relationships.relationship_type_id").
		Where("rt.token = ?", MembershipRelationshipType).
		Where("entity_relationships.source_type = ?", string(entity.TypeGroup)).
		Where("entity_relationships.source_id = ?", group.ID).
		// Constrain to the group's family for the same reason staticGroupMembers does:
		// edge creation does not enforce that a member's family matches the group, and
		// target ids are per-table, so a stray group→area edge would otherwise surface
		// an area's id as if it were a device's.
		Where("entity_relationships.target_type = ?", group.MemberType)
	if afterId > 0 {
		q = q.Where("entity_relationships.target_id > ?", afterId)
	}
	if err := q.Select("target_id", "target_token").Order("target_id").
		Limit(limit).Find(&edges).Error; err != nil {
		return nil, err
	}

	members := make([]EntityMember, 0, len(edges))
	for _, e := range edges {
		members = append(members, EntityMember{Id: e.TargetId, Token: e.TargetToken})
	}
	return targetPage(members, limit, nil), nil
}

// targetPage renders resolved members as a page, deriving the cursor from the last row.
//
// A short page means the group is exhausted, so the cursor is zeroed: continuing from the
// last id of a short page would issue one guaranteed-empty query per batch. A FULL page
// always carries a cursor, even when it happens to be the last one — the alternative is
// counting the whole set to find out, which is the scan this walk exists to avoid.
func targetPage(members []EntityMember, limit int, resolvedVersion *int32) *DeviceGroupTargetPage {
	page := &DeviceGroupTargetPage{
		DeviceTokens:    make([]string, 0, len(members)),
		ResolvedVersion: resolvedVersion,
	}
	for _, m := range members {
		page.DeviceTokens = append(page.DeviceTokens, m.Token)
	}
	if len(members) == limit && limit > 0 {
		page.NextCursor = members[len(members)-1].Id
	}
	return page
}
