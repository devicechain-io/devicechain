// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/limit"
)

// FacetKind names a discovery facet (ADR-045 decision 8): free-text identity/
// classification fields whose in-use values suggest completions in the authoring
// UI so a tenant's fleet vocabulary stays consistent (no "Acme" vs "ACME" drift).
type FacetKind string

const (
	FacetManufacturer FacetKind = "MANUFACTURER" // DeviceType.manufacturer
	FacetModel        FacetKind = "MODEL"        // DeviceType.model
	FacetCategory     FacetKind = "CATEGORY"     // DeviceProfile.category
)

// MaxFacetValues bounds the distinct suggestion vocabulary. SQL fetches one extra
// value to detect overflow; no partial list is served by the existing list schema.
// This bounds materialized results, not the database work needed for DISTINCT.
const MaxFacetValues = 1000

// facetSource maps a facet to the table + column its values live in. The columns
// are fixed internal constants (never user input), so interpolating them into the
// query is safe.
func facetSource(facet FacetKind) (model any, column string, err error) {
	switch facet {
	case FacetManufacturer:
		return &DeviceType{}, "manufacturer", nil
	case FacetModel:
		return &DeviceType{}, "model", nil
	case FacetCategory:
		return &DeviceProfile{}, "category", nil
	default:
		return nil, "", fmt.Errorf("unknown facet %q", facet)
	}
}

// FacetValues returns the distinct non-empty values a facet currently holds across
// the tenant's rows — the suggestion source for the free-text facet inputs. It is
// tenant-scoped by the RDB callback (DeviceType/DeviceProfile are TenantScoped) and
// so is inherently per-tenant; NULL/empty values and soft-deleted rows are excluded,
// and the list is sorted for a stable order. An unknown facet fails closed.
// More than MaxFacetValues distinct values returns a typed LIMIT_EXCEEDED error and
// no values, rather than silently truncating the suggestion list.
func (api *Api) FacetValues(ctx context.Context, facet FacetKind) ([]string, error) {
	model, column, err := facetSource(facet)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0)
	err = api.RDB.DB(ctx).Model(model).
		Where(column+" <> ''").
		Distinct().
		Order(column).
		Limit(MaxFacetValues+1).
		Pluck(column, &values).Error
	if err != nil {
		return nil, err
	}
	if len(values) > MaxFacetValues {
		return nil, limit.Exceeded("facet "+string(facet)+" distinct values", len(values), MaxFacetValues)
	}
	return values, err
}
