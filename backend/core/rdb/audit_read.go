// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// AuditEventSearchCriteria selects rows from the append-only audit journal
// (ADR-019). The journal is tenant-scoped by construction, so a read is
// automatically restricted to the caller's tenant; these filters narrow within
// it. Every filter is optional except pagination — a bounded time range is
// recommended for a busy tenant.
type AuditEventSearchCriteria struct {
	Pagination
	StartTime *time.Time
	EndTime   *time.Time
	Category  *string // "mutation" | "auth"
	Operation *string // create/update/delete | login/login_failed/refresh
	Actor     *string
	TableName *string
	EntityPK  *string
}

// AuditEventSearchResults is a page of audit-journal rows, newest first.
type AuditEventSearchResults struct {
	Results    []AuditEvent
	Pagination SearchResultsPagination
}

// AuditEvents returns a page of audit-journal rows matching the criteria,
// ordered newest-first. The read runs through the tenant-scoped RDB, so the
// fail-closed tenant predicate (ADR-015) restricts results to the caller's
// tenant, and the (tenant_id, occurred_time DESC) journal index backs the order.
//
// The journal is core-owned: this package registers the write callback and
// migrates the audit_events table into every service's schema (see audit.go /
// postgres.go), so this one read helper serves any service that wants to expose
// its journal — the service need only add a GraphQL surface over it.
func (rdb *RdbManager) AuditEvents(ctx context.Context, criteria AuditEventSearchCriteria) (*AuditEventSearchResults, error) {
	results := make([]AuditEvent, 0)
	db, pag := rdb.ListOf(ctx, &AuditEvent{}, func(result *gorm.DB) *gorm.DB {
		if criteria.StartTime != nil {
			result = result.Where("occurred_time >= ?", *criteria.StartTime)
		}
		if criteria.EndTime != nil {
			result = result.Where("occurred_time <= ?", *criteria.EndTime)
		}
		if criteria.Category != nil {
			result = result.Where("category = ?", *criteria.Category)
		}
		if criteria.Operation != nil {
			result = result.Where("operation = ?", *criteria.Operation)
		}
		if criteria.Actor != nil {
			result = result.Where("actor = ?", *criteria.Actor)
		}
		if criteria.TableName != nil {
			result = result.Where("table_name = ?", *criteria.TableName)
		}
		if criteria.EntityPK != nil {
			result = result.Where("entity_pk = ?", *criteria.EntityPK)
		}
		return result.Order("occurred_time DESC")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AuditEventSearchResults{Results: results, Pagination: pag}, nil
}
