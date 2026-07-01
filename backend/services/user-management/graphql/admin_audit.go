// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// AdminAuditEventResolver resolves the AdminAuditEvent type from a core
// rdb.AuditEvent row.
type AdminAuditEventResolver struct {
	M rdb.AuditEvent
}

func (r *AdminAuditEventResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.ID)) }

// OccurredTime is the commit time of the audited statement; always present.
func (r *AdminAuditEventResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

func (r *AdminAuditEventResolver) Category() string  { return r.M.Category }
func (r *AdminAuditEventResolver) Actor() string     { return r.M.Actor }
func (r *AdminAuditEventResolver) Operation() string { return r.M.Operation }

// Tenant is null for a tenant-less system/auth event (e.g. a login, which
// happens before a tenant is selected).
func (r *AdminAuditEventResolver) Tenant() *string {
	if r.M.TenantId == "" {
		return nil
	}
	return &r.M.TenantId
}

// TableName is null for an auth event (which mutates no table).
func (r *AdminAuditEventResolver) TableName() *string {
	if r.M.TableName == "" {
		return nil
	}
	return &r.M.TableName
}

// EntityPk is null for a bulk/condition mutation and for auth events.
func (r *AdminAuditEventResolver) EntityPk() *string {
	if r.M.EntityPK == "" {
		return nil
	}
	return &r.M.EntityPK
}

// EntityLabel is a human-facing identifier of the affected row (a role/tenant
// token or an identity email), null when the model contributed none.
func (r *AdminAuditEventResolver) EntityLabel() *string {
	if r.M.EntityLabel == "" {
		return nil
	}
	return &r.M.EntityLabel
}

func (r *AdminAuditEventResolver) RowsAffected() int32 { return int32(r.M.RowsAffected) }

// SearchResultsPaginationResolver resolves the shared pagination shape.
type SearchResultsPaginationResolver struct {
	M rdb.SearchResultsPagination
}

func (r *SearchResultsPaginationResolver) PageStart() *int32    { v := r.M.PageStart; return &v }
func (r *SearchResultsPaginationResolver) PageEnd() *int32      { v := r.M.PageEnd; return &v }
func (r *SearchResultsPaginationResolver) TotalRecords() *int32 { v := r.M.TotalRecords; return &v }

// AdminAuditEventSearchResultsResolver wraps a page of audit rows.
type AdminAuditEventSearchResultsResolver struct {
	M rdb.AuditEventSearchResults
}

func (r *AdminAuditEventSearchResultsResolver) Results() []*AdminAuditEventResolver {
	out := make([]*AdminAuditEventResolver, 0, len(r.M.Results))
	for i := range r.M.Results {
		out = append(out, &AdminAuditEventResolver{M: r.M.Results[i]})
	}
	return out
}

func (r *AdminAuditEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination}
}

// GraphQL representation of the admin audit-event search criteria input.
type AdminAuditEventSearchCriteriaInput struct {
	PageNumber int32
	PageSize   int32
	StartTime  *string
	EndTime    *string
	Category   *string
	Operation  *string
	Actor      *string
	Tenant     *string
}

// Convert a GraphQL admin audit criteria input into the core search criteria.
func toAdminAuditCriteria(in AdminAuditEventSearchCriteriaInput) (rdb.AuditEventSearchCriteria, error) {
	criteria := rdb.AuditEventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: in.PageNumber, PageSize: in.PageSize},
		Category:   in.Category,
		Operation:  in.Operation,
		Actor:      in.Actor,
		Tenant:     in.Tenant,
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

// AuditEvents queries the instance's user-management audit journal (auth events +
// identity/role/tenant administration), newest first. Requires the audit:read
// authority; superusers hold "*". Instance-wide (cross-tenant) via the admin
// service's system-context read.
func (r *AdminResolver) AuditEvents(ctx context.Context, args struct {
	Criteria AdminAuditEventSearchCriteriaInput
}) (*AdminAuditEventSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AuditRead); err != nil {
		return nil, err
	}

	criteria, err := toAdminAuditCriteria(args.Criteria)
	if err != nil {
		return nil, err
	}
	found, err := r.getAdminService(ctx).AuditEvents(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &AdminAuditEventSearchResultsResolver{M: *found}, nil
}
