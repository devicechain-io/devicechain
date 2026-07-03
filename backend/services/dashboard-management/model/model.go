// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

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

// DashboardVersion is an immutable, published SNAPSHOT of a dashboard's definition
// (ADR-039 versioning). The mutable working copy is the parent Dashboard row (the
// "draft"); publishing freezes the draft into a new version (N+1). History is
// append-only — rollback re-drafts a snapshot into the parent, it never deletes a
// version. There is no token: a version is addressed by its parent + monotonic
// integer, so it embeds TenantScoped (for isolation) but NOT TokenReference. The
// publish timestamp is the row's CreatedAt (a version is created when published).
type DashboardVersion struct {
	gorm.Model
	rdb.TenantScoped
	// Parent dashboard + monotonic-per-dashboard version number; unique together so
	// two concurrent publishes can't mint the same version (the loser's insert fails).
	DashboardID uint  `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:1"`
	Version     int32 `gorm:"not null;uniqueIndex:uix_dashboard_versions_dashboard_version,priority:2"`
	// User-supplied label/description for the version (MAY embed a semver string; the
	// platform does not parse it). Optional.
	Label       sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
	// The full definition snapshot at publish time (stored opaquely, like Dashboard).
	Definition datatypes.JSON `gorm:"not null"`
	// The identity that published this version (claims username, falling back to email).
	PublishedBy string `gorm:"size:256"`
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
