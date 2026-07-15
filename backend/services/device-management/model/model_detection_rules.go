// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// DetectionRule is a DETECT rule declared on a DeviceProfile (ADR-051 / ADR-053 §5 /
// ADR-045), structurally parallel to MetricDefinition and CommandDefinition. Hanging
// the rule off the profile keeps detection config travelling
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

	// AuthoringGraph is the OPAQUE visual-canvas document (ADR-053 / slice 9b) — the
	// CanvasDefinition JSON the console editor round-trips so a canvas-authored rule re-opens
	// on the canvas. It is authoring metadata ONLY: device-management neither parses it (the
	// graph schema is single-homed in event-processing, ADR-045 discipline) nor freezes it into
	// the publish snapshot (json:"-" keeps it out of the snapshot marshal — the runtime consumes
	// only Definition), and it is nullable: absent ⇒ the rule was form-authored and the canvas
	// synthesizes a single-condition graph from Definition when opened. The console derives the
	// authoritative Definition from this graph server-side (compileCanvas) before saving, so the
	// two are kept coherent by the editor, not by a cross-field check here.
	AuthoringGraph datatypes.JSON `json:"-"`

	// Enabled — a disabled rule is retained so an author can park a rule without deleting
	// it. The publish snapshot deliberately captures disabled rules too (buildProfileSnapshot
	// does NOT filter): the flag travels frozen with the version, so a rollback restores the
	// exact enabled/disabled set. The runtime skip is at the propagation boundary — the
	// published-rule fact emitter (ADR-051 slice 4b) does not feed a disabled rule into the
	// DETECT engine's active set. Do not read "disabled" as "absent from the snapshot".
	Enabled bool

	// EntityGroupToken / EntityGroupVersion pin the rule's OPTIONAL scope to one
	// published dynamic entity-group version (ADR-062 S4): the rule fires only for
	// events whose resolved entity is a member of {EntityGroupToken}@{EntityGroupVersion}
	// (stamped into ResolvedEvent.ScopeMemberships at resolution, S3). Both are nil for an
	// unscoped rule (the profile-wide default). They are set together or not at all
	// (validateDetectionRuleScope enforces the pairing) and pin a SPECIFIC version so the
	// rule's target set can never drift under a later group-selector edit (ADR-062:
	// scope-to-{group}@{version}). Deliberately NOT json:"-" (unlike AuthoringGraph): the
	// scope IS runtime state, so it must ride buildProfileSnapshot's marshal into the frozen
	// version and thence the published-rule fact to event-processing (enabledSnapshotRules →
	// PublishedDetectionRule → the engine's ScopedRule). Enrollment of the pinned group@v for
	// membership maintenance follows PUBLISHED state, not this draft column: a scoped enabled
	// rule enrolls its group@v when its profile's active version is (re)published to include it
	// and GCs it when the last such live reference drops (see DetectionRuleScopeRef /
	// syncProfileScopeRefsAndEnroll) — a draft edit here has no enrollment side effect.
	EntityGroupToken   *string
	EntityGroupVersion *int32
}

// GroupScoped reports whether the rule carries a valid group scope (ADR-062 S4): a non-empty
// token AND a version. It is the SINGLE definition of "is this rule scoped", shared by the
// publish gate's validation flag, the published-rule fact projection, and the scope-ref sync,
// so those three sites can never drift into disagreeing about which rules are scoped. The
// pairing is enforced on write (validateDetectionRuleScope / normalizedRuleScope), so in
// practice both fields are set together or both nil; this predicate is defensive about a
// half-set pair (treats it as unscoped) and lets a caller safely deref both when it returns true.
func (dr *DetectionRule) GroupScoped() bool {
	return dr.EntityGroupToken != nil && *dr.EntityGroupToken != "" && dr.EntityGroupVersion != nil
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
	// AuthoringGraph is the optional opaque CanvasDefinition JSON (ADR-053 slice 9b). Nil ⇒ a
	// form-authored rule with no canvas sidecar; when present it must be a JSON object. It is
	// stored verbatim and never parsed here.
	AuthoringGraph *string
	// EntityGroupToken / EntityGroupVersion are the OPTIONAL group scope (ADR-062 S4): the
	// published dynamic entity-group version whose members the rule fires for. Both nil ⇒ an
	// unscoped (profile-wide) rule; set together ⇒ scoped. validateDetectionRuleScope enforces
	// the pairing and that the group is a published dynamic group with that version.
	EntityGroupToken   *string
	EntityGroupVersion *int32
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
