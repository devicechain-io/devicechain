// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Asset hierarchy (ADR-072) — a parent/child tree over assets, expressed entirely
// as edges of the reserved "contains" relationship type. There is NO new storage
// primitive here and there is deliberately no parent column on Asset: the typed
// relationship graph already stores exactly this, and a second home for the same
// fact is the thing ADR-072 rejects.
//
// # Direction
//
// source = PARENT, target = CHILD, mirroring the "member" edge where the group is
// the source and the member the target. Reading an edge as "source contains
// target" is the whole of the convention.
//
// # The three invariants, and where they are ENFORCED
//
// A tree is a tree only if something keeps it one, so all three are checked in
// admitContainmentEdge — and admitContainmentEdge is called from EVERY path that
// writes a relationship edge, which is four: SetAssetParent, CreateEntityRelationship,
// CreateEntityRelationships and ClaimDevice. That placement is the point rather than
// a detail: SetAssetParent is a convenience door, while the other three take a
// CALLER-CHOSEN relationship type, so each is a way to write a "contains" edge that
// never met this contract. An invariant enforced only in the convenience door is an
// invariant with a public bypass.
//
// 🔴 THIS LIST USED TO SAY "BOTH" AND NAME TWO, AND ClaimDevice WAS THE ONE IT MISSED
// — a claim redeemed with relationshipType "contains" wrote a device→customer
// containment edge past every check. Adding a writer without adding it here is the
// failure mode; the count is spelled out so the next reader can check it against the
// tree rather than trust it.
//
//  1. BOTH ENDS ARE ASSETS. A "contains" edge from an area to a device is not a
//     shallow hierarchy, it is a different concept wearing this one's name, and the
//     tree walks below would follow it into another family.
//  2. AT MOST ONE PARENT. Two parents make the structure a DAG, and every "the path
//     to the root" question — a breadcrumb, an ancestor rollup — stops having one
//     answer.
//  3. NO CYCLE. A cycle makes every ancestor walk non-terminating. It is refused at
//     write time, and the walks are ALSO bounded, because "no cycle exists" is a
//     property of this code being the only writer and the bound is what holds when
//     that stops being true (a hand-run UPDATE, a restored backup, a future path
//     that forgets this file).
const (
	// ContainmentRelationshipType is the reserved asset-hierarchy edge: untracked,
	// because tracked-ness governs whether a relationship is denormalized onto a
	// DEVICE's events as an anchor, and a containment edge has an asset on both
	// ends so it can never be a device's anchor. Assigning a device to an asset —
	// the tracked "assigned" edge — is what gives telemetry its asset context; this
	// type organizes the assets themselves.
	ContainmentRelationshipType = "contains"

	// MaxAssetHierarchyDepth bounds the number of ancestors any asset may have, and
	// therefore how many levels a tree can hold. It exists for two different reasons
	// at once and both are load-bearing:
	//
	//   - at WRITE time it makes the depth a real limit. Placing a child under a
	//     parent that already sits at the limit is refused, so a tree built one edge
	//     at a time cannot exceed it.
	//   - at READ time it makes every ancestor walk terminate whether or not the
	//     graph is acyclic. Without it a cycle introduced outside this code turns a
	//     breadcrumb query into an infinite loop, which is a much worse failure than
	//     a refusal.
	//
	// 64 is chosen to be far past any hierarchy a person authors (plant → line →
	// cell → machine → subassembly is five) while staying small enough that the
	// bounded walk is a handful of indexed reads.
	MaxAssetHierarchyDepth = 64
)

var (
	// ErrAssetParentSelf is returned when an asset is offered as its own parent.
	ErrAssetParentSelf = errors.New("an asset cannot contain itself")
	// ErrAssetParentCycle is returned when the proposed parent is already a
	// descendant of the child, which would close a loop.
	ErrAssetParentCycle = errors.New("the proposed parent is already below this asset in the hierarchy")
	// ErrAssetHierarchyTooDeep is returned when an edge would push a chain past
	// MaxAssetHierarchyDepth, and when a walk hits the bound — which, given the
	// cycle check, means the graph is inconsistent and the walk is refusing to spin.
	ErrAssetHierarchyTooDeep = fmt.Errorf("asset hierarchy would exceed the maximum depth of %d",
		MaxAssetHierarchyDepth)
	// ErrContainmentEndsMustBeAssets is returned when a "contains" edge is offered
	// with a non-asset on either end.
	ErrContainmentEndsMustBeAssets = errors.New("a containment edge must have an asset at both ends")
)

// EnsureContainmentType returns the reserved asset-containment type (Tracked=false)
// for the caller's tenant, creating it on first use — the same auto-provisioning
// the other two reserved types use, for the same reason: relationship types are
// tenant-scoped and there is no tenant roster to seed against at boot.
func (api *Api) EnsureContainmentType(ctx context.Context) (*EntityRelationshipType, error) {
	return api.ensureReservedType(ctx, ContainmentRelationshipType, "Contains",
		"Built-in asset-hierarchy relationship (parent contains child).", false)
}

// admitContainmentEdge is the ONE gate every containment edge passes through,
// whichever door created it. It is a no-op for every other relationship type, so
// the generic edge API keeps its "no legal (source, type, target) triple table"
// posture for everything except the one type that has a structural contract.
//
// parentId/childId are already-resolved row ids; the caller has done the token
// resolution, so this performs only the structural checks.
//
// 🔴 IT READS THROUGH THE HANDLE THE CALLER IS WRITING ON, which is why tx is a
// parameter rather than something this function fetches for itself. Every caller
// runs inside a transaction, and a read issued on a fresh session sees the state
// BEFORE that transaction — so a re-parent that deletes the old edge and then asks
// "does this asset already have a parent?" would see the deleted edge and refuse
// every move, and a batch that creates two edges would check each against the state
// before the batch and let the pair close a loop. Both are the same mistake: reading
// somewhere other than where you are writing.
func (api *Api) admitContainmentEdge(tx *gorm.DB, relationshipToken string,
	sourceType string, parentId uint, targetType string, childId uint) error {

	if relationshipToken != ContainmentRelationshipType {
		return nil
	}
	if sourceType != string(entity.TypeAsset) || targetType != string(entity.TypeAsset) {
		return fmt.Errorf("%w (got %s -> %s)", ErrContainmentEndsMustBeAssets, sourceType, targetType)
	}
	if parentId == childId {
		return ErrAssetParentSelf
	}

	// One parent. The check is on the CHILD's existing incoming edges, and it is a
	// refusal rather than a replace: SetAssetParent re-parents by deleting the old
	// edge first, so reaching here with an edge already present means a caller went
	// around it, and silently accepting a second parent is how a tree becomes a DAG
	// nobody decided to allow.
	var existing int64
	if err := api.containmentEdges(tx).Where("target_id = ?", childId).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return fmt.Errorf("asset already has a parent: clear it before assigning another")
	}

	// Cycle + depth, in one walk: the proposed parent's ancestor chain must neither
	// contain the child nor already be at the limit.
	ancestors, err := api.assetAncestorIds(tx, parentId)
	if err != nil {
		return err
	}
	for _, id := range ancestors {
		if id == childId {
			return ErrAssetParentCycle
		}
	}
	// The depth check counts the parent's chain, the parent itself, the child about to
	// be placed, and the child's OWN SUBTREE — which travels with it. Checking only
	// the parent's chain would let a re-parent produce a tree deeper than the limit:
	// the limit would then hold for every edge ever created and not for the tree they
	// form, which is the kind of bound that reads as enforced and is not.
	//
	// There is deliberately only ONE comparison. An earlier version tested the
	// parent's chain first and then the chain-plus-height, and the first test could
	// never fire: height is a count and so is >= 0, which makes the second condition
	// strictly weaker. A check that cannot fail is worse than no check — it reads as
	// coverage. (A surviving mutant is what pointed at it: deleting the first
	// comparison changed nothing anywhere.)
	height, err := api.assetSubtreeHeight(tx, childId, MaxAssetHierarchyDepth)
	if err != nil {
		return err
	}
	if len(ancestors)+2+height > MaxAssetHierarchyDepth {
		return ErrAssetHierarchyTooDeep
	}
	return nil
}

// containmentEdges is the base query for asset-containment edges in the caller's
// tenant, issued on the given handle. The relationship-type subquery is spelled out
// here once so no caller hand-writes it and quietly matches every edge type.
//
// tx is the transaction (or plain session) to read on; see admitContainmentEdge for
// why that is never assumed. Pass api.RDB.DB(ctx) outside a transaction.
func (api *Api) containmentEdges(tx *gorm.DB) *gorm.DB {
	return tx.Model(&EntityRelationship{}).
		Where("source_type = ? AND target_type = ?", string(entity.TypeAsset), string(entity.TypeAsset)).
		Where("relationship_type_id = (?)",
			tx.Model(&EntityRelationshipType{}).Select("id").
				Where("token = ?", ContainmentRelationshipType))
}

// assetAncestorIds walks from an asset up to its root, nearest ancestor first. The
// walk is bounded at MaxAssetHierarchyDepth and returns ErrAssetHierarchyTooDeep
// rather than continuing: reaching the bound means either a chain longer than the
// write path can build or a cycle, and both are states where a truncated answer
// would be indistinguishable from a correct one.
func (api *Api) assetAncestorIds(tx *gorm.DB, assetId uint) ([]uint, error) {
	ancestors := make([]uint, 0)
	current := assetId
	for i := 0; i < MaxAssetHierarchyDepth; i++ {
		var parentIds []uint
		if err := api.containmentEdges(tx).Where("target_id = ?", current).
			Limit(1).Pluck("source_id", &parentIds).Error; err != nil {
			return nil, err
		}
		if len(parentIds) == 0 {
			return ancestors, nil
		}
		ancestors = append(ancestors, parentIds[0])
		current = parentIds[0]
	}
	return nil, ErrAssetHierarchyTooDeep
}

// assetSubtreeHeight returns how many LEVELS hang below an asset (0 for a leaf),
// walking breadth-first and stopping as soon as the answer exceeds limit — the
// caller only ever needs to know whether a bound is breached, so a wide tree is not
// enumerated past the point where the answer is settled.
func (api *Api) assetSubtreeHeight(tx *gorm.DB, assetId uint, limit int) (int, error) {
	level := []uint{assetId}
	height := 0
	for height <= limit {
		var next []uint
		if err := api.containmentEdges(tx).Where("target_id IS NOT NULL").
			Where("source_id IN ?", level).Pluck("target_id", &next).Error; err != nil {
			return 0, err
		}
		if len(next) == 0 {
			return height, nil
		}
		height++
		level = next
	}
	return height, nil
}

// SetAssetParent places an asset under a parent, replacing whatever parent it had.
// It is the authoring door for the hierarchy; the structural contract it upholds is
// enforced in admitContainmentEdge, which the generic edge API shares.
//
// Re-parenting is delete-then-create inside one transaction, so an asset is never
// briefly parentless nor briefly two-parented, and a refused move leaves the old
// parent in place rather than orphaning a subtree.
func (api *Api) SetAssetParent(ctx context.Context, childToken, parentToken string) (*EntityRelationship, error) {
	if _, err := api.EnsureContainmentType(ctx); err != nil {
		return nil, err
	}
	childId, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), childToken)
	if err != nil {
		return nil, fmt.Errorf("child: %w", err)
	}
	parentId, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), parentToken)
	if err != nil {
		return nil, fmt.Errorf("parent: %w", err)
	}

	// The self and cycle checks run BEFORE the old edge is deleted. Deleting first
	// and validating after would drop an asset out of the tree on a refused move.
	if childId == parentId {
		return nil, ErrAssetParentSelf
	}

	var created *EntityRelationship
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := api.clearAssetParentOn(tx, childId); err != nil {
			return err
		}
		// Re-checked inside the transaction and after the clear, so the "at most one
		// parent" count sees the state this edge is actually joining.
		if err := api.admitContainmentEdge(tx, ContainmentRelationshipType,
			string(entity.TypeAsset), parentId, string(entity.TypeAsset), childId); err != nil {
			return err
		}

		var typeId uint
		if err := tx.Model(&EntityRelationshipType{}).Select("id").
			Where("token = ?", ContainmentRelationshipType).Scan(&typeId).Error; err != nil {
			return err
		}
		if typeId == 0 {
			return fmt.Errorf("reserved relationship type %q missing", ContainmentRelationshipType)
		}

		edge := &EntityRelationship{
			TokenReference:     rdb.TokenReference{Token: uuid.New().String()},
			SourceType:         string(entity.TypeAsset),
			SourceId:           parentId,
			TargetType:         string(entity.TypeAsset),
			TargetId:           childId,
			TargetToken:        childToken,
			RelationshipTypeId: typeId,
		}
		if err := tx.Create(edge).Error; err != nil {
			return err
		}
		created = edge
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ClearAssetParent detaches an asset from its parent, making it a root. Its own
// children travel with it — this moves one edge, not a subtree. Reports whether an
// edge was removed, so "already a root" is distinguishable from "done".
func (api *Api) ClearAssetParent(ctx context.Context, childToken string) (bool, error) {
	childId, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), childToken)
	if err != nil {
		return false, err
	}

	var before int64
	if err := api.containmentEdges(api.RDB.DB(ctx)).Where("target_id = ?", childId).Count(&before).Error; err != nil {
		return false, err
	}
	if before == 0 {
		return false, nil
	}
	if err := api.clearAssetParentOn(api.RDB.DB(ctx), childId); err != nil {
		return false, err
	}
	return true, nil
}

// clearAssetParentOn removes an asset's incoming containment edge on the given
// handle (a transaction or the plain session). It is a soft delete like every other
// entity removal here, so the historical edge stays readable while the live-rows
// queries stop seeing it.
func (api *Api) clearAssetParentOn(tx *gorm.DB, childId uint) error {
	var ids []uint
	if err := api.containmentEdges(tx).Where("target_id = ?", childId).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return tx.Where("id IN ?", ids).Delete(&EntityRelationship{}).Error
}

// AssetParent returns an asset's parent, or nil when it is a root. A root is a
// normal, expected state — most assets in a flat tenant are roots — so this is a
// nil result rather than an error.
func (api *Api) AssetParent(ctx context.Context, token string) (*Asset, error) {
	assetId, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), token)
	if err != nil {
		return nil, err
	}
	var parentIds []uint
	if err := api.containmentEdges(api.RDB.DB(ctx)).Where("target_id = ?", assetId).
		Limit(1).Pluck("source_id", &parentIds).Error; err != nil {
		return nil, err
	}
	if len(parentIds) == 0 {
		return nil, nil
	}
	found, err := api.AssetsById(ctx, parentIds)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		// The edge names a parent that no longer exists — a dangling reference, not a
		// root. Reporting it as a root would hide the inconsistency behind a
		// plausible answer.
		return nil, fmt.Errorf("%w: containment edge names asset id %d", gorm.ErrRecordNotFound, parentIds[0])
	}
	return found[0], nil
}

// AssetAncestors returns the path from an asset to its root, NEAREST FIRST — the
// order a breadcrumb is built from. The walk is bounded; see assetAncestorIds.
func (api *Api) AssetAncestors(ctx context.Context, token string) ([]*Asset, error) {
	assetId, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), token)
	if err != nil {
		return nil, err
	}
	ids, err := api.assetAncestorIds(api.RDB.DB(ctx), assetId)
	if err != nil {
		return nil, err
	}

	found, err := api.AssetsById(ctx, ids)
	if err != nil {
		return nil, err
	}
	// AssetsById answers in its own order, so re-order to the walk's. The walk's
	// order IS the answer here — a breadcrumb printed root-first when it should be
	// nearest-first is wrong in a way that still looks like a breadcrumb.
	byId := make(map[uint]*Asset, len(found))
	for _, asset := range found {
		byId[asset.ID] = asset
	}
	ordered := make([]*Asset, 0, len(ids))
	for _, id := range ids {
		if asset, ok := byId[id]; ok {
			ordered = append(ordered, asset)
		}
	}
	return ordered, nil
}

// AssetChildren pages the assets directly below a parent, or the ROOTS of the tree
// when parentToken is nil. Those two are one function on purpose: a tree browser
// asks the same question at every level, and the root level differs only in that
// the answer is "the assets with no incoming containment edge".
//
// It is a page of Assets, ordered by Asset.DefaultOrder, so it composes with every
// other asset list surface rather than inventing an ordering for the tree.
func (api *Api) AssetChildren(ctx context.Context, parentToken *string,
	pagination rdb.Pagination) (*AssetSearchResults, error) {

	var parentId uint
	if parentToken != nil {
		resolved, err := api.ResolveEntityToken(ctx, string(entity.TypeAsset), *parentToken)
		if err != nil {
			return nil, err
		}
		parentId = resolved
	}

	results := make([]Asset, 0)
	db, pag := api.RDB.ListOf(ctx, &Asset{}, func(result *gorm.DB) *gorm.DB {
		if parentToken != nil {
			result = result.Where("assets.id IN (?)",
				api.containmentEdges(api.RDB.DB(ctx)).Select("target_id").Where("source_id = ?", parentId))
		} else {
			result = result.Where("assets.id NOT IN (?)",
				api.containmentEdges(api.RDB.DB(ctx)).Select("target_id"))
		}
		return result.Preload("AssetType")
	}, pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AssetSearchResults{Results: results, Pagination: pag}, nil
}
