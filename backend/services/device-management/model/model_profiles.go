// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Data required to create a device profile.
type DeviceProfileCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	// Category is the functional device-class facet (ADR-045 decision 8) — e.g.
	// thermostat / meter / gateway / tracker. Free-text; a curatable suggestion
	// list backs the authoring UI later, and the ADR-046 catalog can supply the
	// vocabulary without a field migration.
	Category *string
	Metadata *string
}

// Represents a device profile (ADR-045): the reusable, tenant-scoped capability
// contract a device type adopts. It is the home for the typed metric, command,
// and alarm definitions (those relocate onto it in a later slice); today it is the
// distinct entity a DeviceType references, so many types can share one contract.
type DeviceProfile struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	// Category is a capability facet (ADR-045 decision 8): the functional class of
	// device this profile describes. Coherent under sharing (a shared profile is
	// shared because the devices are functionally the same).
	Category sql.NullString `gorm:"size:64;index"`
	// Provenance is a reserved, nullable link recording that this profile was
	// fork-adopted from an ADR-046 catalog entry ("catalog-profile@version"). Unset
	// and unused in v1; present so the future catalog drops in additively.
	Provenance sql.NullString `gorm:"size:256"`
}

// Search criteria for locating device profiles. Pagination-only for now; the
// category (and type manufacturer/model) facet filters that the indexed columns
// anticipate land with the authoring UI (ADR-045 slice d), alongside the
// suggestion-list wiring.
type DeviceProfileSearchCriteria struct {
	rdb.Pagination
}

// Results for device profile search.
type DeviceProfileSearchResults struct {
	Results    []DeviceProfile
	Pagination rdb.SearchResultsPagination
}
