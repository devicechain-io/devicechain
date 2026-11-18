// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Create-enrollment MARKER healing: giving a group@version registered without the marker the
// marker it should have, and the members it missed for want of it.
//
// A registration writes the marker (an EntityGroupFacetRef under createEnrollFacetKey) when the
// frozen selector matches an entity with no attributes, and every create path reads it to enroll
// the new entity in the same transaction (enrollNewEntitiesOnTx). A registration made by a release
// older than the marker has none: every group@version registered before the upgrade, and any a
// replica of the previous release registers during the rolling upgrade. Reconcile does not
// rebuild an already-registered version, so without this nothing ever gives it one, and an entity
// created after the upgrade is not enrolled in a group whose selector matches it.
//
// The marker cannot be written by a migration. Whether a selector matches an entity with no
// attributes is the selector engine's judgement (selector.MatchesEmpty), which SQL cannot make,
// and a migration that ran the live engine would change what it does whenever the engine did. A
// migration would also run once, before the last replica of the previous release has stopped
// registering. So it is a periodic pass, on every replica:
//
//  1. Read, across tenants, every registered group@version (it has facet refs: a compiled
//     selector always references at least one facet) and which of them carry the marker.
//  2. For each without one, compile its frozen selector. A selector that does not match the
//     empty attribute set needs no marker; a frozen version never changes, so that verdict is
//     remembered for the life of the process and never computed again. A version that has a
//     marker row is skipped too, unless its selector itself names the reserved key: Keys() then
//     writes a ref of the same shape, which an earlier release wrote as well, so the row proves
//     nothing. Such a version is rebuilt once per process (a rebuild is idempotent), and then
//     remembered as done.
//  3. For one that does, REBUILD its registration, under the family lock, in the tenant's own
//     transaction: registerGroupVersionOnTx writes the marker with the refs and re-materializes
//     the members, so the entities created since the registration without being enrolled are
//     enrolled now. If the version is an access scope's published version, the devices whose
//     membership the rebuild changed are owed their new owners (ownership_outbox.go), queued
//     last in the transaction as every other access change does.
//
// Under the lock, the rebuild re-reads the refs first: a version deregistered since step 1 is
// not registered again, and one another replica healed is left alone (unless, as above, its
// marker row cannot be told from the selector's own ref). A create that waits on the
// lock reads the marker the rebuild committed; one that held it first is committed when the
// rebuild reads the members. So two replicas, or a pass and a create, cannot leave an entity out.
type CreateEnrollMarkerHealer struct {
	api *Api

	mu sync.Mutex
	// settled holds the group@versions this process has finished with: the frozen selector does
	// not match an entity with no attributes, or the version carries a marker that is the
	// registration's own, or this process rebuilt it. Group ids are a global serial and a frozen
	// version is immutable, so none of these can change.
	settled map[enrollMarkerKey]bool
}

type enrollMarkerKey struct {
	GroupId         uint
	SelectorVersion int32
}

// NewCreateEnrollMarkerHealer builds the healer over api.
func NewCreateEnrollMarkerHealer(api *Api) *CreateEnrollMarkerHealer {
	return &CreateEnrollMarkerHealer{api: api, settled: map[enrollMarkerKey]bool{}}
}

// registeredGroupVersion is one registered group@version as the cross-tenant read returns it.
type registeredGroupVersion struct {
	TenantId        string
	GroupId         uint
	SelectorVersion int32
}

// Pass heals every registered group@version that needs the marker and has none, and returns how
// many it rebuilt. A tenant's failure is logged and left to the next pass; the others proceed.
func (h *CreateEnrollMarkerHealer) Pass(ctx context.Context) (int, error) {
	db := h.api.RDB.DB(core.WithSystemContext(ctx))
	var registered, marked []registeredGroupVersion
	if err := db.Model(&EntityGroupFacetRef{}).Distinct("tenant_id", "group_id", "selector_version").
		Order("tenant_id, group_id, selector_version").Scan(&registered).Error; err != nil {
		return 0, fmt.Errorf("listing registered group versions: %w", err)
	}
	if err := db.Model(&EntityGroupFacetRef{}).Where("facet_key = ?", createEnrollFacetKey).
		Distinct("tenant_id", "group_id", "selector_version").Scan(&marked).Error; err != nil {
		return 0, fmt.Errorf("listing marked group versions: %w", err)
	}
	has := make(map[enrollMarkerKey]bool, len(marked))
	for _, m := range marked {
		has[enrollMarkerKey{m.GroupId, m.SelectorVersion}] = true
	}
	healed, visited, failed := 0, 0, 0
	var lastErr error
	for _, r := range registered {
		key := enrollMarkerKey{r.GroupId, r.SelectorVersion}
		if h.isSettled(key) {
			continue
		}
		visited++
		did, err := h.heal(core.WithTenant(ctx, r.TenantId), key, has[key])
		if err != nil {
			failed++
			lastErr = err
			log.Warn().Err(err).Str("tenant", r.TenantId).Uint("group", r.GroupId).Int32("version", r.SelectorVersion).
				Msg("Unable to give a group version registered by an earlier release its create-enrollment marker; the next pass retries")
			continue
		}
		if did {
			healed++
		}
	}
	if healed > 0 {
		log.Info().Int("groupVersions", healed).
			Msg("Rebuilt group versions registered without the create-enrollment marker; entities created since are enrolled.")
	}
	return healed, core.PassResult(visited, failed, lastErr)
}

func (h *CreateEnrollMarkerHealer) isSettled(key enrollMarkerKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settled[key]
}

func (h *CreateEnrollMarkerHealer) settle(key enrollMarkerKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settled[key] = true
}

// heal judges one group@version and rebuilds its registration when it needs the marker. marked
// says whether the version had a ref under the reserved key when the pass read it. It reports
// whether it rebuilt.
func (h *CreateEnrollMarkerHealer) heal(ctx context.Context, key enrollMarkerKey, marked bool) (bool, error) {
	api := h.api
	db := api.RDB.DB(ctx)
	var group EntityGroup
	if err := db.Where("id = ?", key.GroupId).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil // deleted since the read; its teardown removes the refs
		}
		return false, err
	}
	var frozen EntityGroupVersion
	if err := db.Where("entity_group_id = ? AND version = ?", key.GroupId, key.SelectorVersion).First(&frozen).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	sel, err := selector.Compile(frozen.Selector, frozen.MemberType)
	if err != nil {
		return false, err
	}
	empty, err := sel.MatchesEmpty()
	if err != nil {
		return false, err
	}
	// A ref under the reserved key is the registration's marker unless the selector names the key
	// itself, in which case an earlier release wrote the same row and it proves nothing.
	ambiguous := slices.Contains(sel.Keys(), createEnrollFacetKey)
	if !empty || (marked && !ambiguous) {
		h.settle(key)
		return false, nil
	}

	var evictIds []uint
	rebuilt := false
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := api.lockMembershipFamily(ctx, tx, frozen.MemberType); err != nil {
			return err
		}
		var refs []EntityGroupFacetRef
		if err := tx.Where("group_id = ? AND selector_version = ?", key.GroupId, key.SelectorVersion).Find(&refs).Error; err != nil {
			return err
		}
		if len(refs) == 0 {
			return nil // deregistered since the read
		}
		if !ambiguous && slices.ContainsFunc(refs, func(r EntityGroupFacetRef) bool { return r.FacetKey == createEnrollFacetKey }) {
			return nil // healed by another replica since the read
		}
		current, registered, keys, err := api.loadGroupVersionForScoping(ctx, tx, group.Token, key.SelectorVersion)
		if err != nil {
			return err
		}
		access := current.MemberType == string(entity.TypeDevice) && current.IsAccessScope() &&
			current.ActiveVersion.Valid && current.ActiveVersion.Int32 == key.SelectorVersion
		version := sql.NullInt32{Int32: key.SelectorVersion, Valid: true}
		var before []uint
		if access {
			if before, err = api.membersAtOnTx(tx, current.ID, version); err != nil {
				return err
			}
		}
		if evictIds, err = api.registerGroupVersionOnTx(ctx, tx, current, registered, keys); err != nil {
			return err
		}
		rebuilt = true
		if !access {
			return nil
		}
		after, err := api.membersAtOnTx(tx, current.ID, version)
		if err != nil {
			return err
		}
		// The devices whose anchors the rebuild changed: in one member set and not the other.
		_, err = queueOwnershipOnTx(tx, symmetricDifference(before, after))
		return err
	})
	if err != nil {
		return false, err
	}
	h.settle(key)
	api.evictMemberships(ctx, frozen.MemberType, evictIds)
	return rebuilt, nil
}

// symmetricDifference returns the ids in exactly one of a and b.
func symmetricDifference(a, b []uint) []uint {
	inA := make(map[uint]bool, len(a))
	for _, id := range a {
		inA[id] = true
	}
	inB := make(map[uint]bool, len(b))
	out := make([]uint, 0)
	for _, id := range b {
		inB[id] = true
		if !inA[id] {
			out = append(out, id)
		}
	}
	for _, id := range a {
		if !inB[id] {
			out = append(out, id)
		}
	}
	return out
}
