// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------
// Audit event resolver
// --------------------

type AuditEventResolver struct {
	M rdb.AuditEvent
	S *SchemaResolver
	C context.Context
}

func (r *AuditEventResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

// OccurredTime is the commit time of the audited statement; it is always present.
func (r *AuditEventResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

func (r *AuditEventResolver) Category() string {
	return r.M.Category
}

func (r *AuditEventResolver) Actor() string {
	return r.M.Actor
}

func (r *AuditEventResolver) Operation() string {
	return r.M.Operation
}

// TableName is null for an auth event (which mutates no table).
func (r *AuditEventResolver) TableName() *string {
	if r.M.TableName == "" {
		return nil
	}
	return &r.M.TableName
}

// EntityPk is null for a bulk/condition mutation (rowsAffected then conveys the
// scope) and for auth events.
func (r *AuditEventResolver) EntityPk() *string {
	if r.M.EntityPK == "" {
		return nil
	}
	return &r.M.EntityPK
}

func (r *AuditEventResolver) RowsAffected() int32 {
	return int32(r.M.RowsAffected)
}

type AuditEventSearchResultsResolver struct {
	M rdb.AuditEventSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AuditEventSearchResultsResolver) Results() []*AuditEventResolver {
	resolvers := make([]*AuditEventResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &AuditEventResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *AuditEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
