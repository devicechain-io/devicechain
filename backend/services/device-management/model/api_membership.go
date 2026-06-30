// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Bulk relationship-edge operations (ADR-013) plus the reserved group-membership
// relationship type. Group membership is just an edge (group -> member) of the
// well-known "member" type, so a single set of generic edge mutations serves both
// membership and future assignment surfaces.

// MembershipRelationshipType is the reserved token for the built-in group <-> member
// relationship. It is auto-provisioned per tenant on first use rather than seeded at
// startup: relationship types are tenant-scoped, and a tenant-agnostic service has no
// tenant roster to seed against at boot (and new tenants would be missed anyway).
const MembershipRelationshipType = "member"

// EnsureMembershipType returns the reserved membership relationship type for the
// caller's tenant, creating it (Tracked=false) on first use. Idempotent and safe
// under concurrent callers via an ON CONFLICT DO NOTHING insert + read-back.
func (api *Api) EnsureMembershipType(ctx context.Context) (*EntityRelationshipType, error) {
	if matches, err := api.EntityRelationshipTypesByToken(ctx, []string{MembershipRelationshipType}); err != nil {
		return nil, err
	} else if len(matches) > 0 {
		return matches[0], nil
	}

	name := "Member"
	description := "Built-in group-membership relationship."
	created := &EntityRelationshipType{
		TokenReference: rdb.TokenReference{Token: MembershipRelationshipType},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(&name),
			Description: rdb.NullStrOf(&description),
		},
		Tracked: false,
	}
	if err := api.RDB.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(created).Error; err != nil {
		return nil, err
	}
	if created.ID != 0 {
		return created, nil
	}
	// Lost the create race (token already existed) — read back the winner.
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{MembershipRelationshipType})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("membership relationship type missing after ensure")
	}
	return matches[0], nil
}

// CreateEntityRelationships creates many relationship edges in one transaction, so a
// bulk "add members" / "assign" either fully applies or not at all. Each request's
// source and target are resolved from (type, token) like the single create. If any
// request uses the reserved membership type it is auto-provisioned first.
func (api *Api) CreateEntityRelationships(ctx context.Context,
	requests []*EntityRelationshipCreateRequest) ([]*EntityRelationship, error) {
	if len(requests) == 0 {
		return []*EntityRelationship{}, nil
	}
	for _, request := range requests {
		if request.RelationshipType == MembershipRelationshipType {
			if _, err := api.EnsureMembershipType(ctx); err != nil {
				return nil, err
			}
			break
		}
	}

	created := make([]*EntityRelationship, 0, len(requests))
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, request := range requests {
			sourceId, err := api.ResolveEntityToken(ctx, request.SourceType, request.Source)
			if err != nil {
				return fmt.Errorf("source: %w", err)
			}
			targetId, err := api.ResolveEntityToken(ctx, request.TargetType, request.Target)
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
			rtmatches, err := api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
			if err != nil {
				return err
			}
			if len(rtmatches) == 0 {
				return gorm.ErrRecordNotFound
			}
			edge := &EntityRelationship{
				TokenReference:     rdb.TokenReference{Token: request.Token},
				MetadataEntity:     rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
				SourceType:         request.SourceType,
				SourceId:           sourceId,
				TargetType:         request.TargetType,
				TargetId:           targetId,
				RelationshipTypeId: rtmatches[0].ID,
			}
			if err := tx.Create(edge).Error; err != nil {
				return err
			}
			edge.RelationshipType = *rtmatches[0]
			created = append(created, edge)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// RemoveEntityRelationships hard-deletes many relationship edges by token (a bulk
// "remove members" / "unassign"). Tenant scoping still applies via the global
// callback. Returns true if any edge was removed.
func (api *Api) RemoveEntityRelationships(ctx context.Context, tokens []string) (bool, error) {
	if len(tokens) == 0 {
		return false, nil
	}
	result := api.RDB.DB(ctx).Unscoped().Where("token in ?", tokens).Delete(&EntityRelationship{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
