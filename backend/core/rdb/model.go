// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql"
	"encoding/json"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// TenantScoped marks an entity as tenant-owned. Entities embed this type so
// the global GORM tenant-scope callbacks (see tenant_scope.go) can detect the
// TenantId field and enforce row-level isolation. The tenant id is the stable
// tenant token from the CRD / messaging subject.
type TenantScoped struct {
	TenantId string `gorm:"index;not null;size:128"`
}

// Entity that is referenced by a token which may change over time. Uniqueness is
// NOT declared here: a per-tenant partial unique index (ADR-042 P1) is created by
// each service's migration via rdb.CreateTenantTokenIndex — token is unique within
// a tenant among live (non-soft-deleted) rows, so tenants never collide and a
// deleted token frees for reuse. A bare global UNIQUE(token) would do neither.
type TokenReference struct {
	Token string `gorm:"index;not null;size:128"`
}

// AuditLabel implements AuditLabeler: the token is the human-facing, non-sensitive
// identifier for any token-referenced entity, so the audit journal records it
// alongside the (table, pk) reference. Every entity that embeds TokenReference
// (the registry families) inherits this via promotion.
func (t TokenReference) AuditLabel() string { return t.Token }

// Entity that has a name and description.
type NamedEntity struct {
	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
}

// Entity that has branding information.
type BrandedEntity struct {
	ImageUrl        sql.NullString `gorm:"size:512"`
	Icon            sql.NullString `gorm:"size:128"`
	BackgroundColor sql.NullString `gorm:"size:32"`
	ForegroundColor sql.NullString `gorm:"size:32"`
	BorderColor     sql.NullString `gorm:"size:32"`
}

// Entity that has extra attached metadata.
type MetadataEntity struct {
	Metadata *datatypes.JSON
}

// Create JSON value from string input.
func MetadataStrOf(value *string) *datatypes.JSON {
	if value != nil {
		result := json.RawMessage{}
		err := result.UnmarshalJSON([]byte(*value))
		if err != nil {
			return nil
		}
		conv := datatypes.JSON(result)
		return &conv
	}
	return nil
}

// Creates a sql.NullString from a string constant.
func NullStrOf(value *string) sql.NullString {
	if value != nil {
		trimmed := strings.TrimSpace(*value)
		if len(trimmed) > 0 {
			return sql.NullString{
				String: trimmed,
				Valid:  true,
			}
		}
	}
	return sql.NullString{
		Valid: false,
	}
}

// Creates a sql.NullInt64 from a string constant.
func NullInt64Of(value *int64) sql.NullInt64 {
	if value != nil {
		return sql.NullInt64{
			Int64: *value,
			Valid: true,
		}
	} else {
		return sql.NullInt64{
			Valid: false,
		}
	}
}

// Creates a sql.NullFloat64 from a string constant.
func NullFloat64Of(value *float64) sql.NullFloat64 {
	if value != nil {
		return sql.NullFloat64{
			Float64: *value,
			Valid:   true,
		}
	} else {
		return sql.NullFloat64{
			Valid: false,
		}
	}
}

// Page-size bounds applied to every list query routed through Paginate/ListOf
// (ADR-029). A request that names no size falls back to DefaultPageSize; anything
// above MaxPageSize is clamped. Without this a `pageSize:0` (or a huge value) from
// external GraphQL is an unbounded scan into one pod's heap — a trivial
// single-credential DoS.
const (
	DefaultPageSize = 100
	MaxPageSize     = 1000
)

// Information for paged result sets
type Pagination struct {
	PageNumber int32
	PageSize   int32
	// Unbounded requests every matching row with no LIMIT. It is for internal
	// callers that genuinely need the full set (e.g. resolving a device's tracked
	// relationships); the external GraphQL inputs map only PageNumber/PageSize, so
	// an untrusted client can never set this and can never request a full scan.
	Unbounded bool
}

// EffectivePageSize resolves the page size actually applied (ADR-029): below 1
// falls back to DefaultPageSize, above MaxPageSize is clamped. Unbounded is a
// separate no-LIMIT path and is not reflected here. ListOf uses this so its
// reported PageStart/PageEnd match the LIMIT Paginate applied.
func (pag Pagination) EffectivePageSize() int32 {
	if pag.PageSize < 1 {
		return DefaultPageSize
	}
	if pag.PageSize > MaxPageSize {
		return MaxPageSize
	}
	return pag.PageSize
}

// Scope function used to implement pagination.
func Paginate(pag Pagination) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		// Explicit internal all-rows path (never reachable from external input).
		if pag.Unbounded {
			return db
		}
		size := pag.EffectivePageSize()
		offset := (pag.PageNumber - 1) * size
		return db.Offset(int(offset)).Limit(int(size))
	}
}

// Pagination info included with search results.
type SearchResultsPagination struct {
	PageStart    int32
	PageEnd      int32
	TotalRecords int32
}
