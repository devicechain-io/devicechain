// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// DetectionRule is a DETECT rule declared on a DeviceProfile (ADR-051 / ADR-053 §5 /
// ADR-045), structurally parallel to MetricDefinition, CommandDefinition, and
// AlarmDefinition. Hanging the rule off the profile keeps detection config travelling
// with the versioned fleet definition (draft/publish/rollback) rather than living as a
// free-floating tenant resource: a device resolves its rules through its type's profile,
// read-free, because a resolved event already carries the device's active
// "{profileToken}@{version}" token (denormalized at resolution, ADR-051 slice 1).
//
// Definition is the OPAQUE authored rule document — the event-processing `rules.Rule`
// JSON (its v1 taxonomy: threshold / deltaRate / repeating / duration / absence /
// aggregate / correlation, plus the leaf comparison or raw CEL). Device-management
// stores and versions it but deliberately does NOT parse the taxonomy: the detection
// schema and its cel-go compilation are single-homed in event-processing (the dependency
// direction is event-processing → device-management, never the reverse, and the schema
// lives in that module's internal package). Device-management validates only that the
// blob is well-formed JSON on write; the authoritative type/cost/injection validation is
// the SYNCHRONOUS compile event-processing performs at publish (ADR-044 validate-
// invariants-sync), which fails a publish closed if any ENABLED draft rule does not compile
// (a disabled rule is inert and rides the version un-gated; see Enabled below). The
// runtime rule id ("{tenant}/{profileVersionToken}/{token}") is composed when the
// published-rule fact is emitted (ADR-051 slice 4b), so it is NOT part of the stored blob.
type DetectionRule struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceProfileId uint
	DeviceProfile   *DeviceProfile

	// Definition is the opaque rules.Rule JSON document (required). It is stored whole
	// and never SQL-built; event-processing decodes it strictly at publish/compile.
	Definition datatypes.JSON `gorm:"not null"`

	// Enabled — a disabled rule is retained so an author can park a rule without deleting
	// it. The publish snapshot deliberately captures disabled rules too (buildProfileSnapshot
	// does NOT filter): the flag travels frozen with the version, so a rollback restores the
	// exact enabled/disabled set. The runtime skip is at the propagation boundary — the
	// published-rule fact emitter (ADR-051 slice 4b) does not feed a disabled rule into the
	// DETECT engine's active set. Do not read "disabled" as "absent from the snapshot".
	Enabled bool
}

// Data required to create a detection rule. Definition is the rules.Rule JSON document
// as a string; it is checked for JSON well-formedness on create/update and compiled
// (the authoritative validation) by event-processing at publish.
type DetectionRuleCreateRequest struct {
	Token              string
	DeviceProfileToken string
	Name               *string
	Description        *string
	Definition         string
	Enabled            bool
	Metadata           *string
}

// Search criteria for locating detection rules.
type DetectionRuleSearchCriteria struct {
	rdb.Pagination
	DeviceProfile *string // device profile token
}

// Results for detection rule search.
type DetectionRuleSearchResults struct {
	Results    []DetectionRule
	Pagination rdb.SearchResultsPagination
}
