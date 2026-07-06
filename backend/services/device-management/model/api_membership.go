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

// Reserved relationship-type tokens, auto-provisioned per tenant on first use
// rather than seeded at startup: relationship types are tenant-scoped, and a
// tenant-agnostic service has no tenant roster to seed against at boot (and new
// tenants would be missed anyway).
//
//   - "member"  — group <-> member edges (untracked; organizational only).
//   - "assigned" — the built-in device-assignment edge (tracked). Being tracked,
//     the primary assignment is denormalized onto a device's events as their
//     anchor (ADR-013 addendum 2026-07-01), so assigning a device gives its
//     telemetry a customer/area/asset context. Unassigned devices still emit —
//     resolution is anchorless, not dropped.
const (
	MembershipRelationshipType = "member"
	AssignmentRelationshipType = "assigned"
)

// EnsureMembershipType returns the reserved membership type (Tracked=false) for
// the caller's tenant, creating it on first use.
func (api *Api) EnsureMembershipType(ctx context.Context) (*EntityRelationshipType, error) {
	return api.ensureReservedType(ctx, MembershipRelationshipType, "Member",
		"Built-in group-membership relationship.", false)
}

// EnsureAssignmentType returns the reserved device-assignment type (Tracked=true)
// for the caller's tenant, creating it on first use.
func (api *Api) EnsureAssignmentType(ctx context.Context) (*EntityRelationshipType, error) {
	return api.ensureReservedType(ctx, AssignmentRelationshipType, "Assigned",
		"Built-in device-assignment relationship (tracked).", true)
}

// ensureReservedTypeByToken auto-provisions a reserved relationship type if the
// token names one, returning nil for any other (caller-defined) token so it falls
// through to a normal lookup.
func (api *Api) ensureReservedTypeByToken(ctx context.Context, token string) (*EntityRelationshipType, error) {
	switch token {
	case MembershipRelationshipType:
		return api.EnsureMembershipType(ctx)
	case AssignmentRelationshipType:
		return api.EnsureAssignmentType(ctx)
	default:
		return nil, nil
	}
}

// ensureReservedType returns the reserved relationship type for the caller's
// tenant, creating it with the given tracked flag on first use. Idempotent and
// safe under concurrent callers via an ON CONFLICT DO NOTHING insert + read-back.
func (api *Api) ensureReservedType(ctx context.Context, token, name, description string,
	tracked bool) (*EntityRelationshipType, error) {
	if matches, err := api.EntityRelationshipTypesByToken(ctx, []string{token}); err != nil {
		return nil, err
	} else if len(matches) > 0 {
		return matches[0], nil
	}

	created := &EntityRelationshipType{
		TokenReference: rdb.TokenReference{Token: token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(&name),
			Description: rdb.NullStrOf(&description),
		},
		Tracked: tracked,
	}
	if err := api.RDB.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(created).Error; err != nil {
		return nil, err
	}
	if created.ID != 0 {
		return created, nil
	}
	// Lost the create race (token already existed) — read back the winner.
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("reserved relationship type %q missing after ensure", token)
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

	// Resolve each distinct relationship type once up front — a bulk add of N
	// edges of the same type does one lookup, not N. The reserved types (member,
	// assigned) are auto-provisioned on first use and seeded into the map.
	typesByToken := make(map[string]*EntityRelationshipType)
	plainTokens := make([]string, 0)
	for _, request := range requests {
		if _, ok := typesByToken[request.RelationshipType]; ok {
			continue
		}
		if reserved, err := api.ensureReservedTypeByToken(ctx, request.RelationshipType); err != nil {
			return nil, err
		} else if reserved != nil {
			typesByToken[request.RelationshipType] = reserved
			continue
		}
		typesByToken[request.RelationshipType] = nil // mark as needing a lookup
		plainTokens = append(plainTokens, request.RelationshipType)
	}
	if len(plainTokens) > 0 {
		matches, err := api.EntityRelationshipTypesByToken(ctx, plainTokens)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			typesByToken[m.Token] = m
		}
	}

	created := make([]*EntityRelationship, 0, len(requests))
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, request := range requests {
			rt := typesByToken[request.RelationshipType]
			if rt == nil {
				return gorm.ErrRecordNotFound
			}
			sourceId, err := api.ResolveEntityToken(ctx, request.SourceType, request.Source)
			if err != nil {
				return fmt.Errorf("source: %w", err)
			}
			targetId, err := api.ResolveEntityToken(ctx, request.TargetType, request.Target)
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
			edge := &EntityRelationship{
				TokenReference:     rdb.TokenReference{Token: request.Token},
				MetadataEntity:     rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
				SourceType:         request.SourceType,
				SourceId:           sourceId,
				TargetType:         request.TargetType,
				TargetId:           targetId,
				TargetToken:        request.Target,
				RelationshipTypeId: rt.ID,
			}
			if err := tx.Create(edge).Error; err != nil {
				return err
			}
			edge.RelationshipType = *rt
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
	// Capture the source devices before the edges are gone so their cached tracked
	// sets can be evicted (ADR-044 F2); the targets survive, so the sweep won't
	// repair a stale set here.
	sources := api.relationshipSourceDevices(ctx, "token in ?", tokens)
	result := api.RDB.DB(ctx).Unscoped().Where("token in ?", tokens).Delete(&EntityRelationship{})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		api.evictRelationshipSources(ctx, sources)
	}
	return result.RowsAffected > 0, nil
}
