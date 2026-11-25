// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This file holds the uniform entity-relationship API (ADR-013). One set of
// methods over the single EntityRelationship edge table replaces the per-family
// relationship CRUD that previously duplicated this logic four times.

// ErrDeviceAuthoredOwnership identifies a terminal refusal of device-authored graph ownership.
var ErrDeviceAuthoredOwnership = errors.New("a device may not create an ownership (tracked) or containment relationship; assign devices with a claim code or the createEntityRelationship mutation")

func refuseDeviceAuthoredRelationshipType(token string, tracked bool) error {
	if tracked || token == ContainmentRelationshipType {
		return fmt.Errorf("%w: relationship type %q", ErrDeviceAuthoredOwnership, token)
	}
	return nil
}

// RefuseDeviceAuthoredRelationshipType checks before an idempotency read or any write.
// Reserved types need no provisioning; unknown tenant types retain normal not-found handling.
func RefuseDeviceAuthoredRelationshipType(ctx context.Context, api interface {
	EntityRelationshipTypesByToken(context.Context, []string) ([]*EntityRelationshipType, error)
}, token string) error {
	// The STORED type decides, reserved tokens included: an administrator can mark any type
	// tracked, "member" among them, and a token-only answer would let a device keep asserting
	// a type that has since become ownership. A reserved type not provisioned yet in the tenant
	// is decided by the flag it would be created with.
	types, err := api.EntityRelationshipTypesByToken(ctx, []string{token})
	if err != nil {
		return err
	}
	if len(types) > 0 {
		return refuseDeviceAuthoredRelationshipType(token, types[0].Tracked)
	}
	return refuseDeviceAuthoredRelationshipType(token, token == AssignmentRelationshipType)
}

// CreateEntityRelationshipType creates a new relationship type.
func (api *Api) CreateEntityRelationshipType(ctx context.Context,
	request *EntityRelationshipTypeCreateRequest) (*EntityRelationshipType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	predicate, inverse, err := api.bindPredicateOnCreate(ctx, request.SemanticPredicate, request.SemanticInverse)
	if err != nil {
		return nil, err
	}
	created := &EntityRelationshipType{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		Tracked:        request.Tracked,

		SemanticPredicate: predicate,
		SemanticInverse:   inverse,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateEntityRelationshipType applies a PARTIAL update to a relationship type: a
// field the caller did not name keeps its stored value, an explicit null clears a
// nullable one, and `tracked` — which has no nullable reading — refuses a null.
// Only named columns are written; omitted fields remain untouched even when
// another writer changes them after the read.
func (api *Api) UpdateEntityRelationshipType(ctx context.Context, token string,
	request *EntityRelationshipTypeUpdateRequest) (*EntityRelationshipType, error) {
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]
	// Tracked resolves BEFORE anything is written, so a refused `tracked: null` refuses
	// the whole update rather than applying the other fields first.
	tracked, err := request.Tracked.ApplyToRequired("tracked", updated.Tracked)
	if err != nil {
		return nil, err
	}

	assignments := map[string]any{}
	if request.Name.Set {
		assignments["name"] = request.Name.ApplyToNullString(updated.Name)
	}
	if request.Description.Set {
		assignments["description"] = request.Description.ApplyToNullString(updated.Description)
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	if request.Metadata.Set {
		assignments["metadata"] = metadataJSON
	}
	predicate, inverse, err := api.bindPredicateOnUpdate(ctx,
		request.SemanticPredicate, request.SemanticInverse, updated.SemanticPredicate, updated.SemanticInverse)
	if err != nil {
		return nil, err
	}
	flipped := tracked != updated.Tracked
	if request.Tracked.Set {
		assignments["tracked"] = tracked
	}
	if request.SemanticPredicate.Set || request.SemanticInverse.Set {
		assignments["semantic_predicate"] = predicate
		assignments["semantic_inverse"] = inverse
	}
	if len(assignments) == 0 {
		return updated, nil
	}
	write := func(tx *gorm.DB) error {
		res := rdb.AdvancingFrom(tx, updated.UpdatedAt).Model(updated).Omit(clause.Associations).
			Where("id = ?", updated.ID).Updates(assignments)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	var queued ownershipQueued
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if request.Tracked.Set {
			// Re-read the flag under UPDATE: a concurrent writer may have flipped it
			// since the outer read. Device creates hold SHARE until their edge commits.
			var current EntityRelationshipType
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", updated.ID).First(&current).Error; err != nil {
				return err
			}
			flipped = tracked != current.Tracked
			if tracked && !current.Tracked {
				if err := refuseTrackingDeviceAuthoredEdges(tx, &current); err != nil {
					return err
				}
				if err := checkTrackedFlipCeiling(tx, current.ID); err != nil {
					return err
				}
			}
		}
		if err := write(tx); err != nil {
			return err
		}
		if flipped {
			// Queue new ownership facts in the flip's transaction, before eviction.
			sources, err := sourceDevicesOnTx(tx, "relationship_type_id = ?", updated.ID)
			if err != nil {
				return err
			}
			queued, err = queueOwnershipOnTx(tx, sources)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if flipped {
		api.evictRelationshipSources(ctx, queued)
	} else {
		// Cached edges carry the type's fields. A rename evicts those copies but
		// changes nobody's ownership, so it needs no fact per source device.
		sources := api.relationshipSourceDevices(ctx, "relationship_type_id = ?", updated.ID)
		if api.CacheEvictor != nil && len(sources) > 0 {
			api.CacheEvictor.EvictRelationshipSources(ctx, sources)
		}
	}

	matches, err = api.EntityRelationshipTypesById(ctx, []uint{updated.ID})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// EntityRelationshipTypesById looks up relationship types by id.
func (api *Api) EntityRelationshipTypesById(ctx context.Context, ids []uint) ([]*EntityRelationshipType, error) {
	return rdb.FindByIds[EntityRelationshipType](api.RDB.DB(ctx), ids)
}

// EntityRelationshipTypesByToken looks up relationship types by token.
func (api *Api) EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*EntityRelationshipType, error) {
	found := make([]*EntityRelationshipType, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// EntityRelationshipTypes searches relationship types.
func (api *Api) EntityRelationshipTypes(ctx context.Context,
	criteria EntityRelationshipTypeSearchCriteria) (*EntityRelationshipTypeSearchResults, error) {
	results := make([]EntityRelationshipType, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityRelationshipTypeSearchResults{Results: results, Pagination: pag}, nil
}

// CreateEntityRelationship creates a relationship edge. The source and target are
// each resolved from (type, token) to a row id via the entity-type registry,
// which validates both the type and the entity's existence (ADR-013).
func (api *Api) CreateEntityRelationship(ctx context.Context,
	request *EntityRelationshipCreateRequest) (*EntityRelationship, error) {
	if request.DeviceAuthored {
		types, err := api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
		if err != nil {
			return nil, err
		}
		if len(types) == 0 {
			if err := refuseDeviceAuthoredRelationshipType(request.RelationshipType, request.RelationshipType == AssignmentRelationshipType); err != nil {
				return nil, err
			}
		}
	}
	sourceId, err := api.ResolveEntityToken(ctx, request.SourceType, request.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	targetId, err := api.ResolveEntityToken(ctx, request.TargetType, request.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	// A reserved type is auto-provisioned on first use, exactly as the bulk path
	// does it. Without this a caller naming "contains" (or "member", or "assigned")
	// before anything else in the tenant had used it got ErrRecordNotFound for a
	// type the platform owns and would have created for them.
	reserved, err := api.ensureReservedTypeByToken(ctx, request.RelationshipType)
	if err != nil {
		return nil, err
	}
	rtmatches := []*EntityRelationshipType{}
	if reserved != nil {
		rtmatches = append(rtmatches, reserved)
	} else {
		rtmatches, err = api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
		if err != nil {
			return nil, err
		}
	}
	if len(rtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if request.DeviceAuthored {
		if err := refuseDeviceAuthoredRelationshipType(request.RelationshipType, rtmatches[0].Tracked); err != nil {
			return nil, err
		}
	}

	if err := api.checkTrackedCeiling(ctx, api.RDB.DB(ctx), rtmatches[0], request.SourceType, sourceId, 1); err != nil {
		return nil, err
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &EntityRelationship{
		TokenReference:     rdb.TokenReference{Token: request.Token},
		MetadataEntity:     rdb.MetadataEntity{Metadata: metadataJSON},
		SourceType:         request.SourceType,
		SourceId:           sourceId,
		TargetType:         request.TargetType,
		TargetId:           targetId,
		TargetToken:        request.Target,
		RelationshipTypeId: rtmatches[0].ID,
	}
	if request.RelationshipType == ContainmentRelationshipType {
		// Admission acquires the tenant's transaction-scoped hierarchy lock. The
		// insert must use that transaction too, so a rival checks committed state.
		err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if err := api.admitContainmentEdge(tx, request.RelationshipType,
				request.SourceType, sourceId, request.TargetType, targetId); err != nil {
				return err
			}
			return tx.Create(created).Error
		})
		if err != nil {
			return nil, err
		}
		created.RelationshipType = *rtmatches[0]
		return created, nil
	}
	if created.SourceType != string(entity.TypeDevice) {
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			return createRelationshipWithGenerationTx(tx, created)
		}); err != nil {
			return nil, err
		}
		created.RelationshipType = *rtmatches[0]
		return created, nil
	}
	// A new edge from a device may make it owned (a tracked type): the edge and the
	// device's queued ownership fact commit together.
	var queued ownershipQueued
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if request.DeviceAuthored {
			var current EntityRelationshipType
			// SHARE conflicts with the flip's UPDATE lock until this edge commits.
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", rtmatches[0].ID).First(&current).Error; err != nil {
				return err
			}
			if err := refuseDeviceAuthoredRelationshipType(current.Token, current.Tracked); err != nil {
				return err
			}
			rtmatches[0] = &current
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		var err error
		queued, err = queueOwnershipOnTx(tx, []uint{created.SourceId})
		return err
	})
	if err != nil {
		return nil, err
	}
	// Populate the association from the type we already resolved, so a resolver
	// selecting relationshipType on the mutation result gets real values. Read
	// paths Preload it; the create path must set it explicitly or it stays zero.
	created.RelationshipType = *rtmatches[0]
	// A new edge from a device changes that device's cached tracked set, whether or not its
	// type is tracked (the cache holds the tracked edges, and the type decides): drop it,
	// after the commit, on every replica.
	api.evictRelationshipSources(ctx, queued)
	return created, nil
}

// EntityRelationshipsById looks up relationships by id.
func (api *Api) EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error) {
	return rdb.FindByIds[EntityRelationship](api.RDB.DB(ctx).Preload("RelationshipType"), ids)
}

// EntityRelationshipsByToken looks up relationships by token.
func (api *Api) EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*EntityRelationship, error) {
	found := make([]*EntityRelationship, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("RelationshipType"), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// entityRelationshipFilters builds the WHERE clauses for a relationship search, shared
// by the paged EntityRelationships and the full-set TrackedRelationshipsForDevice so the
// two cannot drift into filtering differently.
func (api *Api) entityRelationshipFilters(ctx context.Context,
	criteria EntityRelationshipSearchCriteria) func(result *gorm.DB) *gorm.DB {
	return func(result *gorm.DB) *gorm.DB {
		if criteria.SourceType != nil {
			result = result.Where("source_type = ?", *criteria.SourceType)
		}
		if criteria.SourceId != nil {
			result = result.Where("source_id = ?", *criteria.SourceId)
		}
		if criteria.TargetType != nil {
			result = result.Where("target_type = ?", *criteria.TargetType)
		}
		if criteria.TargetId != nil {
			result = result.Where("target_id = ?", *criteria.TargetId)
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("token = ?", *criteria.RelationshipType))
		}
		if criteria.Tracked != nil {
			result = result.Where("relationship_type_id in (?)",
				api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", *criteria.Tracked))
		}
		return result
	}
}

// EntityRelationships searches relationships, ONE PAGE at a time. Source is matched by
// the resolved (SourceType, SourceId); Tracked filters by the relationship type's flag.
//
// This is the GraphQL-facing read and it is always bounded. The event resolver's
// whole-set read is TrackedRelationshipsForDevice, which is a separate method precisely
// so that this one cannot be talked into returning the table.
func (api *Api) EntityRelationships(ctx context.Context,
	criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error) {
	results := make([]EntityRelationship, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityRelationship{},
		api.entityRelationshipFilters(ctx, criteria), criteria.Pagination)
	db.Preload("RelationshipType").Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityRelationshipSearchResults{Results: results, Pagination: pag}, nil
}

// TrackedRelationshipsForDevice returns EVERY tracked relationship whose source is the
// given device, with no LIMIT.
//
// 🔴 IT IS A NAMED METHOD RATHER THAN A FLAG ON A SEARCH, and the difference is what
// makes the unbounded read reviewable. The caller used to build an ordinary
// EntityRelationshipSearchCriteria and set Pagination.Unbounded on it; the cache in
// front of this API then had to SNIFF that criteria shape to recognize the one query it
// serves. Both the flag and the sniffing existed because the API had no way to say what
// this call actually is. Now it does, and both are gone.
//
// The full set is what event resolution needs: a device's tracked-relationship targets
// are denormalized in full onto every event (ADR-013 addendum 2026-07-01), so a page of
// them would silently drop anchors. It is bounded by how many relationships one device
// has, not by a LIMIT — which is the standard ListAllOf asks callers to meet.
func (api *Api) TrackedRelationshipsForDevice(ctx context.Context,
	deviceId uint) (*EntityRelationshipSearchResults, error) {
	tracked := true
	sourceType := string(entity.TypeDevice)
	criteria := EntityRelationshipSearchCriteria{
		SourceType: &sourceType,
		SourceId:   &deviceId,
		Tracked:    &tracked,
	}
	results := make([]EntityRelationship, 0)
	db, pag := api.RDB.ListAllOf(ctx, &EntityRelationship{},
		api.entityRelationshipFilters(ctx, criteria))
	db.Preload("RelationshipType").Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityRelationshipSearchResults{Results: results, Pagination: pag}, nil
}
