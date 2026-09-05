// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// DefaultOrder implements rdb.Sortable. Newest-first matches how a dashboard list is
// read — the one you just created is the one you are looking for — and created_at is
// stable for a dashboard even as its draft definition is edited, so the list does not
// reshuffle under an author who is saving.
//
// created_at is not unique (a seeded install creates several in one tick), so token ASC
// makes the order total. Both columns are NOT NULL (gorm.Model, rdb.TokenReference), so
// there are no NULLs to place.
func (Dashboard) DefaultOrder() string {
	return "dashboards.created_at DESC, dashboards.token ASC"
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

// DashboardCreateRequest is the data required to create a dashboard.
// Definition is the raw JSON definition document (validated by the API layer).
type DashboardCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Definition  string
}

// DashboardUpdateRequest is the data an UPDATE carries, and it is a different type from
// the create request rather than the same one reused — which is the whole conversion.
//
// Every field is three-state (dcgraphql.Optional*): absent leaves the stored value
// alone, an explicit null clears it, a value sets it. A shared create input can express
// only two of those, so every update through it was a full replace — a caller renaming a
// dashboard erased its description, successfully.
//
// 🔴 THERE IS NO Token FIELD, and its absence is the point. The mutation's `token`
// argument names the dashboard; the create input carried a second one, which could only
// agree with the argument or name a different record. That disagreement used to be
// REFUSED (dcgraphql.ErrPayloadTokenDisagrees) after the payload token had already been
// written onto the row in an earlier incarnation, where an empty one — legal, since
// `token: String!` admits "" — blanked the dashboard's token and left it addressable by
// nothing. Dropping the field makes the whole class unrepresentable rather than caught.
//
// Nothing is lost by dropping it, because unlike a connector, a notification channel or
// an AI provider, a dashboard has no rename channel: nothing keys a secret or a policy
// by its token, and no test ever pinned a rename as intended.
//
// 🔴 Definition IS THREE-STATE ON THE WIRE AND UNCLEARABLE IN THE FOLD. The column is
// NOT NULL — a dashboard definition is opaque versioned JSON and a dashboard with no
// definition is not a thing — so UpdateDashboard folds it with ApplyToRequired, which
// REFUSES an explicit null instead of writing an empty document. It is still an
// OptionalString rather than a plain string because the ABSENT state is what "leave the
// definition alone while I rename this" is spelled with, and a plain string cannot spell
// it.
type DashboardUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	Definition  dcgraphql.OptionalString
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
