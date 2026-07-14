// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// FacetSource discriminates where a facet's values come from (ADR-061 SD-5). It is
// the seam that lets manufacturer/model/category become first-class system facets
// later without a bolt-on; v1 only ever writes/reads "attribute".
type FacetSource string

const (
	// FacetSourceAttribute — the facet's values live in EntityAttribute rows on the
	// member entity itself (SD-1); a selector leaf lowers to an EntityAttribute
	// semi-join (SD-3). This is the only source v1 implements.
	FacetSourceAttribute FacetSource = "attribute"
	// FacetSourceSystem — a predefined, non-deletable facet whose values come from a
	// modeled column reached by relationship traversal (the ADR-045 discovery facets
	// are the first instances). Declared as a seam but NOT built in v1: it needs a
	// second value source in the lowering plus graph traversal, both deferred
	// (ADR-061 §8, spec §5). No user may create one, and it is never deletable.
	FacetSourceSystem FacetSource = "system"
)

// Valid reports whether s names a known facet source.
func (s FacetSource) Valid() bool {
	switch s {
	case FacetSourceAttribute, FacetSourceSystem:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s FacetSource) String() string {
	return string(s)
}

// FacetKey is a per-tenant declaration that a given EntityAttribute key, for a given
// member family, is a facet — a classification axis the console can offer for
// browse/filter and typeahead (ADR-061 SD-5). It does NOT store facet values: those
// remain EntityAttribute rows on the member entities (SD-1). The registry only
// declares which keys are facets, their value type, an optional value vocabulary,
// and a display label.
//
// Named FacetKey (not Facet) to avoid colliding with the ADR-045 discovery-facet
// concept (model.FacetKind / Api.FacetValues), a fixed 3-value enum over modeled
// type/profile columns that is a different feature on a different plane.
//
// A facet key is addressed by its natural key (MemberType, Key) within a tenant —
// there is no opaque token; setFacetKey upserts on that natural key, exactly like
// setEntityAttribute upserts on (entity, scope, key).
type FacetKey struct {
	gorm.Model
	rdb.TenantScoped

	// MemberType is the entity family this facet classifies (device|asset|area|
	// customer), one of entity.Type; validated on write.
	MemberType string `gorm:"not null;size:32;index"`
	// Key is the EntityAttribute key this facet declares (e.g. "climate", "country").
	// The DB column is attr_key — it names an EntityAttribute.AttrKey, and avoids the
	// "key" reserved-word footgun.
	Key string `gorm:"column:attr_key;not null;size:128"`
	// ValueType is one of AttributeValueType (STRING/LONG/DOUBLE/BOOLEAN/JSON) — the
	// type the facet's attribute values carry; it pins the value predicate the G3
	// lowering emits.
	ValueType string `gorm:"not null;size:16"`
	// Source is one of FacetSource; v1 only ever stores "attribute" (SD-5 seam).
	Source string `gorm:"not null;size:16;default:attribute"`
	// Values is an optional value vocabulary (a JSON array of strings) that powers
	// the console typeahead. Nil when the facet's values are free-form.
	Values *datatypes.JSON
	// Label is an optional human display label for the axis; nil falls back to Key.
	Label sql.NullString
}

// FacetKeySetRequest is the data to declare (upsert) a facet key. The natural key
// (MemberType, Key) selects the row; the remaining fields are (re)written.
type FacetKeySetRequest struct {
	MemberType string
	Key        string
	ValueType  string
	Values     *[]string // optional value vocabulary; nil clears it
	Label      *string
}

// FacetKeySearchCriteria locates facet keys, optionally filtered to one member
// family (the console axis picker asks per family).
type FacetKeySearchCriteria struct {
	rdb.Pagination
	MemberType *string
}

// FacetKeySearchResults are the paged results of a facet-key search.
type FacetKeySearchResults struct {
	Results    []FacetKey
	Pagination rdb.SearchResultsPagination
}
