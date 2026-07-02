// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Dashboard is a persisted dashboard definition (ADR-039). It is a plain
// tenant-scoped, token-referenced entity — one row per dashboard — NOT a
// hypertable. The layout, widgets, and datasource selectors live inside the
// Definition JSON document, which the backend stores opaquely (validated only as
// well-formed JSON carrying a schemaVersion); the canonical shape is owned by the
// @devicechain/dashboards TypeScript types, keeping the document fluid pre-GA.
type Dashboard struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	Definition datatypes.JSON `gorm:"not null"`
}

// DashboardCreateRequest is the data required to create or update a dashboard.
// Definition is the raw JSON definition document (validated by the API layer).
type DashboardCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Definition  string
}

// DashboardSearchCriteria is the filter/pagination for a dashboard search.
type DashboardSearchCriteria struct {
	rdb.Pagination
	Name *string
}

// DashboardSearchResults is a page of dashboards plus its pagination info.
type DashboardSearchResults struct {
	Results    []Dashboard
	Pagination rdb.SearchResultsPagination
}
