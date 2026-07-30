// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models in model/.
//
// A MIGRATION CONTAINS ITS OWN SHAPES. The live models are the current incarnation of these
// datatypes; these are a snapshot, and the difference between them is time. When a model
// gains a column, the column arrives in a NEW migration appended after the baseline — these
// stay exactly as they are.
//
// Self-contained down to the mixins: gorm.Model, rdb.TokenReference, rdb.NamedEntity and
// rdb.TenantScoped are INLINED rather than embedded, so a change in core cannot silently
// rewrite this migration from a PR that never touched this service. Field order matches the
// embedded layout exactly, because physical column order is part of the schema.
//
// 🔴 EVERY TYPE HERE PINS AN EXPLICIT TableName, AND THAT IS THE OPPOSITE OF THE OTHER
// AREAS' BASELINES — for a reason specific to this service's names. Gorm's naming strategy
// splits the "AI" initialism, so it would derive `a_iproviders` and
// `a_iprovider_tier_grants` from these type names. The runtime models pin the real names, so
// a migration that let gorm derive would create tables the CRUD path can never find — and
// migration-diff CANNOT catch that class of disagreement, because it compares a migrated
// schema against a golden and never exercises a query. The consequence of pinning is that
// gorm's TablePrefix (the functional area) is bypassed for DERIVED index names, which is why
// this area's indexes are `idx_ai_providers_token` rather than
// `idx_ai-inference_ai_providers_token`. Removing a TableName here would rename every index
// on that table AND break the runtime. Leave them.
//
// 🔴 THE Provider RELATIONS BELOW EXIST ONLY TO EMIT A FOREIGN KEY. They are deliberately
// absent from the runtime models: AutoMigrate emits the FK inline at CREATE TABLE, which
// works on Postgres and SQLite alike (a raw ALTER TABLE ... ADD CONSTRAINT is Postgres-only
// and fails the unit tests), while keeping the relation off the runtime model avoids gorm's
// association auto-save trying to upsert a provider whenever a grant is written.
//
// 🔑 WHY INLINED — stated carefully, because the obvious version of this argument is BACKWARDS.
// An earlier version of this comment claimed the golden gate now "largely catches" a core mixin
// edit, making the inlining rule belt-and-braces. That is false, and measurably so: nothing in
// this file is reachable from backend/core, so editing rdb.TenantScoped cannot change one byte of
// what this migration emits — there is no divergence for any gate to catch. INLINING IS WHAT
// REMOVES THE MIXIN FROM THE GATE'S FIELD OF VIEW. Crediting the gate for that coverage inverts
// cause and effect.
//
// The contrast is the useful part, and it is deliberate: core/secrets/migration.go DOES embed
// gorm.Model and rdb.TenantScoped, because the secret-store schema genuinely IS core's and a
// change to it SHOULD change all three consumers. Measured — adding a field to rdb.TenantScoped
// reddens migration-diff verify for exactly the three areas carrying a secret store, and no
// others. So the repo runs both patterns on purpose; the test is whose schema it is.
//
// What inlining therefore buys is not detection but the absence of the coupling: this migration
// cannot be rewritten from another module, so its meaning is fixed by this file alone. The cost it
// accepts, worth knowing: the snapshot silently diverges from the live model over time and nothing
// compares the two. That is intended — the snapshot is frozen by design — but no gate will tell you.
//
// Do not reach for a shared LOCAL base type either. These structs must be free to diverge; that
// divergence is the point, so there is no synchronisation requirement for DRY to serve, and a
// shared base only moves change-at-a-distance one level in — from another module to another
// migration in this package.

// aiProvider is the instance-scoped, operator-managed inference-provider list (ADR-056 §4).
//
// It carries NO tenant column and no instance-wide "active"/"baseline" mark. It has twice
// carried such a mark and twice given it up — `active` (which modelled "one model globally"
// and could express no packaging) and `is_platform_baseline` (a default every tier shared,
// which is not a per-tier default). The fallback rides a GRANT ROW instead
// (aiProviderTierGrant.IsDefault), which is what makes "AI is a tiered entitlement"
// structural: a tier that grants nothing has nowhere to put a default. Do not reintroduce a
// column here to simplify a lookup.
type aiProvider struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Token string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Kind     string `gorm:"not null;size:64"`
	Endpoint string `gorm:"size:512"`
	ModelID  string `gorm:"column:model;not null;size:128"`
	Params   datatypes.JSON
	// No gorm `default` on Enabled: a `default:true` would make gorm substitute the DB
	// default for the Go zero value (false) on Create, so a provider could never be
	// persisted DISABLED. The GraphQL contract is `enabled: Boolean!`, always explicit.
	Enabled bool `gorm:"not null"`
}

func (aiProvider) TableName() string { return "ai_providers" }

// aiFunctionAssignment stores a tenant's chosen model per AI function — the mechanism that
// REPLACED a derived default. The point is that the answer is STORED: its predecessor
// inferred which model served a call from properties of the grant sets, and every one of
// those inferences re-answered when an operator changed the set. A row keyed by
// (tenant, function) cannot be re-derived by anything an operator does elsewhere.
//
// No DeletedAt: assignments HARD-delete, which is what lets uix_ai_function_assignment be
// plain rather than partial-on-live. A soft delete would make that index count tombstones
// and refuse to re-assign a function that had previously been cleared.
type aiFunctionAssignment struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	Function   string      `gorm:"not null;size:64"`
	ProviderID uint        `gorm:"not null;index"`
	Provider   *aiProvider `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
}

func (aiFunctionAssignment) TableName() string { return "ai_function_assignments" }

// aiProviderTierGrant is a tier→provider offer (ADR-065 decision 10). No DeletedAt: grants
// hard-delete, so the unique indexes below need no live-row predicate.
type aiProviderTierGrant struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// One grant per (tier, provider): re-granting is idempotent, not a duplicate row.
	TierToken  string      `gorm:"not null;size:128;uniqueIndex:uix_ai_tier_grant_pair,priority:1"`
	ProviderID uint        `gorm:"not null;uniqueIndex:uix_ai_tier_grant_pair,priority:2;index"`
	Provider   *aiProvider `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
	// The tier's default model mark — at most one row per tier carries it. No gorm
	// `default` tag, for the reason given on aiProvider.Enabled.
	IsDefault bool `gorm:"not null"`
}

func (aiProviderTierGrant) TableName() string { return "ai_provider_tier_grants" }

// aiProviderTenantGrant is a per-tenant additive exception to the tier's menu. It carries no
// is_default column, and that asymmetry with the tier grant is load-bearing: the only default
// the schema can express is a tier's, which keeps the per-tenant exception purely additive.
type aiProviderTenantGrant struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	ProviderID uint        `gorm:"not null;index"`
	Provider   *aiProvider `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
}

func (aiProviderTenantGrant) TableName() string { return "ai_provider_tenant_grants" }
