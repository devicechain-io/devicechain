// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// AuditLabel implements AuditLabeler: the token is the human-facing identifier for
// any token-referenced entity, so the audit journal records it alongside the
// (table, pk) reference. Every entity that embeds TokenReference (the registry
// families) inherits this via promotion.
//
// 🔴 THAT PROMOTION IS WHY THE JOURNAL'S LABELS ARE PERSONAL DATA, and the count is
// the argument: this one method puts a customer-chosen token into entity_label for
// every model that embeds TokenReference — devices, assets, areas, geofences,
// commands, dashboards, connectors, and customers, whose tokens are routinely a
// person's or a company's name. Nothing constrains them beyond the token grammar.
// The column is emptied for a purged tenant (see rdb.AuditEvent); do not re-describe
// it as non-sensitive on the strength of it not being a credential.
func (t TokenReference) AuditLabel() string { return t.Token }

// Entity that carries an optional customer-owned external/business identifier
// (ADR-049) — a VIN, serial, GS1 code, asset tag — distinct from the token. Unlike
// the token it is opaque (no NATS/MQTT addressing grammar), not a credential, and
// nullable; it exists only to be looked up by. Per-tenant uniqueness among live
// rows WITH an id present is a partial unique index created by each service's
// migration via rdb.CreateTenantExternalIdIndex (the token analog, ADR-042 P1).
type ExternalReference struct {
	ExternalId sql.NullString `gorm:"index;size:256"`
}

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

// JSONInputOf creates a JSON column value from a string an API CALLER supplied,
// refusing the write when that string is not valid JSON. field names the request
// field in the error, because a request can carry several of these — metadata,
// payload, config, parameterSchema, enum, recipients — and "invalid JSON" without
// a field name leaves the caller guessing which one.
//
// 🔴 IT RETURNS AN ERROR BECAUSE THE TWO EARLIER SHAPES WERE BOTH SILENT, IN
// OPPOSITE DIRECTIONS. The original guard checked the error from
// json.RawMessage.UnmarshalJSON, which only copies its input — it returns an error
// solely on a nil receiver, never for malformed JSON. So every invalid value sailed
// through and became a JSON column write that Postgres rejected at execution time:
//
//	ERROR: invalid input syntax for type json (SQLSTATE 22P02)
//
// That was found end to end, not by reading: a device answered a command with the
// plain text "acknowledged by livedevice", which left the UPDATE failing, the command
// stuck in SENT, and the redelivery sweep retrying the same doomed write once a minute
// forever. Swapping in json.Valid stopped the doomed write — and replaced it with a
// worse failure, because returning nil for a malformed value does not mean "reject",
// it means "write NULL". An update carrying one bad field therefore ERASED whatever
// that column already held and answered 200. Loud breakage became silent data loss:
// the caller is told the write succeeded, and the value they did not mean to touch is
// gone with nothing to indicate it.
//
// 🔴 PARTIAL-UPDATE SEMANTICS DO NOT SUBSUME THIS, AND THAT WAS CHECKED RATHER THAN
// ASSUMED. An Optional* field folds three states — omitted keeps, null clears, a value
// sets — and hands down a *string. A malformed value is a VALUE, so it arrives here as
// the third state and, under the old shape, silently produced the second. It is a
// fourth outcome collapsing onto one of the three, inside the mechanism whose entire
// purpose is keeping them distinct. Refusal is the fourth outcome.
//
// 🔴 THIS IS THE LAST LAYER, NOT THE ONLY ONE, AND THE DIFFERENCE IS WORTH STATING because
// an earlier draft of this comment claimed the latter and it was false. Several fields
// reach a JSON column through a guard that runs BEFORE this and refuses more than
// json.Valid does: command-delivery checks payload and metadata with a coded rejection,
// notification-management requires config and metadata to be JSON OBJECTS, a command
// definition's parameterSchema has its own validator, and detection-rule definitions,
// authoring graphs and fence geometries are each parsed by the thing that will later read
// them. Those are stronger and they win; behind them this function cannot fire. What it
// covers is everything else — and "everything else" was, until it returned an error, a
// silent write of NULL over whatever the column already held.
//
// An absent value (nil), or one that is empty or whitespace, is NOT an error — it is
// "no value", the same reading NullStrOf gives, and it clears the column exactly as it
// did before. Clearing is spelled with an explicit null or an empty string; refusing
// those would be a second behaviour change riding along with this one, affecting
// callers who were never doing anything wrong.
//
// Compare JSONTextOf below, which is the DEVICE-supplied counterpart and deliberately
// never refuses. The split is by who wrote the value, which is why the two now have
// names that say so.
func JSONInputOf(field string, value *string) (*datatypes.JSON, error) {
	if value == nil {
		return nil, nil
	}
	if strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	if !json.Valid([]byte(*value)) {
		return nil, fmt.Errorf("%s must be valid JSON", field)
	}

	conv := datatypes.JSON(json.RawMessage(*value))
	return &conv, nil
}

// JSONTextOf creates a JSON column value from a string that is NOT required to be
// JSON: a valid JSON document is stored as-is, and anything else is stored as a JSON
// string, losslessly.
//
// It exists for values supplied by a DEVICE rather than by an API caller. A device
// reporting "bucket raised" is answering correctly, and the alternative readings are
// both worse than encoding it: dropping the payload discards what the device said,
// and rejecting it strands a command that was in fact carried out. An API caller who
// sends malformed JSON is told so, by JSONInputOf above — a sentence this comment
// asserted for some time while JSONInputOf's predecessor did no such thing, which is
// how the silent erasure survived review. The two are counterparts, not duplicates,
// and the difference is who wrote the value rather than what it looks like.
func JSONTextOf(value *string) *datatypes.JSON {
	if value == nil {
		return nil
	}
	if json.Valid([]byte(*value)) {
		conv := datatypes.JSON(json.RawMessage(*value))
		return &conv
	}

	// Marshal cannot fail for a string: any Go string encodes, with invalid UTF-8
	// replaced by U+FFFD rather than erroring.
	encoded, err := json.Marshal(*value)
	if err != nil {
		return nil
	}
	conv := datatypes.JSON(encoded)
	return &conv
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
		// int64 so a large PageNumber (up to the GraphQL Int max) can't overflow the
		// offset and wrap back to an early page; a past-the-end offset just yields an
		// empty page, and the LIMIT is always applied.
		offset := (int64(pag.PageNumber) - 1) * int64(size)
		if offset < 0 {
			offset = 0
		}
		return db.Offset(int(offset)).Limit(int(size))
	}
}

// Pagination info included with search results.
type SearchResultsPagination struct {
	PageStart    int32
	PageEnd      int32
	TotalRecords int32
}
