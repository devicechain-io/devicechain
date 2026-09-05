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
	// Location is the profile's position declaration (ADR-078), or nil for "these
	// devices do not report their own position".
	Location *LocationDeclaration
}

// DeviceProfileUpdateRequest is the three-state partial update of a device profile:
// an omitted field leaves the stored value alone, an explicit null clears it, and a
// value sets it.
//
// 🔴 IT CARRIES NO TOKEN, AND THE RENAME IT USED TO CARRY DID NOT EVAPORATE. A
// profile rename is a real capability — allowed while the profile is unused, refused
// once it is published or adopted — and it now has a mutation of its own
// (renameDeviceProfile / Api.RenameDeviceProfile) where `newToken` can mean only one
// thing. What is gone is the payload token that had to be RECONCILED against the
// argument, which is a disagreement this type cannot express rather than one it
// refuses.
type DeviceProfileUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// Category is the functional device-class facet (ADR-045 decision 8). Nullable,
	// so an explicit null withdraws the classification.
	Category dcgraphql.OptionalString
	// Metadata is an opaque JSON string in the schema, not a map, so it replaces
	// wholesale when sent and clears on null. There is no per-key merge to choose
	// between: the API has never been able to address an individual key.
	Metadata dcgraphql.OptionalString
	// Location is the position declaration (ADR-078).
	//
	// 🔴 ITS ABSENT READING CHANGED WITH THIS CONVERSION, DELIBERATELY. Under the
	// full-replace input, a request carrying no declaration CLEARED one that was
	// there — omission was the clear operation, so restating a profile without
	// carrying the declaration forward silently un-declared position for every device
	// built on it. Omitting it now PRESERVES it, and clearing is an explicit null.
	Location OptionalLocationDeclaration
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
	// LocationDeclaration is the SINGULAR, NULLABLE position declaration (ADR-078) —
	// see LocationDeclaration for why it is not a fourth definition list. It is stored
	// as one nullable JSON document rather than a spread of nullable columns for a
	// reason the design depends on: a set of columns would need a separate "declared"
	// flag to tell an undeclared profile from one declared with no stated
	// expectations, and that flag is precisely the ambiguity the singular-nullable
	// shape exists to remove. SQL NULL here IS "does not report position"; `{}` is
	// "reports position, no expectations stated". Read/written through
	// decodeLocationDeclaration / encodeLocationDeclaration, never touched raw.
	LocationDeclaration *datatypes.JSON
	// Provenance is a reserved, nullable link recording that this profile was
	// fork-adopted from an ADR-046 catalog entry ("catalog-profile@version"). Unset
	// and unused in v1; present so the future catalog drops in additively.
	Provenance sql.NullString `gorm:"size:256"`

	// ActiveVersion is the published version (ADR-045 decision 4) a device resolves
	// through this profile — the currently-live capability set. Null until the
	// profile is first published: an unpublished profile has no active capability
	// (its draft definitions are inert), the same limiting case as a type with no
	// profile. Rollback flips this pointer; publish advances it to the new version.
	ActiveVersion sql.NullInt32

	// The typed capability definitions the profile owns (ADR-045 slice b): the
	// inbound metric vocabulary (ADR-016), the outbound command vocabulary
	// (ADR-043). A device resolves these through its type's profile.
	MetricDefinitions  []MetricDefinition
	CommandDefinitions []CommandDefinition
	// DetectionRules are the profile's DETECT rules (ADR-051 / ADR-053 §5): opaque
	// authored rule documents versioned with the profile and propagated to
	// event-processing at publish (slice 4b). They are the one alarm-authoring path
	// (ADR-057) — the legacy AlarmDefinition model was torn down after the 6d cutover.
	DetectionRules []DetectionRule
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Not ordered by ActiveVersion — that names a version of THIS
// profile, so it is meaningless as a cross-row ordering, and it is nullable until the
// profile is first published.
func (DeviceProfile) DefaultOrder() string {
	return "device_profiles.created_at DESC, device_profiles.token ASC"
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
