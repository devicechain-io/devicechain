// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// GraphQL representation of the audit-event search criteria input.
type AuditEventSearchCriteriaInput struct {
	PageNumber int32
	PageSize   int32
	StartTime  *string
	EndTime    *string
	Category   *string
	Operation  *string
	Actor      *string
	TableName  *string
	EntityPk   *string
}

// Convert a GraphQL audit criteria input into the core search criteria.
func toAuditEventSearchCriteria(in AuditEventSearchCriteriaInput) (rdb.AuditEventSearchCriteria, error) {
	criteria := rdb.AuditEventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: in.PageNumber, PageSize: in.PageSize},
		Category:   in.Category,
		Operation:  in.Operation,
		Actor:      in.Actor,
		TableName:  in.TableName,
		EntityPK:   in.EntityPk,
	}
	if in.StartTime != nil {
		t, err := time.Parse(time.RFC3339, *in.StartTime)
		if err != nil {
			return criteria, err
		}
		criteria.StartTime = &t
	}
	if in.EndTime != nil {
		t, err := time.Parse(time.RFC3339, *in.EndTime)
		if err != nil {
			return criteria, err
		}
		criteria.EndTime = &t
	}
	return criteria, nil
}

// Query the append-only audit journal (ADR-019). Tenant-scoped and gated by the
// audit:read authority; returns rows newest first. The read itself is the
// core-owned RdbManager.AuditEvents helper — this resolver only adds the GraphQL
// surface and the authority gate for device-management's journal.
func (r *SchemaResolver) AuditEvents(ctx context.Context, args struct {
	Criteria AuditEventSearchCriteriaInput
}) (*AuditEventSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AuditRead); err != nil {
		return nil, err
	}

	criteria, err := toAuditEventSearchCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).RDB.AuditEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &AuditEventSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
